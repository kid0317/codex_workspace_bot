$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "lib.ps1")
Assert-Space
Invoke-Compose config --quiet
Invoke-Compose up -d
if (-not (Wait-BotReady 180)) { Invoke-Compose ps; throw "服务没有在 3 分钟内就绪，请运行 .\logs.ps1 查看原因。" }
Write-Host "启动成功。"
& (Join-Path $PSScriptRoot "status.ps1")

