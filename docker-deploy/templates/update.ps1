param([string]$Manifest, [switch]$Check)
$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "lib.ps1")
Assert-Space
if ([string]::IsNullOrWhiteSpace($Manifest)) { throw "请用 -Manifest 指定 release-manifest.json。" }
$lockDir = Join-Path $PSScriptRoot "system\update.lock"
try { New-Item -ItemType Directory -Path $lockDir -ErrorAction Stop | Out-Null } catch { throw "另一个安装或更新正在运行。" }
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("codex-update-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    $manifestPath = Join-Path $tmp "release-manifest.json"
    if ($Manifest -match '^https?://') {
        Invoke-WebRequest -UseBasicParsing -Uri $Manifest -OutFile $manifestPath
        Invoke-WebRequest -UseBasicParsing -Uri ($Manifest + ".sha256") -OutFile ($manifestPath + ".sha256")
    } else {
        Copy-Item -LiteralPath $Manifest -Destination $manifestPath
        Copy-Item -LiteralPath ($Manifest + ".sha256") -Destination ($manifestPath + ".sha256")
    }
    $expected = ((Get-Content ($manifestPath + ".sha256") -Raw).Trim().Split()[0]).ToLowerInvariant()
    $actual = (Get-FileHash -Algorithm SHA256 $manifestPath).Hash.ToLowerInvariant()
    if ($expected -ne $actual) { throw "发行清单校验失败。" }
    $release = Get-Content $manifestPath -Raw | ConvertFrom-Json
    if ($release.image.digest -notmatch '^sha256:[0-9a-f]{64}$') { throw "镜像 digest 无效。" }
    Write-Host "可用版本：$($release.version)"
    Write-Host "镜像摘要：$($release.image.digest)"
    if ($Check) { return }

    $backupDir = Join-Path $PSScriptRoot ("system\backups\" + (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ"))
    New-Item -ItemType Directory -Path $backupDir -Force | Out-Null
    Copy-Item (Join-Path $PSScriptRoot ".env"), (Join-Path $PSScriptRoot "space.lock.json"), $manifestPath -Destination $backupDir
    Invoke-Compose up -d --wait mysql
    $backupPath = Join-Path $backupDir "mysql.sql"
    & docker compose --project-directory $PSScriptRoot -f (Join-Path $PSScriptRoot "compose.yaml") exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysqldump --no-tablespaces -u"$MYSQL_USER" "$MYSQL_DATABASE"' | Set-Content -Encoding utf8 $backupPath
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path $backupPath) -or (Get-Item $backupPath).Length -eq 0) { throw "数据库备份失败。" }
    Protect-CurrentUserFile $backupPath

    $envPath = Join-Path $PSScriptRoot ".env"
    $oldEnv = Get-Content $envPath -Raw
    $candidate = "$($release.image.repository)@$($release.image.digest)"
    $newEnv = [Regex]::Replace($oldEnv, '(?m)^BOT_IMAGE=.*$', "BOT_IMAGE=$candidate")
    [IO.File]::WriteAllText($envPath, $newEnv, [Text.UTF8Encoding]::new($false))
    try {
        Invoke-Compose pull
        Invoke-Compose config --quiet
        Invoke-Compose up -d
        if (-not (Wait-BotReady 180)) { throw "新版本未就绪。" }
        $lockPath = Join-Path $PSScriptRoot "space.lock.json"
        $spaceLock = Get-Content $lockPath -Raw | ConvertFrom-Json
        $spaceLock.version = $release.version
        $spaceLock.image_digest = $release.image.digest
        $spaceLock | ConvertTo-Json -Depth 10 | Set-Content -Encoding utf8 $lockPath
        Write-Host "更新完成：$($release.version)"
    } catch {
        [IO.File]::WriteAllText($envPath, $oldEnv, [Text.UTF8Encoding]::new($false))
        Invoke-Compose up -d
        throw "更新失败，已恢复旧镜像和配置。数据库备份在 $backupDir。原错误：$($_.Exception.Message)"
    }
} finally {
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $lockDir -Force -ErrorAction SilentlyContinue
}
