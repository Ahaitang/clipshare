<# clipshare Windows 卸载脚本：撤销安装脚本做的每一步。#>
$ErrorActionPreference = 'Continue'
$appName    = 'clipshare'
$installDir = Join-Path $env:LOCALAPPDATA $appName

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }

Write-Step "停止进程"
Get-Process clipshare -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500

Write-Step "移除登录自启项"
Remove-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name $appName -ErrorAction SilentlyContinue

Write-Step "删除开始菜单快捷方式"
Remove-Item (Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\clipshare.lnk') -ErrorAction SilentlyContinue

Write-Step "移除防火墙规则（需要管理员，失败可忽略）"
try { Remove-NetFirewallRule -DisplayName 'clipshare' -ErrorAction Stop } catch { Write-Host "    跳过（需要管理员权限）" -ForegroundColor DarkGray }

Write-Step "删除程序目录 $installDir"
Remove-Item -Recurse -Force $installDir -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "已卸载。你的配置与历史（$env:APPDATA\clipshare）保留，需要彻底清除请手动删除该目录。" -ForegroundColor Yellow
