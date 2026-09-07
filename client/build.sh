#!/bin/sh
set -eu
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
export CGO_ENABLED=0
test "$(go env GOVERSION)" = go1.26.8 || { echo 'Install Go 1.26.8 to reproduce this release.' >&2; exit 1; }
mkdir -p out
GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags='-s -w' -o out/tunnel-client-linux-amd64 ./cmd/tunnel-client
GOOS=windows GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags='-s -w' -o out/tunnel-client-windows-amd64.exe ./cmd/tunnel-client
GOOS=windows GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags='-s -w' -o out/1cattunnel-windows-ssh-amd64.exe ./cmd/tunnel-windows-client
echo 'Native clients built in out/. Third-party OpenSSH payload is not compiled by this script.'
