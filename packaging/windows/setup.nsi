; clipshare Windows 安装包（NSIS）
;
; 设计取舍：
;   * 请求管理员权限 —— 因为要放行防火墙入站；文件装到 Program Files 而不是
;     %LOCALAPPDATA%，这样「程序」和「用户数据」界线清楚（数据仍在 %APPDATA%\clipshare）。
;   * 自启写在 HKLM Run：机器级安装就该机器级自启，换用户登录也能起来。
;   * 卸载时**不删** 用户配置与历史（%APPDATA%\clipshare），只在最后提示路径。
Unicode true
!include "MUI2.nsh"
!include "x64.nsh"

!ifndef VERSION
  !define VERSION "0.1.0"
!endif
!ifndef PORT
  !define PORT "47100"
!endif

!define APPNAME "clipshare"
!define PUBLISHER "clipshare"
!define REGUNINST "Software\Microsoft\Windows\CurrentVersion\Uninstall\clipshare"

Name "clipshare ${VERSION}"
OutFile "clipshare-${VERSION}-windows-setup.exe"
InstallDir "$PROGRAMFILES64\clipshare"
InstallDirRegKey HKLM "Software\clipshare" "InstallDir"
RequestExecutionLevel admin
SetCompressor /SOLID lzma
ShowInstDetails show
ShowUninstDetails show

!define MUI_ABORTWARNING
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\clipshare.exe"
!define MUI_FINISHPAGE_RUN_PARAMETERS "--port ${PORT}"
!define MUI_FINISHPAGE_RUN_TEXT "立即启动 clipshare（托盘常驻）"
!define MUI_FINISHPAGE_SHOWREADME "$INSTDIR\README.html"
!define MUI_FINISHPAGE_SHOWREADME_TEXT "打开使用说明"
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "English"

Section "clipshare" SecMain
  ; makensis 产出的是 32 位安装器，不显式切到 64 视图的话，
  ; HKLM 写入会被重定向到 Wow6432Node —— 那里的 Run 项 Windows 根本不执行，
  ; 自启会静默失效（实测踩到：装完 Run 项查不到）。
  SetRegView 64

  ; 升级场景：必须先把正在运行的实例停掉，否则
  ;   ① clipshare.exe 被占用，File 写不进去（升级失败）
  ;   ② 旧进程占着 47100，新进程绑不上端口直接退出（看起来「装完没反应」）
  ; 这两条都是实测升级时才会暴露的，全新安装碰不到。
  DetailPrint "停止正在运行的 clipshare ..."
  nsExec::ExecToLog 'taskkill /IM clipshare.exe /F'
  Sleep 1200

  SetOutPath "$INSTDIR"
  File "clipshare.exe"
  File "README.html"
  File "README.md"

  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr HKLM "Software\clipshare" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\clipshare" "Port" "${PORT}"

  ; 控制面板「程序和功能」里的一条
  WriteRegStr HKLM "${REGUNINST}" "DisplayName" "clipshare 局域网剪贴板共享"
  WriteRegStr HKLM "${REGUNINST}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${REGUNINST}" "Publisher" "${PUBLISHER}"
  WriteRegStr HKLM "${REGUNINST}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${REGUNINST}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKLM "${REGUNINST}" "NoModify" 1
  WriteRegDWORD HKLM "${REGUNINST}" "NoRepair" 1

  ; 开始菜单
  CreateDirectory "$SMPROGRAMS\clipshare"
  CreateShortCut "$SMPROGRAMS\clipshare\clipshare.lnk" "$INSTDIR\clipshare.exe" "--port ${PORT}" "$INSTDIR\clipshare.exe" 0
  CreateShortCut "$SMPROGRAMS\clipshare\使用说明.lnk" "$INSTDIR\README.html"
  CreateShortCut "$SMPROGRAMS\clipshare\卸载.lnk" "$INSTDIR\uninstall.exe"

  ; 登录自启（机器级）
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Run" "clipshare" '"$INSTDIR\clipshare.exe" --port ${PORT}'

  ; 放行防火墙入站（对等 P2P 两个方向都要能连）。
  ;
  ; 必须先把「按程序路径」生成的规则也删掉：Windows 在用户点了
  ; 「Windows 防火墙已阻止部分功能」弹窗的取消时，会自动生成
  ; name=clipshare.exe、app=<路径>、action=block 的规则，而**阻止规则优先级高于放行规则**。
  ; 实测后果：端口放行规则明明存在，对端仍然连不进来（HTTP 000），极难排查。
  DetailPrint "放行防火墙入站 TCP ${PORT} ..."
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name=all program="$INSTDIR\clipshare.exe"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="clipshare"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="clipshare.exe"'
  nsExec::ExecToLog 'netsh advfirewall firewall add rule name="clipshare" dir=in action=allow protocol=TCP localport=${PORT}'

  DetailPrint "启动 clipshare ..."
  Exec '"$INSTDIR\clipshare.exe" --port ${PORT}'
SectionEnd

Section "Uninstall"
  SetRegView 64
  ; 先停进程，否则 exe 删不掉
  nsExec::ExecToLog 'taskkill /IM clipshare.exe /F'
  Sleep 1200

  DeleteRegValue HKLM "Software\Microsoft\Windows\CurrentVersion\Run" "clipshare"
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name=all program="$INSTDIR\clipshare.exe"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="clipshare"'
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="clipshare.exe"'
  DeleteRegKey HKLM "${REGUNINST}"
  DeleteRegKey HKLM "Software\clipshare"

  Delete "$SMPROGRAMS\clipshare\clipshare.lnk"
  Delete "$SMPROGRAMS\clipshare\使用说明.lnk"
  Delete "$SMPROGRAMS\clipshare\卸载.lnk"
  RMDir "$SMPROGRAMS\clipshare"

  Delete "$INSTDIR\clipshare.exe"
  Delete "$INSTDIR\README.html"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  ; 用户数据刻意保留
  DetailPrint "已保留你的配置与历史：$APPDATA\clipshare（需要彻底清除请手动删除）"
SectionEnd
