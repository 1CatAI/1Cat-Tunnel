@echo off
setlocal
chcp 65001 >nul
cd /d "%~dp0"
start "" "%~dp0\1cattunnel.exe" support
exit /b
