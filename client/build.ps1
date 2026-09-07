$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
if ((& go env GOVERSION) -ne 'go1.26.8') { throw 'Install Go 1.26.8 to reproduce this release.' }
$env:CGO_ENABLED = '0'
$env:GOARCH = 'amd64'
New-Item -ItemType Directory -Force out | Out-Null
$env:GOOS = 'linux'
& go build -buildvcs=false -trimpath '-ldflags=-s -w' -o out/tunnel-client-linux-amd64 ./cmd/tunnel-client
if ($LASTEXITCODE -ne 0) { throw 'Linux build failed' }
$env:GOOS = 'windows'
& go build -buildvcs=false -trimpath '-ldflags=-s -w' -o out/tunnel-client-windows-amd64.exe ./cmd/tunnel-client
if ($LASTEXITCODE -ne 0) { throw 'Windows build failed' }
& go build -buildvcs=false -trimpath '-ldflags=-s -w' -o out/1cattunnel-windows-ssh-amd64.exe ./cmd/tunnel-windows-client
if ($LASTEXITCODE -ne 0) { throw 'Windows SSH launcher build failed' }
Write-Output 'Native clients built in out/.'
