package client

import _ "embed"

// projectCAPEM anchors the private CA used by the managed 1CatTunnel service.
// The corresponding private key is never included in source or client packages.
//
//go:embed 1cat-tunnel-ca.pem
var projectCAPEM []byte
