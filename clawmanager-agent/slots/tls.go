package slots

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

func poolTLSConfig(cfg gateway.Config) (*tls.Config, error) {
	if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
		return nil, fmt.Errorf("slot pool management requires a server TLS keypair")
	}
	certificate, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil || len(certificate.Certificate) == 0 {
		return nil, fmt.Errorf("slot pool server TLS keypair is invalid or unavailable")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("slot pool server certificate is invalid")
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) || leaf.VerifyHostname("lite-agent.clawmanager.internal") != nil {
		return nil, fmt.Errorf("slot pool server certificate identity or validity check failed")
	}
	serverAuth := false
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth {
			serverAuth = true
		}
	}
	if !serverAuth {
		return nil, fmt.Errorf("slot pool certificate does not permit server authentication")
	}
	certificate.Leaf = leaf
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}, nil
}
