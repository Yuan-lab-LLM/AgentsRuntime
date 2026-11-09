package slots

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func poolTLSFixture(t *testing.T, name string, expired bool) gateway.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	expires := now.Add(time.Hour)
	if expired {
		expires = now.Add(-time.Minute)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: now.Add(-time.Hour), NotAfter: expires, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := gateway.Config{TLSCertFile: filepath.Join(dir, "tls.crt"), TLSKeyFile: filepath.Join(dir, "tls.key")}
	if err := os.WriteFile(cfg.TLSCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.TLSKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPoolTLSRequiresValidServerIdentity(t *testing.T) {
	if _, err := poolTLSConfig(gateway.Config{}); err == nil {
		t.Fatal("pool listener accepted missing TLS keypair")
	}
	for _, cfg := range []gateway.Config{poolTLSFixture(t, "wrong.invalid", false), poolTLSFixture(t, "lite-agent.clawmanager.internal", true)} {
		if _, err := poolTLSConfig(cfg); err == nil {
			t.Fatal("pool accepted expired/wrong server identity")
		}
	}
	cfg, err := poolTLSConfig(poolTLSFixture(t, "lite-agent.clawmanager.internal", false))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != tls.VersionTLS13 || len(cfg.Certificates) != 1 {
		t.Fatal("pool did not require TLS1.3")
	}
}
