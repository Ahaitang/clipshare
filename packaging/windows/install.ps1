<#
clipshare Windows 安装脚本。

做四件事（都可逆，卸载脚本会把它们全部撤销）：
  1. 把 clipshare.exe 复制到 %LOCALAPPDATA%\clipshare（不需要管理员权限）
  2. 在 HKCU 的 Run 项登记登录自启（不需要管理员权限）
  3. 放行防火墙入站（需要管理员，脚本会自检；没权限时给出可复制的命令）
  4. 生成开始菜单快捷方式与文档

用法：
  powershell -ExecutionPolicy Bypass -File install.ps1 [-Port 47100] [-Seed "1.2.3.4:47100"]
#>
param(
    [int]$Port = 47100,
    [string]$Seed = ""
)

$ErrorActionPreference = 'Stop'
$appName    = 'clipshare'
$installDir = Join-Path $env:LOCALAPPDATA $appName
$exeSrc     = Join-Path $PSScriptRoot 'clipshare.exe'
$exeDst     = Join-Path $installDir 'clipshare.exe'

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "    $msg" -ForegroundColor Green }
function Write-Warn2($msg){ Write-Host "    $msg" -ForegroundColor Yellow }

if (-not (Test-Path $exeSrc)) { throw "找不到 clipshare.exe（应与本脚本在同一目录）：$exeSrc" }

Write-Step "停止正在运行的 clipshare"
Get-Process clipshare -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500
Write-Ok "已停止（若原本未运行则忽略）"

Write-Step "复制程序到 $installDir"
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
Copy-Item $exeSrc $exeDst -Force
foreach ($doc in @('README.html', 'README.md')) {
    $src = Join-Path $PSScriptRoot $doc
    if (Test-Path $src) { Copy-Item $src (Join-Path $installDir $doc) -Force }
}
Write-Ok (Get-Item $exeDst).FullName

Write-Step "登记登录自启（HKCU\Software\Microsoft\Windows\CurrentVersion\Run）"
$launchArgs = "--port $Port"
if ($Seed) { $launchArgs += " --seed `"$Seed`"" }
$runValue = "`"$exeDst`" $launchArgs"
Set-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name $appName -Value $runValue
Write-Ok $runValue

Write-Step "创建开始菜单快捷方式"
$startMenu = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs'
$shell = New-Object -ComObject WScript.Shell
$lnk = $shell.CreateShortcut((Join-Path $startMenu 'clipshare.lnk'))
$lnk.TargetPath = $exeDst
$lnk.Arguments  = $launchArgs
$lnk.Description = '局域网剪贴板共享'
$lnk.Save()
Write-Ok (Join-Path $startMenu 'clipshare.lnk')

Write-Step "放行防火墙入站（TCP $Port）"
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if ($isAdmin) {
    try {
        # 先清掉所有相关规则，特别是「按程序路径生成」的阻止规则：
        # 用户在 Windows 防火墙弹窗上点取消后，系统会自动建一条
        # app=<exe 路径>、action=block 的规则，而**阻止优先于放行**，
        # 于是端口规则看着正常、对端却连不进来（实测踩到过）。
        Get-NetFirewallApplicationFilter -Program $exeDst -ErrorAction SilentlyContinue |
            Get-NetFirewallRule -ErrorAction SilentlyContinue |
            Remove-NetFirewallRule -ErrorAction SilentlyContinue
        Remove-NetFirewallRule -DisplayName 'clipshare*' -ErrorAction SilentlyContinue
        New-NetFirewallRule -DisplayName 'clipshare' -Direction Inbound -Protocol TCP -LocalPort $Port -Action Allow | Out-Null
        Write-Ok "已放行 TCP $Port（同时清理了可能存在的阻止规则）"
    } catch {
        Write-Warn2 "放行失败：$($_.Exception.Message)"
    }
} else {
    Write-Warn2 "当前不是管理员，跳过防火墙。请用管理员 PowerShell 执行："
    Write-Warn2 "New-NetFirewallRule -DisplayName clipshare -Direction Inbound -Protocol TCP -LocalPort $Port -Action Allow"
}

Write-Step "启动 clipshare"
Start-Process -FilePath $exeDst -ArgumentList $launchArgs -WindowStyle Hidden
Start-Sleep -Seconds 2
$proc = Get-Process clipshare -ErrorAction SilentlyContinue
if ($proc) { Write-Ok "已在后台运行（PID $($proc.Id)），托盘出现图标表示成功" }
else { Write-Warn2 "启动后没看到进程，请手动运行 $exeDst 查看错误" }

Write-Host ""
Write-Host "安装完成。文档：$installDir\README.html" -ForegroundColor Cyan
Write-Host "卸载：右键用 PowerShell 运行 卸载.ps1，或直接删除 $installDir 并清掉 Run 项。" -ForegroundColor DarkGray
