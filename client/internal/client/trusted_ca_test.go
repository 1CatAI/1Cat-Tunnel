package client

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func TestEmbeddedProjectCAIsValidAndTrusted(t *testing.T) {
	block, rest := pem.Decode(projectCAPEM)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatal("embedded project CA is not a single PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse embedded project CA: %v", err)
	}
	if !cert.IsCA {
		t.Fatal("embedded project certificate is not a CA")
	}
	if cert.Subject.CommonName != "1CatTunnel Private Root CA 2026" {
		t.Fatalf("unexpected CA common name %q", cert.Subject.CommonName)
	}
	if time.Until(cert.NotAfter) < 5*365*24*time.Hour {
		t.Fatalf("embedded project CA expires too soon: %s", cert.NotAfter)
	}

	tlsConfig, err := buildClientTLSConfig(Config{
		TLSEnabled:    true,
		TLSServerName: "dx.1catai.com",
		ServerAddr:    "dx.1catai.com:50001",
	})
	if err != nil {
		t.Fatalf("build TLS config: %v", err)
	}
	if tlsConfig.RootCAs == nil {
		t.Fatal("TLS config has no root CA pool")
	}
}
