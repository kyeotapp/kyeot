package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCert makes a self-signed certificate and key for name.
func writeCert(t *testing.T, name string) (cert, key string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(k)
	dir := t.TempDir()
	cert, key = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), 0o600)
	return cert, key
}

func TestTLSFilesComeInPairs(t *testing.T) {
	t.Setenv("KYEOT_TLS_CERT", "/x/cert.pem")
	t.Setenv("KYEOT_TLS_KEY", "")
	if _, _, err := tlsFiles(); err == nil {
		t.Fatal("cert without key should fail")
	}
	t.Setenv("KYEOT_TLS_CERT", "")
	if c, k, err := tlsFiles(); err != nil || c != "" || k != "" {
		t.Fatalf("none: %q %q %v", c, k, err)
	}
}

func TestPairServerUsesCertDomainOverHTTPS(t *testing.T) {
	cert, key := writeCert(t, "kyeot.example.com")
	t.Setenv("KYEOT_TLS_CERT", cert)
	t.Setenv("KYEOT_TLS_KEY", key)
	if got, _ := pairServer("0.0.0.0:7700", ""); got != "https://kyeot.example.com:7700" {
		t.Fatalf("pair url: %s", got)
	}
	if got, _ := pairServer("0.0.0.0:7700", "other.example.com"); got != "https://other.example.com:7700" {
		t.Fatalf("pair url with -host: %s", got)
	}
}
