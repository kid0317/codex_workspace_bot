param([switch]$Purge, [switch]$DeleteBackups)
$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "lib.ps1")
Assert-Space
if (-not $Purge) {
    Invoke-Compose down
    Write-Host "服务和网络已卸载，Space、数据库 volume、Workspace 与 Secret 均保留。"
    return
}
$lock = Get-Content (Join-Path $PSScriptRoot "space.lock.json") -Raw | ConvertFrom-Json
$typed = Read-Host "请输入 Space ID $($lock.space_id) 进行确认"
if ($typed -ne $lock.space_id) { throw "Space ID 不一致，已取消。" }
Invoke-Compose down -v
$generated = @("compose.yaml", ".env", "start.sh", "stop.sh", "status.sh", "logs.sh", "manage.sh", "update.sh", "uninstall.sh", "start.ps1", "stop.ps1", "status.ps1", "logs.ps1", "manage.ps1", "update.ps1", "uninstall.ps1", "lib.sh", "lib.ps1")
foreach ($name in $generated) { Remove-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Force -ErrorAction SilentlyContinue }
foreach ($dir in @("config", ".secrets", "system\codex-home", "system\home", "system\bot-home", "logs", "attachments")) { Remove-Item -LiteralPath (Join-Path $PSScriptRoot $dir) -Recurse -Force -ErrorAction SilentlyContinue }
if ($DeleteBackups) { Remove-Item -LiteralPath (Join-Path $PSScriptRoot "system\backups") -Recurse -Force -ErrorAction SilentlyContinue }
Remove-Item -LiteralPath (Join-Path $PSScriptRoot "space.lock.json") -Force
Write-Host "受管运行环境已删除。apps/ 和默认备份仍保留。"

