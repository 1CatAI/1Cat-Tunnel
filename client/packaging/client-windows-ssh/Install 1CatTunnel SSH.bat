@echo off
setlocal
chcp 65001 >nul
cd /d "%~dp0"

fltmc >nul 2>&1
if errorlevel 1 (
  set "ONECAT_INSTALLER=%~f0"
  powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Start-Process -FilePath $env:ONECAT_INSTALLER -Verb RunAs"
  exit /b
)

1cattunnel.exe install --monitor
set "ONECAT_EXIT=%errorlevel%"
echo.
pause
exit /b %ONECAT_EXIT%
