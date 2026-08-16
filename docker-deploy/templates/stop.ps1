$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "lib.ps1")
Assert-Space
Invoke-Compose stop
Write-Host "服务已停止，Workspace、配置和数据库都保留。"

