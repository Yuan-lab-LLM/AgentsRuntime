package netbroker

import (
	"context"
	"strings"
)

// NewFixedEgress reuses the same bounded proxy relay for a trusted sidecar whose
// credential is independently scoped and revalidated by the fixed backend. Only
// Egress is published by that caller; no business credential is returned.
func NewFixedEgress(ctx context.Context, clientIP, proxyURL, credential string) (*Broker, error) {
	if len(credential) < 4 || len(credential) > 4096 || strings.IndexFunc(credential, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
		return nil, errCredentials
	}
	return New(ctx, Config{ChildIP: clientIP, BackendURL: proxyURL, EgressProxyURL: proxyURL, Credentials: fixedEgressCredentials{ctx, credential}})
}

type fixedEgressCredentials struct {
	ctx   context.Context
	token string
}

func (c fixedEgressCredentials) Context() context.Context { return c.ctx }
func (c fixedEgressCredentials) Snapshot() (Lease, error) {
	if err := c.ctx.Err(); err != nil {
		return Lease{}, err
	}
	return Lease{EgressToken: c.token}, nil
}
