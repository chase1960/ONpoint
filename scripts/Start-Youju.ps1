param([string]$Address = '127.0.0.1:8088', [string]$Data = 'data/ota-routing.db', [switch]$CheckJEV, [switch]$DisableLLM)
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
. (Join-Path $PSScriptRoot 'LLM-Config.ps1')
$llmConfig = Get-有据LLMConfig
. (Join-Path $PSScriptRoot 'JEV-Config.ps1')
$jevEnvironment = Get-有据JEVEnvironment
$cachedToolchain = 'D:\Download\有据\cache\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.25.14.windows-amd64\bin\go.exe'
$goCommand = if (Test-Path -LiteralPath $cachedToolchain) { $cachedToolchain } else { (Get-Command go -ErrorAction Stop).Source }
$env:GOTOOLCHAIN = 'local'
$cachedModules = 'D:\Download\有据\cache\go\pkg\mod'
if (Test-Path -LiteralPath $cachedModules) { $env:GOMODCACHE = $cachedModules }
$env:GOCACHE = if (Test-Path -LiteralPath 'D:\Download\有据\cache\go-build') { 'D:\Download\有据\cache\go-build' } else { Join-Path $PSScriptRoot 'cache/go-build' }
$env:GOTMPDIR = Join-Path $PSScriptRoot 'cache/tmp'
New-Item -ItemType Directory -Force $env:GOCACHE, $env:GOTMPDIR | Out-Null
$env:TEMP = $env:GOTMPDIR
$env:TMP = $env:GOTMPDIR
$llmEnvironmentNames = @('LLM_PROVIDER', 'LLM_BASE_URL', 'LLM_MODEL', 'LLM_API_KEY', 'LLM_MODE', 'DASHSCOPE_API_KEY', 'DEEPSEEK_API_KEY')
$previousLLMEnvironment = @{}
foreach ($name in $llmEnvironmentNames) { $previousLLMEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
$previousJEVEnvironment = @{}
foreach ($name in $jevEnvironment.Keys) { $previousJEVEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
try {
    foreach ($name in $jevEnvironment.Keys) { [Environment]::SetEnvironmentVariable($name, $jevEnvironment[$name], 'Process') }
    if ($jevEnvironment.Count -gt 0) {
        Write-Host "JEV configuration loaded: $($env:TYPESAFE_MODEL) / $($env:JEV_MODE); proxy configured: $(-not [string]::IsNullOrWhiteSpace($env:TYPESAFE_PROXY_URL))."
    }
    if ($CheckJEV) {
        & $goCommand run . -check-jev
        if ($LASTEXITCODE -ne 0) { throw 'JEV connection check failed. No inference was requested.' }
        return
    }
    if ($null -ne $llmConfig) {
        $env:LLM_MODE = if ($DisableLLM) { 'off' } else { 'grounded' }
        $env:LLM_PROVIDER = $llmConfig.Provider
        $env:LLM_BASE_URL = $llmConfig.BaseUrl
        $env:LLM_MODEL = $llmConfig.Model
        $env:LLM_API_KEY = $llmConfig.Credential.GetNetworkCredential().Password
        if ($llmConfig.Provider -eq 'Bailian') { $env:DASHSCOPE_API_KEY = $env:LLM_API_KEY }
        if ($llmConfig.Provider -eq 'DeepSeek') { $env:DEEPSEEK_API_KEY = $env:LLM_API_KEY }
        Write-Host "LLM configuration loaded: $($llmConfig.Provider) / $($llmConfig.Model) / $($env:LLM_MODE). Grounded explanations use synthetic server conclusions only."
    }
    if ($DisableLLM) { $env:LLM_MODE = 'off' }
    & $goCommand run . -addr $Address -data $Data -fixtures fixtures/ota.json
} finally {
    foreach ($name in $llmEnvironmentNames) { [Environment]::SetEnvironmentVariable($name, $previousLLMEnvironment[$name], 'Process') }
    foreach ($name in $previousJEVEnvironment.Keys) { [Environment]::SetEnvironmentVariable($name, $previousJEVEnvironment[$name], 'Process') }
    $jevEnvironment.Clear()
    $llmConfig = $null
}
