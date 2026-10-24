$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'JEV-Config.ps1')
$taskNames = @('TYPESAFE_API_KEY', 'TYPESAFE_PROXY_URL', 'JEV_MODE', 'HTTP_PROXY', 'HTTPS_PROXY')
$taskBefore = @{}
foreach ($name in $taskNames) { $taskBefore[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
try {
    foreach ($name in $taskNames) { [Environment]::SetEnvironmentVariable($name, $null, 'Process') }
    $env:TYPESAFE_API_KEY = 'offline-test-placeholder'
    function Get-有据JEVSystemProxyUri { return [Uri]'http://127.0.0.1:7890/' }
    $settings = Get-有据JEVEnvironment -Root $PSScriptRoot
    if ($settings.TYPESAFE_API_KEY -ne 'offline-test-placeholder' -or
        $settings.TYPESAFE_PROXY_URL -ne 'http://127.0.0.1:7890/' -or
        $settings.TYPESAFE_MODEL -ne 'jev-1.14.0' -or $settings.JEV_MODE -ne 'shadow' -or
        $settings.TYPESAFE_TIMEOUT_MS -ne '15000') { throw 'Configuration resolution failed.' }
    if ($env:HTTP_PROXY -or $env:HTTPS_PROXY) { throw 'Configuration modified global proxy variables.' }
    $env:JEV_MODE = 'off'
    $env:TYPESAFE_PROXY_URL = 'http://127.0.0.1:7891'
    $settings = Get-有据JEVEnvironment -Root $PSScriptRoot
    if ($settings.JEV_MODE -ne 'off' -or $settings.TYPESAFE_PROXY_URL -ne 'http://127.0.0.1:7891/') { throw 'Explicit process overrides were lost.' }
    if ((ConvertTo-有据JEVProxyUri 'http=localhost:7892;https=localhost:7893').Port -ne 7893) { throw 'HTTPS proxy mapping failed.' }
    foreach ($badProxy in @('socks5://localhost:7890', 'http://user:secret@localhost:7890', 'http://localhost:7890/path', 'socks=localhost:7890')) {
        $rejected = $false
        try { ConvertTo-有据JEVProxyUri $badProxy | Out-Null } catch { $rejected = $true }
        if (-not $rejected) { throw 'Invalid proxy was accepted.' }
    }
    Write-Output 'PASS: JEV configuration, proxy mapping, explicit overrides, invalid proxy rejection, no global proxy changes. No network requests.'
} finally {
    foreach ($name in $taskNames) { [Environment]::SetEnvironmentVariable($name, $taskBefore[$name], 'Process') }
    if ($settings) { $settings.Clear() }
}
