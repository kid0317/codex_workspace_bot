$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "lib.ps1")
Assert-Space
Invoke-Compose ps
if (Wait-BotReady 3) { Write-Host "状态：ready"; exit 0 }
$running = & docker compose --project-directory $PSScriptRoot -f (Join-Path $PSScriptRoot "compose.yaml") ps --status running --services
if ($running) { Write-Host "状态：degraded（容器在运行，但 Bot 尚未就绪）"; exit 2 }
Write-Host "状态：stopped"
exit 3

