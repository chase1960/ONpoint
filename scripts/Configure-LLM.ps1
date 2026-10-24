param(
    [ValidateSet('Bailian', 'DeepSeek')][string]$Provider = 'Bailian',
    [string]$WorkspaceId = '',
    [string]$Model = ''
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'LLM-Config.ps1')
if ($env:OS -ne 'Windows_NT') { throw 'This encrypted storage requires Windows.' }
if ($WorkspaceId -and ($Provider -ne 'Bailian' -or $WorkspaceId -cnotmatch '^[a-zA-Z0-9-]+$')) {
    throw 'WorkspaceId must be a Beijing Bailian workspace ID containing only letters, numbers or hyphens.'
}
if (!$Model) { $Model = if ($Provider -eq 'Bailian') { 'qwen-flash' } else { 'deepseek-flash' } }
$baseUrl = if ($Provider -eq 'DeepSeek') {
    'https://api.deepseek.com'
} elseif ($WorkspaceId) {
    "https://$WorkspaceId.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
} else {
    'https://dashscope.aliyuncs.com/compatible-mode/v1'
}
Assert-有据LLMEndpoint -Provider $Provider -BaseUrl $baseUrl
Write-Host "Provider: $Provider | Model: $Model"
Write-Host 'Paste the API Key below. Input is hidden; do not send the key to chat.'
$secret = Read-Host 'API Key' -AsSecureString
try {
    if ($secret.Length -lt 12) { throw 'The key appears empty or incomplete. Configuration was not saved.' }
    $credential = New-Object System.Management.Automation.PSCredential($Provider, $secret)
    $secretDirectory = Join-Path $PSScriptRoot '.secrets'
    New-Item -ItemType Directory -Force -Path $secretDirectory | Out-Null
    $credential | Export-Clixml -LiteralPath (Join-Path $secretDirectory 'llm.credential.xml')
    [ordered]@{ provider = $Provider; model = $Model; base_url = $baseUrl } |
        ConvertTo-Json | Set-Content -LiteralPath (Join-Path $secretDirectory 'llm.settings.json') -Encoding UTF8
    Write-Host 'Saved with Windows encryption for this Windows account and computer. No API request was sent.'
    Write-Host 'Run .\Test-LLM.ps1 to check local configuration, or .\Test-LLM.ps1 -Online for one small API request.'
} finally {
    if ($null -ne $secret) { $secret.Dispose() }
    $credential = $null
}
