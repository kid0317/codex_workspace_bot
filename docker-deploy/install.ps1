$ErrorActionPreference = "Stop"
$deployDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$imageDefault = "crpi-0c1kby082wk3ovcx.cn-hangzhou.personal.cr.aliyuncs.com/codex-workspace/codex-workspace-bot:0.1.0"

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw "没有找到 Docker。请先安装并打开 Docker Desktop。" }
docker info *> $null
if ($LASTEXITCODE -ne 0) { throw "Docker 没有启动。请先打开 Docker Desktop，等鲸鱼图标稳定后再试。" }
docker compose version *> $null
if ($LASTEXITCODE -ne 0) { throw "需要 Docker Compose v2。" }

$defaultPath = Join-Path (Get-Location) "codex-space"
$installPath = Read-Host "把 Space 安装到哪里？[$defaultPath]"
if ([string]::IsNullOrWhiteSpace($installPath)) { $installPath = $defaultPath }
$installPath = [IO.Path]::GetFullPath($installPath)
New-Item -ItemType Directory -Force -Path $installPath | Out-Null
if (Get-ChildItem -LiteralPath $installPath -Force | Select-Object -First 1) { throw "目标文件夹不是空的。为避免覆盖文件，安装已停止。" }

Write-Host "请选择模型服务："
Write-Host "1. 阿里百炼 Responses"
Write-Host "2. DeepSeek Responses"
$providerChoice = Read-Host "请输入 1 或 2"
switch ($providerChoice) {
    "1" { $providerKind = "bailian-responses"; $defaultBase = "https://dashscope.aliyuncs.com/api/v2/apps"; $defaultModel = "qwen3-coder-plus" }
    "2" { $providerKind = "deepseek-responses"; $defaultBase = "https://api.deepseek.com"; $defaultModel = "deepseek-chat" }
    default { throw "只能输入 1 或 2。" }
}
$providerBase = Read-Host "Base URL [$defaultBase]"
if ([string]::IsNullOrWhiteSpace($providerBase)) { $providerBase = $defaultBase }
if ($providerBase -notmatch '^https://') { throw "Base URL 必须以 https:// 开头。" }
$model = Read-Host "模型名称 [$defaultModel]"
if ([string]::IsNullOrWhiteSpace($model)) { $model = $defaultModel }
if ($model -notmatch '^[A-Za-z0-9._:/-]+$') { throw "模型名称格式不正确。" }
$secureProviderKey = Read-Host "API Key（输入时不会显示）" -AsSecureString
$botPort = Read-Host "Bot 本机端口 [8080]"
if ([string]::IsNullOrWhiteSpace($botPort)) { $botPort = "8080" }
$parsedPort = 0
if (-not [int]::TryParse($botPort, [ref]$parsedPort) -or $parsedPort -lt 1024 -or $parsedPort -gt 65535) { throw "端口必须是 1024 到 65535 的数字。" }

function New-RandomHex([int]$Bytes = 32) {
    $buffer = New-Object byte[] $Bytes
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($buffer) } finally { $rng.Dispose() }
    return -join ($buffer | ForEach-Object { $_.ToString("x2") })
}

$spaceId = "space-" + (New-RandomHex).Substring(0, 16)
$projectName = "codex-space-" + $spaceId.Substring($spaceId.Length - 8)
$staging = "$installPath.staging.$PID"
try {
    New-Item -ItemType Directory -Path $staging | Out-Null
    Copy-Item -Path (Join-Path $deployDir "templates\*") -Destination $staging -Recurse -Force
    foreach ($dir in @(".secrets", "apps", "attachments", "logs", "system\backups", "system\codex-home", "system\home", "system\bot-home")) { New-Item -ItemType Directory -Force -Path (Join-Path $staging $dir) | Out-Null }
    Copy-Item (Join-Path $deployDir "release\release-manifest.json") (Join-Path $staging "system\release-manifest.json")

    $dbRoot = New-RandomHex
    $dbPassword = New-RandomHex
    $attachmentKey = New-RandomHex
    $actionKey = New-RandomHex
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureProviderKey)
    try {
        $providerKey = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
        if ([string]::IsNullOrEmpty($providerKey) -or $providerKey.Contains("`n") -or $providerKey.Contains("`r")) { throw "API Key 不能为空或包含换行。" }
        [IO.File]::WriteAllLines((Join-Path $staging ".secrets\provider.env"), @("PROVIDER_UPSTREAM_BASE_URL=$providerBase", "PROVIDER_API_KEY=$providerKey", "PROVIDER_PROXY_LISTEN=0.0.0.0:8090"), [Text.UTF8Encoding]::new($false))
    } finally {
        if ($bstr -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
        $providerKey = $null
    }
    [IO.File]::WriteAllLines((Join-Path $staging ".secrets\mysql.env"), @("MYSQL_ROOT_PASSWORD=$dbRoot", "MYSQL_DATABASE=codex_workspace_bot", "MYSQL_USER=codex_workspace_bot", "MYSQL_PASSWORD=$dbPassword"), [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllLines((Join-Path $staging ".secrets\bot.env"), @("CODEX_WORKSPACE_BOT_DB_PASSWORD=$dbPassword", "CODEX_WORKSPACE_BOT_ATTACHMENT_KEY_V1=$attachmentKey", "CODEX_WORKSPACE_BOT_ACTION_RESULT_KEY_V1=$actionKey"), [Text.UTF8Encoding]::new($false))
    $dbRoot = $dbPassword = $attachmentKey = $actionKey = $null

    [IO.File]::WriteAllLines((Join-Path $staging ".env"), @("COMPOSE_PROJECT_NAME=$projectName", "SPACE_ID=$spaceId", "BOT_IMAGE=$imageDefault", "MYSQL_IMAGE=mysql:8.4", "BOT_HOST_PORT=$botPort", "LOCAL_UID=10001", "LOCAL_GID=10001", "DEFAULT_MODEL=$model", "PROVIDER_KIND=$providerKind"), [Text.UTF8Encoding]::new($false))
    $codexConfig = Get-Content (Join-Path $staging "system\codex-home\config.toml") -Raw
    [IO.File]::WriteAllText((Join-Path $staging "system\codex-home\config.toml"), $codexConfig.Replace("__MODEL__", $model), [Text.UTF8Encoding]::new($false))
    $spaceLock = [ordered]@{ schema_version = 1; space_id = $spaceId; version = "0.1.0"; image_digest = ""; provider_kind = $providerKind }
    $spaceLock | ConvertTo-Json | Set-Content -Encoding utf8 (Join-Path $staging "space.lock.json")
    [IO.File]::WriteAllLines((Join-Path $staging ".gitignore"), @(".env", ".secrets/", "system/backups/", "logs/", "attachments/"), [Text.UTF8Encoding]::new($false))

    & docker compose --project-directory $staging -f (Join-Path $staging "compose.yaml") config --quiet
    if ($LASTEXITCODE -ne 0) { throw "Compose 配置校验失败。" }
    & docker compose --project-directory $staging -f (Join-Path $staging "compose.yaml") pull
    if ($LASTEXITCODE -ne 0) { throw "镜像下载失败。" }
    Copy-Item -Path (Join-Path $staging "*") -Destination $installPath -Recurse -Force
    Get-ChildItem -LiteralPath $staging -Force | Where-Object { $_.Name -like ".*" } | ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination $installPath -Recurse -Force }
} finally {
    Remove-Item -LiteralPath $staging -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host "Space 已安装到：$installPath"
$addWorkspace = Read-Host "现在添加第一个 Workspace 吗？[Y/n]"
if ($addWorkspace -notmatch '^[Nn]$') { & (Join-Path $installPath "manage.ps1") }
$startNow = Read-Host "现在启动服务吗？[Y/n]"
if ($startNow -notmatch '^[Nn]$') { & (Join-Path $installPath "start.ps1") } else { Write-Host "以后进入该文件夹，运行 .\start.ps1 即可启动。" }

