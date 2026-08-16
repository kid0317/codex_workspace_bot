param([ValidateSet("mysql", "bot", "codex", "provider-proxy")][string]$Service = "bot")
$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "lib.ps1")
Assert-Space
Invoke-Compose logs --tail 200 $Service

