@echo off
setlocal
title 1cat Tunnel Client
set SCRIPT_DIR=%~dp0
"%SCRIPT_DIR%1cat Tunnel Client.exe" -config "%SCRIPT_DIR%client-windows.json" %*
