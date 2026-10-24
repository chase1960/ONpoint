param([string]$Address='127.0.0.1:8123')
$ErrorActionPreference='Stop'
$bundlePath=$PSScriptRoot
$runtimePath=Join-Path $bundlePath 'runtime'
$binaryPath=Join-Path $runtimePath 'onpoint-demo.exe'
if(-not (Test-Path -LiteralPath $binaryPath)){throw 'Missing runtime/onpoint-demo.exe. Use the full delivery package.'}
$previousModeJEV=$env:JEV_MODE
$previousModeLLM=$env:LLM_MODE
try {
 $env:JEV_MODE='off'
 $env:LLM_MODE='off'
 Set-Location -LiteralPath $bundlePath
 Write-Host "ONpoint live demo: http://$Address/story/index.html#experience"
 Write-Host 'Synthetic orders only. The UI starts in replay mode; click the backend connection button for live API requests.'
 & $binaryPath -addr $Address -data (Join-Path $runtimePath 'live-cases.db') -fixtures (Join-Path $runtimePath 'ota.json') -rules (Join-Path $runtimePath 'story-rules.json')
} finally {
 $env:JEV_MODE=$previousModeJEV
 $env:LLM_MODE=$previousModeLLM
}
