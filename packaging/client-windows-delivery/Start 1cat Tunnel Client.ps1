$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
& (Join-Path $scriptDir "1cat Tunnel Client.exe") -config (Join-Path $scriptDir "client-windows.json") @args
