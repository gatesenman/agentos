; AgentOS Windows installer (module M8).
; Build via scripts/build.sh, or manually:
;   makensis -WX -DVERSION=0.1.0 -DVERSION_NUM=0.1.0.0 -DSRCEXE=dist/agentos-windows-amd64.exe \
;            -DTRAYEXE=dist/agentos-tray-windows-amd64.exe -DOUTFILE=dist/AgentOS-Setup-0.1.0.exe packaging/nsis/agentos.nsi
;
; Silent install:   AgentOS-Setup.exe /S [/MODE=server|node] [/SERVER=URL /TOKEN=T /NAME=N]
;                    [/SERVICE] [/NOSERVICE] [/NODESKTOP] [/NOTRAY] [/NODIST] [/D=C:\path]
;   /MODE=console (default) management software (console + local Agent OS node); "server" = alias
;   /MODE=node    only an Agent OS node joining the console at /SERVER with join token /TOKEN
;   /SERVICE      register + start the "AgentOS" Windows service (auto start, restart on failure)
;   /PORT=7800    console port (server mode)
;   /NOTRAY       do not start the tray launcher at logon
;   /NOWEBVIEW2   do not download the WebView2 runtime (desktop app builds only)
;   /NODIST       do not ship the node binaries for other machines (<install>\bin\dist\)
; Settings are written to %ProgramData%\AgentOS\agentos.conf (UTF-16, key=value: mode, listen,
; server, token, name), read by "agentos service run" and by agentos-tray.
; <install>\bin\dist\ holds agentos-{linux-amd64,darwin-arm64,windows-amd64.exe} + join.sh/join.ps1,
; served by the console at /download/<file> so other machines can join (module M9).
; Upgrade in place: running tray/server processes and the service are stopped before files are
; replaced; an existing service is kept (and restarted if it was running).
; Silent uninstall: "%ProgramFiles%\AgentOS\uninstall.exe" /S  (keeps %ProgramData%\AgentOS unless /PURGE)
Unicode true
ManifestDPIAware true
!include "MUI2.nsh"
!include "x64.nsh"
!include "FileFunc.nsh"
!include "TextFunc.nsh"
!include "LogicLib.nsh"
!include "Sections.nsh"
!include "nsDialogs.nsh"
!include "WinMessages.nsh"

!ifndef VERSION
  !define VERSION "0.1.4"
!endif
!ifndef VERSION_NUM
  !define VERSION_NUM "0.1.4.0"
!endif
!ifndef SRCEXE
  !define SRCEXE "${__FILEDIR__}\..\..\dist\agentos-windows-amd64.exe"
!endif
!ifndef TRAYEXE
  !define TRAYEXE "${__FILEDIR__}\..\..\dist\agentos-tray-windows-amd64.exe"
!endif
!ifndef ASSETDIR
  !define ASSETDIR "${__FILEDIR__}\..\windows"
!endif
!ifndef DISTDIR
  !define DISTDIR "${__FILEDIR__}\..\..\dist"
!endif
!ifndef SCRIPTDIR
  !define SCRIPTDIR "${__FILEDIR__}\..\..\scripts"
!endif
; Optional desktop app (cmd/agentos-desktop, WebView2 window). When DESKTOPEXE is defined
; AgentOS.exe is installed and the shortcuts / "launch after install" open it (server mode);
; node-only installs keep using the tray launcher.
!ifndef OUTFILE
  !define OUTFILE "AgentOS-Setup-${VERSION}.exe"
!endif
!define APPNAME "AgentOS"
!define REGKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\AgentOS"
!define RUNKEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define PORT "7800"

Name "${APPNAME} ${VERSION}"
Caption "${APPNAME} ${VERSION} 安装程序"
BrandingText "AgentOS ${VERSION}"
OutFile "${OUTFILE}"
InstallDir "$PROGRAMFILES64\AgentOS"
InstallDirRegKey HKLM "Software\AgentOS" "InstallDir"
RequestExecutionLevel admin
SetCompressor /SOLID lzma
ShowInstDetails show
ShowUninstDetails show
VIProductVersion "${VERSION_NUM}"
VIAddVersionKey /LANG=2052 "ProductName" "${APPNAME}"
VIAddVersionKey /LANG=2052 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=2052 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=2052 "FileDescription" "AgentOS 安装程序 - 本地 AI Agent 管理工具"
VIAddVersionKey /LANG=2052 "CompanyName" "AgentOS"
VIAddVersionKey /LANG=2052 "LegalCopyright" "Copyright (C) AgentOS contributors"

Var SvcExisted      ; "1" if the AgentOS service was registered before this install
Var OldVersion
Var Mode            ; "console" | "node"
Var Server
Var Token
Var NodeName
Var LaunchArgs
Var Port            ; console port (server mode), default ${PORT}
Var hRadioServer
Var hRadioNode
Var hServer
Var hToken
Var hName

; ---------- UI ----------
!define MUI_ICON "${ASSETDIR}\agentos.ico"
!define MUI_UNICON "${ASSETDIR}\agentos.ico"
!define MUI_WELCOMEFINISHPAGE_BITMAP "${ASSETDIR}\wizard.bmp"
!define MUI_UNWELCOMEFINISHPAGE_BITMAP "${ASSETDIR}\wizard.bmp"
!define MUI_HEADERIMAGE
!define MUI_HEADERIMAGE_RIGHT
!define MUI_HEADERIMAGE_BITMAP "${ASSETDIR}\header.bmp"
!define MUI_HEADERIMAGE_UNBITMAP "${ASSETDIR}\header.bmp"
!define MUI_ABORTWARNING
!define MUI_COMPONENTSPAGE_SMALLDESC

!define MUI_WELCOMEPAGE_TITLE "欢迎安装 AgentOS ${VERSION}"
!define MUI_WELCOMEPAGE_TEXT "AgentOS 是本地 AI Agent 的可视化管理工具：托管、启动、监控 Agent 进程，实时查看日志与资源占用，并可通过浏览器远程访问桌面。$\r$\n$\r$\n如已安装旧版本，安装程序会停止正在运行的 AgentOS 并原地升级，数据与配置将保留。$\r$\n$\r$\n点击「下一步」继续。"
!insertmacro MUI_PAGE_WELCOME
Page custom ModePageCreate ModePageLeave
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_TITLE "AgentOS 安装完成"
!define MUI_FINISHPAGE_TEXT "AgentOS 已安装到您的计算机。$\r$\n$\r$\n管理软件模式：控制台地址 http://localhost:<端口>/ (默认 ${PORT})，首次登录的管理员密码保存在 %ProgramData%\AgentOS\server\initial-admin-password.txt (也可在托盘菜单中查看)。$\r$\n$\r$\n节点模式：本机已加入所填写的控制台，请在控制台的节点列表中查看。"
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchApp
!define MUI_FINISHPAGE_RUN_TEXT "启动 AgentOS 桌面程序"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

; ---------- helpers ----------
!macro StopEverything
  ; stop the service (if any) and every AgentOS process so files can be replaced
  ${If} ${FileExists} "$INSTDIR\bin\agentos-server.exe"
    nsExec::ExecToLog '"$INSTDIR\bin\agentos-server.exe" service stop'
    Pop $0
  ${ElseIf} ${FileExists} "$INSTDIR\bin\agentos.exe"
    nsExec::ExecToLog '"$INSTDIR\bin\agentos.exe" service stop'
    Pop $0
  ${EndIf}
  ${If} ${FileExists} "$INSTDIR\agentos-tray.exe" ; old layout
    nsExec::ExecToLog '"$INSTDIR\agentos.exe" service stop'
    Pop $0
  ${EndIf}
  nsExec::Exec 'sc.exe stop AgentOS'
  Pop $0
  nsExec::Exec 'taskkill.exe /F /T /IM agentos-tray.exe'
  Pop $0
  nsExec::Exec 'taskkill.exe /F /T /IM AgentOS.exe'
  Pop $0
  nsExec::Exec 'taskkill.exe /F /T /IM agentos-server.exe'
  Pop $0
  nsExec::Exec 'taskkill.exe /F /T /IM agentos.exe'
  Pop $0
  Sleep 1500
!macroend

; delete a file that may still be held by an exiting process (retry, then on reboot)
!macro DeleteRetry FILE
  StrCpy $8 0
  ${DoWhile} ${FileExists} "${FILE}"
    ClearErrors
    Delete "${FILE}"
    ${IfNot} ${FileExists} "${FILE}"
      ${Break}
    ${EndIf}
    IntOp $8 $8 + 1
    ${If} $8 >= 10
      Delete /REBOOTOK "${FILE}"
      ${Break}
    ${EndIf}
    Sleep 1000
  ${Loop}
!macroend

; ---------- sections ----------
Section "AgentOS 主程序 (必需)" SecCore
  SectionIn RO
  SetShellVarContext all
  ${If} $OldVersion != ""
    DetailPrint "升级 / upgrade: $OldVersion -> ${VERSION}"
  ${EndIf}
  DetailPrint "正在停止正在运行的 AgentOS..."
  !insertmacro StopEverything

  ; layout: AgentOS.exe (desktop GUI) in the root; CLI/server lives in bin\agentos-server.exe
  ; (never ship bin\agentos.exe — users double-click it and get a black console)
  ; pre-0.2 layout had agentos.exe / agentos-tray.exe / dist\ in the root: remove them
  ${If} ${FileExists} "$INSTDIR\agentos-tray.exe"
    !insertmacro DeleteRetry "$INSTDIR\agentos-tray.exe"
    !insertmacro DeleteRetry "$INSTDIR\agentos.exe"
    RMDir /r "$INSTDIR\dist"
  ${EndIf}
  SetOverwrite on
  SetOutPath "$INSTDIR\bin"
  File "/oname=agentos-server.exe" "${SRCEXE}"
  ; remove pre-0.1.11 console name that users double-clicked by mistake
  !insertmacro DeleteRetry "$INSTDIR\bin\agentos.exe"
  File "/oname=agentos-tray.exe" "${TRAYEXE}"
  File "/oname=hyperv-add-group.ps1" "${ASSETDIR}\helpers\hyperv-add-group.ps1"
  File "/oname=hyperv-repair-group.ps1" "${ASSETDIR}\helpers\hyperv-repair-group.ps1"
  SetOutPath "$INSTDIR"
!ifdef DESKTOPEXE
  File "/oname=AgentOS.exe" "${DESKTOPEXE}"
!endif
  File "/oname=agentos.ico" "${ASSETDIR}\agentos.ico"
  ${If} $Mode == "node"
    WriteINIStr "$INSTDIR\AgentOS 控制台.url" "InternetShortcut" "URL" "$Server"
  ${Else}
    WriteINIStr "$INSTDIR\AgentOS 控制台.url" "InternetShortcut" "URL" "http://localhost:$Port/"
  ${EndIf}
  WriteINIStr "$INSTDIR\AgentOS 控制台.url" "InternetShortcut" "IconFile" "$INSTDIR\agentos.ico"
  WriteINIStr "$INSTDIR\AgentOS 控制台.url" "InternetShortcut" "IconIndex" "0"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  ; data dir shared by service (LocalSystem) and the per-user tray launcher
  CreateDirectory "$APPDATA\AgentOS"
  nsExec::Exec 'icacls.exe "$APPDATA\AgentOS" /grant *S-1-5-32-545:(OI)(CI)M /T /C /Q'
  Pop $0
  Call WriteConfig

  WriteRegStr HKLM "Software\AgentOS" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\AgentOS" "Version" "${VERSION}"
  WriteRegStr HKLM "Software\AgentOS" "Mode" "$Mode"
  WriteRegStr HKLM "Software\AgentOS" "Server" "$Server"
  WriteRegStr HKLM "Software\AgentOS" "NodeName" "$NodeName"
  WriteRegStr HKLM "${REGKEY}" "DisplayName" "${APPNAME}"
  WriteRegStr HKLM "${REGKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${REGKEY}" "Publisher" "AgentOS"
  WriteRegStr HKLM "${REGKEY}" "DisplayIcon" "$INSTDIR\agentos.ico"
  WriteRegStr HKLM "${REGKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${REGKEY}" "URLInfoAbout" "http://localhost:$Port/"
  WriteRegStr HKLM "${REGKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKLM "${REGKEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKLM "${REGKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${REGKEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKLM "${REGKEY}" "EstimatedSize" "$0"

  ; allow LAN access to the console / node connections
  nsExec::Exec 'netsh advfirewall firewall delete rule name="AgentOS"'
  Pop $0
  nsExec::Exec 'netsh advfirewall firewall add rule name="AgentOS" dir=in action=allow program="$INSTDIR\bin\agentos-server.exe" enable=yes'
  ; 0.1.9: add installing interactive user + local Administrator to Hyper-V Administrators
  ; (S-1-5-32-578). GetCurrent during elevated install is often wrong/SYSTEM.
  ; Log to INSTDIR hyperv-group.log. Takes effect after next sign-in.
  ExpandEnvStrings $R8 "%USERDOMAIN%\%USERNAME%"
  FileOpen $R9 "$INSTDIR\hyperv-group.log" w
  FileWrite $R9 "AgentOS Hyper-V Administrators membership"
  FileWrite $R9 "$\r$\n"
  FileWrite $R9 "installer_user="
  FileWrite $R9 "$R8"
  FileWrite $R9 "$\r$\n"
  FileClose $R9
  nsExec::ExecToLog 'powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\bin\hyperv-add-group.ps1" -LogPath "$INSTDIR\hyperv-group.log" -InstallerUser "$R8"'
  Pop $0
  DetailPrint "Hyper-V Administrators membership exit=$0 (see $INSTDIR\hyperv-group.log)"
SectionEnd

Section "开始菜单快捷方式" SecStart
  SetShellVarContext all
  RMDir /r "$SMPROGRAMS\AgentOS"
  CreateDirectory "$SMPROGRAMS\AgentOS"
  Call LauncherPath
  Pop $1
  CreateShortcut "$SMPROGRAMS\AgentOS\AgentOS.lnk" "$1" "$LaunchArgs" "$INSTDIR\agentos.ico" 0 SW_SHOWNORMAL "" "启动 AgentOS"
  CreateShortcut "$SMPROGRAMS\AgentOS\AgentOS 托盘.lnk" "$INSTDIR\bin\agentos-tray.exe" "" "$INSTDIR\agentos.ico" 0 SW_SHOWNORMAL "" "AgentOS 托盘启动器 (后台运行并打开浏览器控制台)"
  CreateShortcut "$SMPROGRAMS\AgentOS\AgentOS 控制台 (浏览器).lnk" "$INSTDIR\AgentOS 控制台.url" "" "$INSTDIR\agentos.ico" 0
  ; 0.1.11+: 不再提供「命令行窗口」开始菜单项，避免用户误开黑色控制台；高级用户可手动运行 bin\agentos-server.exe
  CreateShortcut "$SMPROGRAMS\AgentOS\数据目录.lnk" "$APPDATA\AgentOS"
  CreateShortcut "$SMPROGRAMS\AgentOS\卸载 AgentOS.lnk" "$INSTDIR\uninstall.exe"
SectionEnd

Section "桌面快捷方式" SecDesktop
  SetShellVarContext all
  Call LauncherPath
  Pop $1
  CreateShortcut "$DESKTOP\AgentOS.lnk" "$1" "$LaunchArgs" "$INSTDIR\agentos.ico" 0 SW_SHOWNORMAL "" "启动 AgentOS"
SectionEnd

Section "节点安装包 (供其他电脑下载加入)" SecDist
  ; served by the console at /download/<file> (see scripts/join.sh, scripts/join.ps1)
  SetOutPath "$INSTDIR\bin\dist"
  File "/oname=agentos-windows-amd64.exe" "${SRCEXE}"
  File "/oname=agentos-linux-amd64" "${DISTDIR}\agentos-linux-amd64"
  File "/oname=agentos-linux-arm64" "${DISTDIR}\agentos-linux-arm64"
  File "/oname=agentos-darwin-arm64" "${DISTDIR}\agentos-darwin-arm64"
  File "/oname=agentos-darwin-amd64" "${DISTDIR}\agentos-darwin-amd64"
  File "/oname=join.ps1" "${SCRIPTDIR}\join.ps1"
  File "/oname=join.sh" "${SCRIPTDIR}\join.sh"
  SetOutPath "$INSTDIR"
SectionEnd

!ifdef DESKTOPEXE
Section "WebView2 运行时 (桌面窗口所需, 缺失时自动下载)" SecWebView2
  ${If} $Mode == "node"
    Return
  ${EndIf}
  Call HasWebView2
  Pop $0
  ${If} $0 == "1"
    DetailPrint "WebView2 运行时已安装"
    Return
  ${EndIf}
  DetailPrint "下载 Microsoft Edge WebView2 运行时 (Evergreen Bootstrapper)..."
  nsExec::ExecToLog 'powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12; Invoke-WebRequest -UseBasicParsing -Uri https://go.microsoft.com/fwlink/p/?LinkId=2124703 -OutFile $\"$TEMP\MicrosoftEdgeWebview2Setup.exe$\""'
  Pop $0
  ${If} ${FileExists} "$TEMP\MicrosoftEdgeWebview2Setup.exe"
    ExecWait '"$TEMP\MicrosoftEdgeWebview2Setup.exe" /silent /install' $0
    DetailPrint "WebView2 安装程序退出码 $0"
    Delete "$TEMP\MicrosoftEdgeWebview2Setup.exe"
  ${Else}
    DetailPrint "WebView2 下载失败; AgentOS 桌面窗口不可用时将改用浏览器打开控制台"
  ${EndIf}
SectionEnd
!endif

!ifdef DESKTOPEXE
Section /o "登录时启动托盘程序" SecTray
!else
Section "登录时启动托盘程序" SecTray
!endif
  WriteRegStr HKLM "${RUNKEY}" "AgentOSTray" '"$INSTDIR\bin\agentos-tray.exe" --open=false'
SectionEnd

Section /o "注册为 Windows 服务 (开机自启, 故障自动重启)" SecService
  SetShellVarContext all
  DetailPrint "注册 Windows 服务 AgentOS..."
  ; settings come from $APPDATA\AgentOS\agentos.conf written by WriteConfig
  nsExec::ExecToLog '"$INSTDIR\bin\agentos-server.exe" service install --data "$APPDATA\AgentOS"'
  Pop $0
  ${If} $0 != 0
    DetailPrint "服务注册失败 (exit $0)"
    MessageBox MB_OK|MB_ICONEXCLAMATION "注册 Windows 服务失败 (代码 $0)。$\r$\n您仍可通过桌面快捷方式以普通程序方式运行 AgentOS。" /SD IDOK
  ${Else}
    ; retry: the SCM can be busy right after registration
    StrCpy $1 0
    ${Do}
      nsExec::ExecToLog '"$INSTDIR\bin\agentos-server.exe" service start'
      Pop $0
      IntOp $1 $1 + 1
      ${If} $0 == 0
      ${OrIf} $1 >= 3
        ${Break}
      ${EndIf}
      Sleep 2000
    ${Loop}
    ${If} $0 != 0
      DetailPrint "服务启动失败 (exit $0), 详见 %ProgramData%\AgentOS\logs\service.log"
    ${EndIf}
  ${EndIf}
SectionEnd

Section "-post"
  ; service existed but the user unticked the option -> remove it
  ${IfNot} ${SectionIsSelected} ${SecService}
  ${AndIf} $SvcExisted == "1"
    DetailPrint "删除 Windows 服务 AgentOS"
    nsExec::ExecToLog '"$INSTDIR\bin\agentos-server.exe" service uninstall'
    Pop $0
  ${EndIf}
  ${IfNot} ${SectionIsSelected} ${SecTray}
    DeleteRegValue HKLM "${RUNKEY}" "AgentOSTray"
  ${EndIf}
  ${IfNot} ${SectionIsSelected} ${SecDist}
    RMDir /r "$INSTDIR\bin\dist"
  ${EndIf}
SectionEnd

; pushes the program the shortcuts start: the desktop app (server mode, if bundled) or the tray
; $LaunchArgs receives its arguments (non-default console port)
Function LauncherPath
  StrCpy $LaunchArgs ""
!ifdef DESKTOPEXE
  ${If} $Mode != "node"
    StrCpy $LaunchArgs '--agentos "$INSTDIR\bin\agentos-server.exe"'
    ${If} $Port != "${PORT}"
      StrCpy $LaunchArgs '$LaunchArgs --port $Port'
    ${EndIf}
    Push "$INSTDIR\AgentOS.exe"
    Return
  ${EndIf}
!endif
  ${If} $Mode != "node"
  ${AndIf} $Port != "${PORT}"
    StrCpy $LaunchArgs "--listen :$Port"
  ${EndIf}
  Push "$INSTDIR\bin\agentos-tray.exe"
FunctionEnd

Function LaunchApp
  Call LauncherPath
  Pop $0
  Exec '"$0" $LaunchArgs'
FunctionEnd

!ifdef DESKTOPEXE
; pushes "1" when the Evergreen WebView2 runtime is installed (machine or user)
Function HasWebView2
  ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ${If} $0 == ""
    ReadRegStr $0 HKLM "SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ${EndIf}
  ${If} $0 == ""
    ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ${EndIf}
  ${If} $0 != ""
  ${AndIf} $0 != "0.0.0.0"
    Push "1"
  ${Else}
    Push "0"
  ${EndIf}
FunctionEnd
!endif

; ---------- install type page ----------
Function ModePageCreate
  !insertmacro MUI_HEADER_TEXT "安装类型" "选择本机在 AgentOS 中的角色"
  nsDialogs::Create 1018
  Pop $0
  ${If} $0 == error
    Abort
  ${EndIf}
  ${NSD_CreateRadioButton} 0 0 100% 12u "管理软件（含本机 Agent OS）(推荐)"
  Pop $hRadioServer
  ${NSD_AddStyle} $hRadioServer ${WS_GROUP}
  ${NSD_CreateLabel} 12u 13u -12u 18u "安装 AgentOS 桌面管理软件, 管理本机及远程的运行环境与智能体; 本机同时作为一个 Agent OS 节点。"
  Pop $0
  ${NSD_CreateRadioButton} 0 34u 100% 12u "仅 Agent OS 节点（加入已有的管理端）"
  Pop $hRadioNode
  ${NSD_CreateLabel} 12u 47u -12u 10u "本机只运行 Agent OS 节点 (Windows 服务), 由远程管理端统一管理。"
  Pop $0
  ${NSD_CreateLabel} 12u 62u 70u 12u "管理端地址:"
  Pop $0
  ${NSD_CreateText} 85u 60u -1u 12u "$Server"
  Pop $hServer
  SendMessage $hServer 0x1501 1 "STR:http://192.168.1.10:7800"
  ${NSD_CreateLabel} 12u 79u 70u 12u "加入令牌:"
  Pop $0
  ${NSD_CreateText} 85u 77u -1u 12u "$Token"
  Pop $hToken
  SendMessage $hToken 0x1501 1 "STR:控制台「设置」页中的节点令牌"
  ${NSD_CreateLabel} 12u 96u 70u 12u "名称:"
  Pop $0
  ${NSD_CreateText} 85u 94u -1u 12u "$NodeName"
  Pop $hName
  ${NSD_CreateLabel} 12u 113u -12u 20u "加入令牌可在管理端「设置」页查看; 其他电脑也可运行 join.ps1 / join.sh 一键加入。"
  Pop $0
  ${NSD_OnClick} $hRadioServer ModePageToggle
  ${NSD_OnClick} $hRadioNode ModePageToggle
  ${If} $Mode == "node"
    ${NSD_Check} $hRadioNode
  ${Else}
    ${NSD_Check} $hRadioServer
  ${EndIf}
  Call ModePageToggle
  nsDialogs::Show
FunctionEnd

Function ModePageToggle
  Pop $0 ; clicked control (ignored; also called directly)
  ${NSD_GetState} $hRadioNode $1
  ${If} $1 == ${BST_CHECKED}
    StrCpy $2 1
  ${Else}
    StrCpy $2 0
  ${EndIf}
  EnableWindow $hServer $2
  EnableWindow $hToken $2
  EnableWindow $hName $2
FunctionEnd

Function ModePageLeave
  ${NSD_GetState} $hRadioNode $1
  ${NSD_GetText} $hServer $Server
  ${NSD_GetText} $hToken $Token
  ${NSD_GetText} $hName $NodeName
  ${If} $1 == ${BST_CHECKED}
    StrCpy $Mode "node"
    Call ValidateNode
    Pop $0
    ${If} $0 != ""
      MessageBox MB_OK|MB_ICONEXCLAMATION "$0"
      Abort
    ${EndIf}
  ${Else}
    StrCpy $Mode "console"
  ${EndIf}
  Call ApplyModeDefaults
FunctionEnd

; pushes "" when node settings are valid, otherwise an error message
Function ValidateNode
  ${If} $Server == ""
    Push "请填写管理端地址, 例如 http://192.168.1.10:7800"
    Return
  ${EndIf}
  StrCpy $0 $Server 7
  StrCpy $1 $Server 8
  ${If} $0 != "http://"
  ${AndIf} $1 != "https://"
    Push "管理端地址必须以 http:// 或 https:// 开头"
    Return
  ${EndIf}
  ${If} $Token == ""
    Push "请填写加入令牌 (管理端「设置」页中查看)"
    Return
  ${EndIf}
  Push ""
FunctionEnd

; node-only installs do not need the binaries for other machines
Function ApplyModeDefaults
  ${If} $Mode == "node"
    !insertmacro SelectSection ${SecService}
    !insertmacro UnselectSection ${SecDist}
!ifdef DESKTOPEXE
    !insertmacro UnselectSection ${SecWebView2}
!endif
  ${Else}
    !insertmacro SelectSection ${SecDist}
!ifdef DESKTOPEXE
    !insertmacro SelectSection ${SecWebView2}
!endif
  ${EndIf}
FunctionEnd

Function WriteConfig
  ClearErrors
  FileOpen $9 "$APPDATA\AgentOS\agentos.conf" w
  ${If} ${Errors}
    DetailPrint "无法写入配置文件 agentos.conf"
    Return
  ${EndIf}
  FileWriteUTF16LE /BOM $9 "# AgentOS configuration (written by the installer)$\r$\n[agentos]$\r$\n"
  FileWriteUTF16LE $9 "mode=$Mode$\r$\n"
  ${If} $Mode == "node"
    FileWriteUTF16LE $9 "server=$Server$\r$\ntoken=$Token$\r$\n"
    ${If} $NodeName != ""
      FileWriteUTF16LE $9 "name=$NodeName$\r$\n"
    ${EndIf}
  ${Else}
    FileWriteUTF16LE $9 "listen=:$Port$\r$\nwith_node=true$\r$\n"
  ${EndIf}
  FileClose $9
  DetailPrint "配置已写入 / config: $APPDATA\AgentOS\agentos.conf (mode=$Mode)"
FunctionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecCore} "AgentOS 主程序 (控制台 + 本地节点) 与托盘启动器。"
  !insertmacro MUI_DESCRIPTION_TEXT ${SecStart} "在开始菜单创建 AgentOS 快捷方式。"
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} "在桌面创建 AgentOS 快捷方式 (启动服务器并打开浏览器控制台)。"
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDist} "附带 Linux / macOS / Windows 节点程序与 join 脚本, 由控制台在 /download/ 提供下载, 方便其他电脑一键加入。"
!ifdef DESKTOPEXE
  !insertmacro MUI_DESCRIPTION_TEXT ${SecWebView2} "AgentOS 桌面窗口基于 Microsoft Edge WebView2 (Windows 11 已内置)。如未安装, 将从微软官网下载并静默安装。"
!endif
  !insertmacro MUI_DESCRIPTION_TEXT ${SecTray} "登录 Windows 时在后台启动 AgentOS 托盘程序 (并启动服务器)。"
  !insertmacro MUI_DESCRIPTION_TEXT ${SecService} "以 Windows 服务方式运行 AgentOS：开机自动启动，无需登录，进程异常退出时自动重启。注意：服务模式下内置屏幕流无法捕获用户桌面。"
!insertmacro MUI_FUNCTION_DESCRIPTION_END

; GetParamValue: value of option <key> (e.g. "/SERVER=") in $R0 (the command
; line from GetParameters). The value runs to the next " /" or the end, or is
; the quoted string when it starts with ". Sets the error flag when absent.
; Usage: Push "/SERVER="  Call GetParamValue  Pop $R1
Function GetParamValue
  Exch $1          ; key
  Push $2
  Push $3
  Push $4
  Push $5
  Push $6
  ClearErrors
  StrLen $2 $1     ; key length
  StrLen $3 $R0    ; params length
  StrCpy $4 0      ; position
  StrCpy $6 ""
  ${Do}
    IntOp $5 $4 + $2
    ${If} $5 > $3
      SetErrors
      ${Break}
    ${EndIf}
    StrCpy $5 $R0 $2 $4
    ${If} $5 == $1            ; case-insensitive
      ${If} $4 == 0
        ${Break}
      ${EndIf}
      IntOp $5 $4 - 1
      StrCpy $5 $R0 1 $5
      ${If} $5 == " "
      ${OrIf} $5 == '"'
        ${Break}
      ${EndIf}
    ${EndIf}
    IntOp $4 $4 + 1
  ${Loop}
  ${IfNot} ${Errors}
    IntOp $4 $4 + $2          ; value start
    StrCpy $5 $R0 1 $4
    ${If} $5 == '"'
      IntOp $4 $4 + 1
      ${Do}
        StrCpy $5 $R0 1 $4
        ${If} $5 == '"'
        ${OrIf} $5 == ""
          ${Break}
        ${EndIf}
        StrCpy $6 "$6$5"
        IntOp $4 $4 + 1
      ${Loop}
    ${Else}
      ${Do}
        StrCpy $5 $R0 2 $4
        ${If} $5 == " /"
          ${Break}
        ${EndIf}
        StrCpy $5 $R0 1 $4
        ${If} $5 == ""
          ${Break}
        ${EndIf}
        StrCpy $6 "$6$5"
        IntOp $4 $4 + 1
      ${Loop}
      ; trim trailing spaces
      ${Do}
        StrCpy $5 $6 1 -1
        ${If} $5 != " "
          ${Break}
        ${EndIf}
        StrCpy $6 $6 -1
      ${Loop}
    ${EndIf}
  ${EndIf}
  StrCpy $1 $6
  Pop $6
  Pop $5
  Pop $4
  Pop $3
  Pop $2
  Exch $1
FunctionEnd

Function .onInit
  SetShellVarContext all
  ${IfNot} ${RunningX64}
    MessageBox MB_OK|MB_ICONSTOP "AgentOS 需要 64 位 Windows。" /SD IDOK
    Abort
  ${EndIf}
  ; only one installer at a time
  System::Call 'kernel32::CreateMutex(p 0, i 0, t "AgentOS.Setup") p .r1 ?e'
  Pop $0
  ${If} $0 == 183
    MessageBox MB_OK|MB_ICONEXCLAMATION "AgentOS 安装程序已在运行。" /SD IDOK
    Abort
  ${EndIf}
  ReadRegStr $OldVersion HKLM "Software\AgentOS" "Version"
  ; previous install type (upgrade keeps it)
  ReadRegStr $Mode HKLM "Software\AgentOS" "Mode"
  ReadRegStr $Server HKLM "Software\AgentOS" "Server"
  ReadRegStr $NodeName HKLM "Software\AgentOS" "NodeName"
  ${If} $Mode != "node"
    StrCpy $Mode "console"
  ${EndIf}

  ; keep the service option in sync with the current state
  StrCpy $SvcExisted "0"
  nsExec::Exec 'sc.exe query AgentOS'
  Pop $0
  ${If} $0 == 0
    StrCpy $SvcExisted "1"
    !insertmacro SelectSection ${SecService}
  ${EndIf}

  ${GetParameters} $R0
  ClearErrors
  ${GetOptions} $R0 "/SERVICE" $R1
  ${IfNot} ${Errors}
    !insertmacro SelectSection ${SecService}
  ${EndIf}
  ClearErrors
  ${GetOptions} $R0 "/NOSERVICE" $R1
  ${IfNot} ${Errors}
    !insertmacro UnselectSection ${SecService}
  ${EndIf}
  ClearErrors
  ${GetOptions} $R0 "/NODESKTOP" $R1
  ${IfNot} ${Errors}
    !insertmacro UnselectSection ${SecDesktop}
  ${EndIf}
  ClearErrors
  ${GetOptions} $R0 "/NOTRAY" $R1
  ${IfNot} ${Errors}
    !insertmacro UnselectSection ${SecTray}
  ${EndIf}
  ClearErrors
  ${GetOptions} $R0 "/MODE=" $R1
  ${IfNot} ${Errors}
    ${If} $R1 == "node"
      StrCpy $Mode "node"
    ${Else}
      StrCpy $Mode "console"
    ${EndIf}
  ${EndIf}
  ; not ${GetOptions}: it splits values at "/", which breaks URLs (http://...)
  Push "/SERVER="
  Call GetParamValue
  Pop $R1
  ${IfNot} ${Errors}
    StrCpy $Server $R1
  ${EndIf}
  ; not ${GetOptions}: it splits values at "/", which breaks URLs (http://...)
  Push "/TOKEN="
  Call GetParamValue
  Pop $R1
  ${IfNot} ${Errors}
    StrCpy $Token $R1
  ${EndIf}
  ; not ${GetOptions}: it splits values at "/", which breaks URLs (http://...)
  Push "/NAME="
  Call GetParamValue
  Pop $R1
  ${IfNot} ${Errors}
    StrCpy $NodeName $R1
  ${EndIf}
  ${If} $NodeName == ""
    ReadEnvStr $NodeName COMPUTERNAME
  ${EndIf}
  ; upgrade: keep token / port from the old config unless given on the command line
  StrCpy $Port ""
  Call ReadOldConf
  ClearErrors
  ${GetOptions} $R0 "/PORT=" $R1
  ${IfNot} ${Errors}
    StrCpy $Port $R1
  ${EndIf}
  ${If} $Port == ""
    StrCpy $Port "${PORT}"
  ${EndIf}
  Call ApplyModeDefaults
  ClearErrors
  ${GetOptions} $R0 "/NODIST" $R1
  ${IfNot} ${Errors}
    !insertmacro UnselectSection ${SecDist}
  ${EndIf}
!ifdef DESKTOPEXE
  ClearErrors
  ${GetOptions} $R0 "/NOWEBVIEW2" $R1
  ${IfNot} ${Errors}
    !insertmacro UnselectSection ${SecWebView2}
  ${EndIf}
!endif
  ${If} ${Silent}
  ${AndIf} $Mode == "node"
    Call ValidateNode
    Pop $0
    ${If} $0 != ""
      MessageBox MB_OK|MB_ICONSTOP "$0" /SD IDOK
      SetErrorLevel 2
      Abort
    ${EndIf}
  ${EndIf}
FunctionEnd

; reads token=... (if $Token is empty) and the port of listen=... from the existing UTF-16LE agentos.conf
Function ReadOldConf
  ClearErrors
  FileOpen $9 "$APPDATA\AgentOS\agentos.conf" r
  ${If} ${Errors}
    Return
  ${EndIf}
  FileReadWord $9 $0 ; BOM
  ${DoUntil} ${Errors}
    FileReadUTF16LE $9 $1
    ${If} ${Errors}
      ${Break}
    ${EndIf}
    ${TrimNewLines} $1 $1
    StrCpy $2 $1 6
    ${If} $2 == "token="
    ${AndIf} $Token == ""
      StrCpy $Token $1 "" 6
    ${EndIf}
    StrCpy $2 $1 7
    ${If} $2 == "listen="
      ; keep what follows the last ':'
      StrLen $3 $1
      ${DoWhile} $3 > 7
        IntOp $3 $3 - 1
        StrCpy $4 $1 1 $3
        ${If} $4 == ":"
          IntOp $3 $3 + 1
          StrCpy $Port $1 "" $3
          ${Break}
        ${EndIf}
      ${Loop}
    ${EndIf}
  ${Loop}
  FileClose $9
FunctionEnd

; ---------- uninstaller ----------
Function un.onInit
  SetShellVarContext all
FunctionEnd

Section "Uninstall"
  SetShellVarContext all
  nsExec::ExecToLog '"$INSTDIR\bin\agentos-server.exe" service uninstall'
  Pop $0
  !insertmacro StopEverything
  nsExec::Exec 'netsh advfirewall firewall delete rule name="AgentOS"'
  Pop $0
  !insertmacro DeleteRetry "$INSTDIR\bin\agentos-server.exe"
  !insertmacro DeleteRetry "$INSTDIR\bin\agentos.exe"
  !insertmacro DeleteRetry "$INSTDIR\bin\agentos-tray.exe"
  !insertmacro DeleteRetry "$INSTDIR\AgentOS.exe"
  Delete "$INSTDIR\agentos.ico"
  Delete "$INSTDIR\AgentOS 控制台.url"
  Delete "$INSTDIR\uninstall.exe"
  RMDir /r "$INSTDIR\bin\dist"
  RMDir "$INSTDIR\bin"
  RMDir "$INSTDIR"
  RMDir /r "$SMPROGRAMS\AgentOS"
  Delete "$DESKTOP\AgentOS.lnk"
  DeleteRegValue HKLM "${RUNKEY}" "AgentOSTray"
  DeleteRegValue HKCU "${RUNKEY}" "AgentOSTray"
  DeleteRegKey HKLM "${REGKEY}"
  DeleteRegKey HKLM "Software\AgentOS"

  ${GetParameters} $R0
  ClearErrors
  ${GetOptions} $R0 "/PURGE" $R1
  ${IfNot} ${Errors}
    RMDir /r "$APPDATA\AgentOS"
  ${Else}
    MessageBox MB_YESNO|MB_ICONQUESTION "是否同时删除 AgentOS 数据目录 (Agent 配置、日志、数据库)？$\r$\n$APPDATA\AgentOS" /SD IDNO IDNO keep
      RMDir /r "$APPDATA\AgentOS"
    keep:
  ${EndIf}
SectionEnd
