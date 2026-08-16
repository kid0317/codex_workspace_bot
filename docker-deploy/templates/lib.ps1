$ErrorActionPreference = "Stop"
$script:SpaceRoot = Split-Path -Parent $MyInvocation.MyCommand.Path

function Assert-Space {
    if (-not (Test-Path (Join-Path $script:SpaceRoot "space.lock.json"))) { throw "这不是安装器管理的 Space：缺少 space.lock.json" }
    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw "没有找到 Docker，请先安装 Docker Desktop。" }
    docker info *> $null
    if ($LASTEXITCODE -ne 0) { throw "Docker 还没有启动，请打开 Docker Desktop。" }
}

function Invoke-Compose {
    & docker compose --project-directory $script:SpaceRoot -f (Join-Path $script:SpaceRoot "compose.yaml") @args
    if ($LASTEXITCODE -ne 0) { throw "Docker Compose 命令失败。" }
}

function Wait-BotReady([int]$TimeoutSeconds = 180) {
    $portLine = Get-Content (Join-Path $script:SpaceRoot ".env") | Where-Object { $_ -like "BOT_HOST_PORT=*" } | Select-Object -First 1
    $port = $portLine.Split("=", 2)[1]
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            Invoke-RestMethod -Uri "http://127.0.0.1:$port/readyz" -TimeoutSec 3 | Out-Null
            return $true
        } catch { Start-Sleep -Seconds 2 }
    }
    return $false
}

