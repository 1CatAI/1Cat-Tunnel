@echo off
setlocal
set SCRIPT_DIR=%~dp0
"%SCRIPT_DIR%tunnel-client.exe" -config "%SCRIPT_DIR%client-windows.json" %*
