package slots

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type networkOwner struct {
	mu     sync.Mutex
	active *networkSession
	build  func(context.Context, gateway.Config, gateway.CreateGatewayRequest, int) (*networkSession, error)
}
type networkSession struct {
	ctx                        context.Context
	cancel                     context.CancelFunc
	req                        gateway.CreateGatewayRequest
	port                       int
	digest                     [32]byte
	hosts, resolver            string
	wrap                       func(*exec.Cmd, int, int, []uint32) error
	release                    func(context.Context) error
	mu                         sync.Mutex
	closing, closed, uncertain bool
	aux                        sync.WaitGroup
	closeMu                    sync.Mutex
}

func (o *networkOwner) prepare(ctx context.Context, cfg gateway.Config, req gateway.CreateGatewayRequest, port int) (func(context.Context) error, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	o.mu.Lock()
	defer o.mu.Unlock()
	if s := o.active; s != nil {
		if s.digest != digest || s.port != port {
			return nil, fmt.Errorf("network session conflicts with active assignment")
		}
		if err = s.ctx.Err(); err != nil {
			return nil, fmt.Errorf("network session unavailable")
		}
		return o.cleanup(s), nil
	}
	build := o.build
	if build == nil {
		build = prepareNetworkSession
	}
	session, err := build(ctx, cfg, req, port)
	if session != nil {
		session.req = req
		session.port = port
		session.digest = digest
		o.active = session
	}
	if err != nil {
		if session != nil {
			return o.cleanup(session), err
		}
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("network preparation returned no session")
	}
	return o.cleanup(session), nil
}
func (o *networkOwner) cleanup(s *networkSession) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := s.close(ctx); err != nil {
			return err
		}
		o.mu.Lock()
		if o.active == s {
			o.active = nil
		}
		o.mu.Unlock()
		return nil
	}
}
func (o *networkOwner) lookup(spec gateway.GatewayStartSpec) (*networkSession, error) {
	if o == nil {
		return nil, fmt.Errorf("private network session unavailable")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	s := o.active
	if s == nil || s.req.InstanceID != spec.InstanceID || s.req.UserID != spec.UserID || s.req.Generation != spec.Generation || s.req.UID != spec.UID || s.req.GID != spec.GID || s.req.WorkspacePath != spec.WorkspacePath || s.req.AgentType != spec.RuntimeType || s.port != spec.Port {
		return nil, fmt.Errorf("private network assignment mismatch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.ctx.Err() != nil {
		return nil, fmt.Errorf("private network session unavailable")
	}
	return s, nil
}
func (o *networkOwner) join(spec gateway.GatewayStartSpec) (*networkSession, func(error), error) {
	s, err := o.lookup(spec)
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.ctx.Err() != nil {
		return nil, nil, fmt.Errorf("private network session is stopping")
	}
	s.aux.Add(1)
	var once sync.Once
	return s, func(err error) {
		once.Do(func() {
			if errors.Is(err, gateway.ErrGatewayStopFailed) {
				s.mu.Lock()
				s.uncertain = true
				s.mu.Unlock()
			}
			s.aux.Done()
		})
	}, nil
}
func (s *networkSession) close(ctx context.Context) error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	s.cancel()
	s.mu.Unlock()
	joined := make(chan struct{})
	go func() { s.aux.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-ctx.Done():
		return fmt.Errorf("%w: network auxiliary join incomplete", gateway.ErrGatewayStopFailed)
	}
	s.mu.Lock()
	uncertain := s.uncertain
	s.mu.Unlock()
	if uncertain {
		return fmt.Errorf("%w: auxiliary writer stop unconfirmed", gateway.ErrGatewayStopFailed)
	}
	if s.release != nil {
		if err := s.release(ctx); err != nil {
			return fmt.Errorf("%w: network cleanup unconfirmed", gateway.ErrGatewayStopFailed)
		}
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}
func (s sandboxStarter) PrepareGateway(ctx context.Context, req gateway.CreateGatewayRequest, port int) (func(context.Context) error, error) {
	if s.cfg.NetworkSecurityProfile == "" {
		return nil, nil
	}
	if s.cfg.NetworkSecurityProfile != "isolated-network-v1" || s.network == nil {
		return nil, fmt.Errorf("private network preparation unavailable")
	}
	return s.network.prepare(ctx, s.cfg, req, port)
}

func (o *networkOwner) unavailable() bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.active != nil && o.active.ctx.Err() != nil
}
