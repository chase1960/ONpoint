# Local Windows credential storage. Dot-source this file; it makes no network calls.
function Get-ONpointLLMConfig {
    param([string]$Root = $PSScriptRoot)
    $settingsPath = Join-Path $Root '.secrets/llm.settings.json'
    $credentialPath = Join-Path $Root '.secrets/llm.credential.xml'
    if (!(Test-Path -LiteralPath $settingsPath) -or !(Test-Path -LiteralPath $credentialPath)) { return $null }
    try {
        $settings = Get-Content -LiteralPath $settingsPath -Raw | ConvertFrom-Json
        Assert-ONpointLLMEndpoint -Provider $settings.provider -BaseUrl $settings.base_url
        $credential = Import-Clixml -LiteralPath $credentialPath
        if ($credential -isnot [System.Management.Automation.PSCredential]) { throw 'Invalid credential type.' }
        if ([string]::IsNullOrWhiteSpace($settings.model)) { throw 'Missing model.' }
        return [PSCustomObject]@{
            Provider = [string]$settings.provider
            BaseUrl = [string]$settings.base_url
            Model = [string]$settings.model
            Credential = $credential
        }
    } catch {
        throw 'Cannot load local LLM configuration. Run Configure-LLM.ps1 again with the same Windows account.'
    }
}

function Assert-ONpointLLMEndpoint {
    param([string]$Provider, [string]$BaseUrl)
    $valid = switch ($Provider) {
        'Bailian' { $BaseUrl -cmatch '^https://(dashscope\.aliyuncs\.com|[a-zA-Z0-9-]+\.cn-beijing\.maas\.aliyuncs\.com)/compatible-mode/v1$' }
        'DeepSeek' { $BaseUrl -ceq 'https://api.deepseek.com' }
        default { $false }
    }
    if (!$valid) { throw 'Only the official Beijing Bailian or DeepSeek endpoint is allowed.' }
}
