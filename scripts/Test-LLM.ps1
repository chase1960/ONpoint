param([switch]$Online)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'LLM-Config.ps1')
$config = Get-有据LLMConfig
if ($null -eq $config) {
    Write-Host 'Not configured. Run .\Configure-LLM.ps1 first. No API request was sent.'
    exit 1
}
Write-Host "Local configuration loaded | Provider: $($config.Provider) | Model: $($config.Model)"
if (!$Online) {
    Write-Host 'Local check passed. This does not verify key validity, balance or model access. No API request was sent.'
    exit 0
}
$body = @{
    model = $config.Model
    messages = @(@{ role = 'user'; content = 'Reply with OK only.' })
    max_tokens = 16
    stream = $false
}
if ($config.Provider -eq 'Bailian') { $body.enable_thinking = $false }
if ($config.Provider -eq 'DeepSeek') { $body.thinking = @{ type = 'disabled' } }
Add-Type -AssemblyName System.Net.Http
# Do not follow redirects with a credential attached.
$handler = New-Object System.Net.Http.HttpClientHandler
$handler.AllowAutoRedirect = $false
$client = New-Object System.Net.Http.HttpClient($handler)
$client.Timeout = [TimeSpan]::FromSeconds(30)
$response = $null
$content = $null
try {
    $client.DefaultRequestHeaders.Authorization = New-Object System.Net.Http.Headers.AuthenticationHeaderValue(
        'Bearer', $config.Credential.GetNetworkCredential().Password
    )
    $content = New-Object System.Net.Http.StringContent(
        ($body | ConvertTo-Json -Depth 6 -Compress), [System.Text.Encoding]::UTF8, 'application/json'
    )
    $response = $client.PostAsync("$($config.BaseUrl)/chat/completions", $content).GetAwaiter().GetResult()
    if (!$response.IsSuccessStatusCode) {
        $code = [int]$response.StatusCode
        $hint = switch ($code) {
            401 { 'Check the API Key and region.' }
            402 { 'Check account balance in the provider console.' }
            403 { 'Check model permission and account service status.' }
            404 { 'Check the model name and endpoint.' }
            429 { 'Rate limit or quota reached. Check the provider console.' }
            default { 'Check the provider console. No automatic retry was made.' }
        }
        Write-Host "Online check failed (HTTP $code). $hint"
        # Print only recognized public error codes, never the raw response or credentials.
        $knownErrors = @{
            'AccessDenied.Unpurchased' = 'Bailian service is not activated. Open the console and activate the service.'
            'Model.AccessDenied' = 'This workspace or API Key lacks permission for the selected model.'
            'Workspace.AccessDenied' = 'This API Key lacks permission for the selected workspace.'
            'App.AccessDenied' = 'Application or workspace access is denied.'
            'Endpoint.AccessDenied' = 'The model endpoint is unavailable. Check the model lifecycle.'
            'AllocationQuota.FreeTierOnly' = 'Free quota is unavailable or exhausted with free-tier-only enabled. Check quota before choosing paid access.'
            'Arrearage' = 'Account billing is not in good standing. Check balance and unpaid bills.'
            'AccessDenied' = 'Model access denied. Check model permissions and availability.'
            'access_denied' = 'Model access denied. Check model permissions and availability.'
            'InvalidApiKey' = 'Check the API Key and region.'
            'invalid_api_key' = 'Check the API Key and region.'
        }
        try {
            $errorResponse = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult() | ConvertFrom-Json
            $providerCode = if ($errorResponse.error.code) { [string]$errorResponse.error.code } else { [string]$errorResponse.code }
            if ($knownErrors.ContainsKey($providerCode)) {
                Write-Host "Provider error: $providerCode"
                Write-Host $knownErrors[$providerCode]
            } else {
                Write-Host 'No recognized provider error code. Check service activation, model permission and free quota in the console.'
            }
        } catch {
            Write-Host 'No readable provider error code. Check the provider console.'
        }
        exit 1
    }
    $result = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult() | ConvertFrom-Json
    if (!$result.choices -or [string]::IsNullOrWhiteSpace($result.choices[0].message.content)) {
        Write-Host 'The API responded without text. Connectivity alone does not prove a successful completion.'
        exit 1
    }
    Write-Host 'Online check passed: the model returned text. No customer data was sent.'
    if ($result.usage) { Write-Host "Reported tokens: $($result.usage.total_tokens)" }
} catch {
    Write-Host 'Online check failed: network, TLS, timeout or response format error. No automatic retry was made.'
    exit 1
} finally {
    if ($null -ne $response) { $response.Dispose() }
    if ($null -ne $content) { $content.Dispose() }
    $client.Dispose()
    $handler.Dispose()
    $config = $null
}
