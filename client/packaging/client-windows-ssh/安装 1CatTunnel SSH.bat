@echo off
setlocal
chcp 65001 >nul
title 1CatTunnel Windows SSH 安装与实时监视
cd /d "%~dp0"

fltmc >nul 2>&1
if errorlevel 1 (
  echo 正在申请 Windows 管理员权限，请在弹出的窗口中选择“是”...
  set "ONECAT_INSTALLER=%~f0"
  powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath $env:ONECAT_INSTALLER -Verb RunAs"
  exit /b
)

echo 正在安装或检查 1CatTunnel、OpenSSH 和自动启动服务...
echo.
1cattunnel.exe install --monitor
set "ONECAT_EXIT=%errorlevel%"
echo.
if "%ONECAT_EXIT%"=="0" (
  echo 监视窗口已关闭，后台 1CatTunnel 和 OpenSSH 服务仍会继续运行。
) else (
  echo 操作失败，退出代码：%ONECAT_EXIT%
)
echo 请按任意键关闭此窗口...
pause >nul
exit /b %ONECAT_EXIT%
