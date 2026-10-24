# Performs two real, small model calls using synthetic business conclusions only.
param()
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'LLM-Config.ps1')
$llmConfig = Get-ONpointLLMConfig
if ($null -eq $llmConfig) { throw 'Run Configure-LLM.ps1 before the live MVP test.' }
$goCommand = (Get-Command go -ErrorAction Stop).Source
$variables = @{
    LLM_PROVIDER = $llmConfig.Provider
    LLM_MODEL = $llmConfig.Model
    LLM_BASE_URL = $llmConfig.BaseUrl
    LLM_API_KEY = $llmConfig.Credential.GetNetworkCredential().Password
    LLM_MODE = 'grounded'
    ONPOINT_TEST_LLM_LIVE = '1'
    GOTOOLCHAIN = 'local'
    GOCACHE = (Join-Path $PSScriptRoot 'cache/go-build')
    GOTMPDIR = (Join-Path $PSScriptRoot 'cache/tmp')
    TEMP = (Join-Path $PSScriptRoot 'cache/tmp')
    TMP = (Join-Path $PSScriptRoot 'cache/tmp')
}
if (-not [string]::IsNullOrWhiteSpace($env:ONPOINT_GOMODCACHE)) { $variables.GOMODCACHE = $env:ONPOINT_GOMODCACHE }
New-Item -ItemType Directory -Force $variables.GOCACHE, $variables.GOTMPDIR | Out-Null
$previous = @{}
foreach ($name in $variables.Keys) { $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
try {
    foreach ($name in $variables.Keys) { [Environment]::SetEnvironmentVariable($name, $variables[$name], 'Process') }
    Write-Host 'Testing the actual MVP API: read-only explanation, proposed-plan explanation, then confirmation-gated sandbox execution.'
    & $goCommand -C $PSScriptRoot test -run '^TestLLMLiveSyntheticMVP$' -v -count=1 -timeout 60s
    if ($LASTEXITCODE -ne 0) { throw 'Live MVP test failed. See the sanitized status above.' }
} finally {
    foreach ($name in $previous.Keys) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
    $variables.Clear()
    $llmConfig = $null
}
