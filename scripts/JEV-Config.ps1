# Local per-process JEV configuration. Reading this file makes no network calls.
function ConvertTo-有据JEVProxyUri {
    param([Parameter(Mandatory)][string]$ProxyServer)
    $server = $ProxyServer.Trim()
    if ($server -match '=') {
        $mapped = @{}
        foreach ($entry in $server.Split(';')) {
            $pair = $entry.Split('=', 2)
            if ($pair.Count -eq 2) { $mapped[$pair[0].Trim().ToLowerInvariant()] = $pair[1].Trim() }
        }
        if ($mapped.ContainsKey('https')) { $server = $mapped['https'] }
        elseif ($mapped.ContainsKey('http')) { $server = $mapped['http'] }
        else { throw 'Windows has no HTTP/HTTPS proxy endpoint. PAC and SOCKS-only settings are unsupported.' }
    }
    if ($server -notmatch '^[a-zA-Z][a-zA-Z0-9+.-]*://') { $server = 'http://' + $server }
    $proxyUri = $null
    if (-not [Uri]::TryCreate($server, [UriKind]::Absolute, [ref]$proxyUri) -or
        $proxyUri.Scheme -notin @('http', 'https') -or -not $proxyUri.Host -or
        $proxyUri.UserInfo -or $proxyUri.Query -or $proxyUri.Fragment -or $proxyUri.AbsolutePath -ne '/' -or
        $proxyUri.Port -lt 1 -or $proxyUri.Port -gt 65535) {
        throw 'Invalid HTTP/HTTPS proxy endpoint. Credentials, paths, queries, and fragments are unsupported.'
    }
    return $proxyUri
}

function Get-有据JEVSystemProxyUri {
    $settings = Get-ItemProperty -LiteralPath 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' -ErrorAction SilentlyContinue
    if ($settings.ProxyEnable -ne 1 -or [string]::IsNullOrWhiteSpace($settings.ProxyServer)) {
        throw 'No enabled Windows HTTP/HTTPS proxy is configured for JEV. Start your existing proxy or change jev.settings.json.'
    }
    return ConvertTo-有据JEVProxyUri -ProxyServer $settings.ProxyServer
}

function Get-有据JEVEnvironment {
    param([string]$Root = $PSScriptRoot)
    $settingsPath = Join-Path $Root 'jev.settings.json'
    if (-not (Test-Path -LiteralPath $settingsPath)) { return @{} }
    $settings = Get-Content -LiteralPath $settingsPath -Raw | ConvertFrom-Json
    if ($settings.mode -notin @('off', 'shadow', 'assist') -or
        $settings.use_system_proxy -isnot [bool] -or $settings.allow_synthetic_egress -isnot [bool] -or
        $settings.base_url -cne 'https://api.typesafe.ai' -or
        $settings.model -cnotmatch '^jev-[A-Za-z0-9_.-]+$' -or
        $settings.timeout_ms -isnot [ValueType] -or $settings.timeout_ms -lt 500 -or $settings.timeout_ms -gt 30000 -or
        $settings.timeout_ms -ne [int]$settings.timeout_ms) {
        throw 'Invalid jev.settings.json. Use the official API, a JEV model, supported mode, and a timeout of 500..30000 ms.'
    }
    $result = @{
        TYPESAFE_BASE_URL = [string]$settings.base_url
        TYPESAFE_MODEL = [string]$settings.model
        TYPESAFE_TIMEOUT_MS = [string]$settings.timeout_ms
        TYPESAFE_ALLOW_SYNTHETIC_EGRESS = ([string]$settings.allow_synthetic_egress).ToLowerInvariant()
    }
    $key = [Environment]::GetEnvironmentVariable('TYPESAFE_API_KEY', 'Process')
    if ([string]::IsNullOrWhiteSpace($key)) { $key = [Environment]::GetEnvironmentVariable('TYPESAFE_API_KEY', 'User') }
    $result['TYPESAFE_API_KEY'] = $key
    $mode = [Environment]::GetEnvironmentVariable('JEV_MODE', 'Process')
    if ([string]::IsNullOrWhiteSpace($mode)) { $mode = [string]$settings.mode }
    if ($mode -notin @('off', 'shadow', 'assist')) { throw 'JEV_MODE must be off, shadow, or assist.' }
    $result['JEV_MODE'] = $mode
    $proxy = [Environment]::GetEnvironmentVariable('TYPESAFE_PROXY_URL', 'Process')
    if (-not [string]::IsNullOrWhiteSpace($proxy)) {
        $result['TYPESAFE_PROXY_URL'] = (ConvertTo-有据JEVProxyUri -ProxyServer $proxy).AbsoluteUri
    } elseif ($settings.use_system_proxy) {
        $result['TYPESAFE_PROXY_URL'] = (Get-有据JEVSystemProxyUri).AbsoluteUri
    } else { $result['TYPESAFE_PROXY_URL'] = '' }
    return $result
}
