$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "lib.ps1")
Assert-Space

$appName = Read-Host "Workspace 名称（英文、数字、短横线）"
if ($appName -notmatch '^[a-z0-9][a-z0-9-]{0,62}$') { throw "名称格式不对。" }
$sourceDir = Read-Host "Workspace 文件夹完整路径"
if (-not (Test-Path -LiteralPath $sourceDir -PathType Container)) { throw "找不到这个文件夹。" }
$appId = Read-Host "飞书 App ID"
$secureSecret = Read-Host "飞书 App Secret（输入时不会显示）" -AsSecureString
$defaultModel = ((Get-Content (Join-Path $PSScriptRoot ".env") | Where-Object { $_ -like "DEFAULT_MODEL=*" } | Select-Object -First 1).Split("=", 2)[1])
$model = Read-Host "模型 [$defaultModel]"
if ([string]::IsNullOrWhiteSpace($model)) { $model = $defaultModel }
if ($model -notmatch '^[A-Za-z0-9._:/-]+$') { throw "模型名称格式不对。" }

$target = Join-Path $PSScriptRoot "apps\$appName\workspace"
if (Test-Path -LiteralPath $target) {
    $answer = Read-Host "同名 Workspace 已存在，覆盖运行副本吗？[y/N]"
    if ($answer -notmatch '^[Yy]$') { return }
    Remove-Item -LiteralPath $target -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $target | Out-Null
Get-ChildItem -LiteralPath $sourceDir -Force | Copy-Item -Destination $target -Recurse -Force

$secretPath = Join-Path $PSScriptRoot ".secrets\.bootstrap-$appName-$PID"
$bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureSecret)
try {
    $plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
    if ([string]::IsNullOrEmpty($plain) -or $plain.Contains("`n") -or $plain.Contains("`r")) { throw "Secret 不能为空或包含换行。" }
    [IO.File]::WriteAllText($secretPath, $plain + "`n", [Text.UTF8Encoding]::new($false))
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent().User
    $acl = New-Object Security.AccessControl.FileSecurity
    $acl.SetOwner($identity)
    $acl.SetAccessRuleProtection($true, $false)
    $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($identity, "FullControl", "Allow")))
    Set-Acl -LiteralPath $secretPath -AclObject $acl
} finally {
    if ($bstr -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
    $plain = $null
}
try {
    Invoke-Compose up -d --wait mysql
    $mountPath = (Resolve-Path $secretPath).Path
    Invoke-Compose run --rm --no-deps -v "${mountPath}:/run/secrets/feishu:ro" bot /usr/local/bin/secure-appctl --config /space/config/bot.yaml --name $appName --app-id $appId --secret-file /run/secrets/feishu --workspace-dir "/space/apps/$appName/workspace" --model $model --effort high
} finally {
    Remove-Item -LiteralPath $secretPath -Force -ErrorAction SilentlyContinue
}
Write-Host "Workspace 已加入。运行 .\start.ps1 即可启动全部服务。"
