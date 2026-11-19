package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type NativeUpgradeHelperRequest struct {
	Config    NativeStorageConfig    `json:"config"`
	Operation NativeUpgradeOperation `json:"operation"`
}
type NativeUpgradeFrame struct {
	Kind      string          `json:"kind"`
	Sequence  uint64          `json:"sequence,omitempty"`
	Allowed   bool            `json:"allowed,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	Uncertain bool            `json:"uncertain,omitempty"`
}

type scopedUpgradeHost struct {
	op       NativeUpgradeOperation
	cfg      Config
	input    *json.Decoder
	output   *json.Encoder
	mu       sync.Mutex
	sequence uint64
}

func (h *scopedUpgradeHost) matches(r WorkspaceUpgradeRequest) bool {
	return r == h.op.Request && r.LeaseToken == ""
}
func (h *scopedUpgradeHost) matchesRead(r WorkspaceUpgradeRequest) bool {
	if h.matches(r) {
		return true
	}
	expected := h.op.Request
	if expected.RecoveryMode != "retry" || expected.ResumeRolloutID == "" || expected.ResumeGeneration < 1 {
		return false
	}
	expected.RolloutID = expected.ResumeRolloutID
	expected.Generation = expected.ResumeGeneration
	return r == expected && r.LeaseToken == ""
}

func (h *scopedUpgradeHost) WorkspaceForUpgrade(r WorkspaceUpgradeRequest) (string, error) {
	if !h.matchesRead(r) {
		return "", fmt.Errorf("native host identity mismatch")
	}
	return ValidateWorkspacePath(h.cfg.WorkspaceRoot, h.cfg.RuntimeType, CreateGatewayRequest{UserID: r.UserID, InstanceID: r.InstanceID, WorkspacePath: filepath.Join(h.cfg.WorkspaceRoot, h.cfg.RuntimeType, fmt.Sprintf("user-%d", r.UserID), fmt.Sprintf("instance-%d", r.InstanceID))})
}
func (h *scopedUpgradeHost) UpgradeRoot() string {
	return filepath.Join(h.cfg.WorkspaceRoot, ".clawmanager-upgrades")
}
func (h *scopedUpgradeHost) InstanceActive(id int) bool { return id != h.op.Request.InstanceID }
func (h *scopedUpgradeHost) LockUpgradeOperation(id int) (func(), error) {
	if id != h.op.Request.InstanceID {
		return nil, fmt.Errorf("native lock identity mismatch")
	}
	return func() {}, nil
}
func (h *scopedUpgradeHost) UpgradeOperationContext(ctx context.Context, id int) context.Context {
	if id != h.op.Request.InstanceID {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		return cancelled
	}
	return ctx
}
func (h *scopedUpgradeHost) VerifyUpgradeWriter(ctx context.Context, r WorkspaceUpgradeRequest) error {
	if !h.matches(r) || ctx.Err() != nil {
		return fmt.Errorf("native writer scope invalid")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sequence++
	if err := h.output.Encode(NativeUpgradeFrame{Kind: "verify", Sequence: h.sequence}); err != nil {
		return err
	}
	var ack NativeUpgradeFrame
	if err := h.input.Decode(&ack); err != nil {
		return err
	}
	if ack.Kind != "verified" || ack.Sequence != h.sequence || !ack.Allowed {
		return fmt.Errorf("%w: native writer authorization revoked", ErrNativeStorageUncertain)
	}
	return nil
}
func (h *scopedUpgradeHost) KeepUpgradeWriterLease(ctx context.Context, r WorkspaceUpgradeRequest) (context.Context, func() error, error) {
	if err := h.VerifyUpgradeWriter(ctx, r); err != nil {
		return ctx, nil, err
	}
	child, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-child.Done():
				return
			case <-ticker.C:
				if h.VerifyUpgradeWriter(child, r) != nil {
					cancel()
					return
				}
			}
		}
	}()
	return child, func() error { cancel(); <-done; return nil }, nil
}

func runNativeUpgradeHelper(profile RuntimeProfile, in io.Reader, out io.Writer) error {
	if err := protectNativeStorageHelper(); err != nil {
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(in, 8<<20))
	decoder.DisallowUnknownFields()
	var payload NativeUpgradeHelperRequest
	if err := decoder.Decode(&payload); err != nil {
		return err
	}
	op := payload.Operation
	if err := op.Validate(); err != nil {
		return err
	}
	if op.Request.LeaseToken != "" || os.Geteuid() != op.Request.UID || os.Getegid() != op.Request.GID || profile == nil || profile.Type() != payload.Config.RuntimeType || payload.Config.SandboxProfile != "nfs-rootless-v1" || payload.Config.NetworkSecurityProfile != "isolated-network-v1" {
		return fmt.Errorf("native upgrade helper identity mismatch")
	}
	provider, ok := profile.(ScopedNativeUpgradeProvider)
	if !ok {
		return fmt.Errorf("scoped native upgrade unavailable")
	}
	cfg := payload.Config.config(profile)
	cfg.IsolatedLifecycle = true
	cfg.GatewayBaseEnv = append([]string{}, os.Environ()...)
	encoder := json.NewEncoder(out)
	host := &scopedUpgradeHost{op: op, cfg: cfg, input: decoder, output: encoder}
	result, err := provider.RunScopedNativeUpgrade(context.Background(), cfg, op, host)
	frame := NativeUpgradeFrame{Kind: "result", Result: result, Uncertain: errors.Is(err, ErrNativeStorageUncertain)}
	if err != nil {
		frame.Error = err.Error()
		if len(frame.Error) > 4096 {
			frame.Error = frame.Error[:4096]
		}
	}
	if len(frame.Result) > 1<<20 {
		return fmt.Errorf("native result exceeds limit")
	}
	return encoder.Encode(frame)
}
