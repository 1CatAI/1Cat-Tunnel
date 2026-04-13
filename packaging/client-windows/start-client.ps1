$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
& (Join-Path $scriptDir "tunnel-client.exe") -config (Join-Path $scriptDir "client-windows.json") @args
