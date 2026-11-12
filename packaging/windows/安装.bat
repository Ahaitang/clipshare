@echo off
chcp 65001 >nul
setlocal
rem Double-click to install clipshare (elevates for the firewall rule).
set "PS1=%~dp0install.ps1"
powershell -NoProfile -Command "Start-Process powershell -Verb RunAs -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-NoExit','-File','\"%PS1%\"'"
