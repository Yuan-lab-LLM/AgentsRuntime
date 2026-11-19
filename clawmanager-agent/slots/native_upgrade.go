package slots

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/sandbox"
)

func nativeUpgradeCallbackPath(operationID string) (string, error) {
	if !executionTokenValid(operationID) {
		return "", fmt.Errorf("invalid native callback identity")
	}
	return filepath.Join("/run/clawmanager-slots", "upgrade-"+operationID+".sock"), nil
}

// VerifyNativeUpgradeParent supports exactly one authorization checkpoint. The
// operation-specific socket belongs to management and never enters the sandbox.
func VerifyNativeUpgradeParent(ctx context.Context, operationID string) error {
	path, err := nativeUpgradeCallbackPath(operationID)
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = conn.Write([]byte("V1\n")); err != nil {
		return err
	}
	var response [3]byte
	if _, err = io.ReadFull(conn, response[:]); err != nil {
		return err
	}
	if string(response[:]) != "OK\n" {
		return fmt.Errorf("parent writer verification failed")
	}
	return nil
}

func prepareNativeUpgradeCapsules(cfg gateway.Config, op gateway.NativeUpgradeOperation) error {
	for _, rollout := range []string{op.Request.RolloutID, op.Request.ResumeRolloutID} {
		if rollout == "" {
			continue
		}
		if err := runStorageGrant(gateway.StorageGrantRequest{Root: cfg.WorkspaceRoot, CapsuleRollout: rollout, InstanceID: op.Request.InstanceID, GID: op.Request.GID}, op.Request.GID); err != nil {
			return err
		}
	}
	return nil
}

// ExecuteNativeUpgrade returns a staged result only. The worker must persist it
// and reset its container; the coordinator publishes it after termination proof.
func ExecuteNativeUpgrade(ctx context.Context, cfg gateway.Config, op gateway.NativeUpgradeOperation, verify func(context.Context) error) (json.RawMessage, error) {
	if cfg.SandboxProfile != "nfs-rootless-v1" || cfg.NetworkSecurityProfile != "isolated-network-v1" || cfg.Runtime == nil || verify == nil {
		return nil, fmt.Errorf("native maintenance execution unavailable")
	}
	if err := op.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := op.Request
	workspace := filepath.Join(cfg.WorkspaceRoot, cfg.RuntimeType, fmt.Sprintf("user-%d", r.UserID), fmt.Sprintf("instance-%d", r.InstanceID))
	source, err := checkedSandboxSource(workspace)
	if err != nil {
		return nil, err
	}
	mounts := []sandbox.Mount{{Source: source, Target: workspace, ReadOnly: !op.MutatesWorkspace()}}
	for _, capsule := range []struct {
		rollout  string
		readonly bool
	}{{r.RolloutID, !op.WritesCapsule()}, {r.ResumeRolloutID, true}} {
		if capsule.rollout == "" || capsule.rollout == r.RolloutID && len(mounts) > 1 {
			continue
		}
		path := filepath.Join(cfg.WorkspaceRoot, ".clawmanager-upgrades", "rollout-"+capsule.rollout, fmt.Sprintf("instance-%d", r.InstanceID))
		source, err = checkedSandboxSource(path)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, sandbox.Mount{Source: source, Target: path, ReadOnly: capsule.readonly})
	}
	privatePaths := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		privatePaths = append(privatePaths, mount.Source)
	}
	if err := runPrivateStorageAudit(r.UID, r.GID, privatePaths, false); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	spec := sandbox.Spec{ContainerLifecycle: true, OfflineNetwork: true, UID: r.UID, GID: r.GID, Dir: workspace, Mounts: mounts, Args: []string{executable, "managed-native-upgrade"}, Env: imageEnvironment(cfg.Runtime)}
	argv, err := sandbox.Command(spec)
	if err != nil {
		return nil, err
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(childCtx, argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.WaitDelay = 2 * time.Second
	if err = configureSandboxProcess(cmd, r.UID, r.GID, nil); err != nil {
		return nil, err
	}
	cmd.Cancel = func() error { return sandbox.SignalProcess(cmd.Process, uint32(r.UID), syscall.SIGKILL) }
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer input.Close()
	outputR, outputW := io.Pipe()
	defer outputR.Close()
	cmd.Stdout = outputW
	var stderr boundedCommandStream
	stderr.tail = true
	cmd.Stderr = &stderr
	tracker, err := sandbox.TrackContainer(cmd)
	if err != nil {
		return nil, err
	}
	defer tracker.Close()
	// OCI's fixed management bootstrap retains inheritable SETUID/GID. Drop
	// those only in the already selected business child before bubblewrap's guard.
	cmd.Args = append([]string{"/usr/bin/setpriv", "--inh-caps=-all", "--ambient-caps=-all", "--"}, cmd.Args...)
	cmd.Path = "/usr/bin/setpriv"

	if err = cmd.Start(); err != nil {
		return nil, err
	}
	waited := make(chan error, 1)
	go func() { err := cmd.Wait(); outputW.CloseWithError(err); waited <- err }()
	startCtx, cancelStart := context.WithTimeout(childCtx, 10*time.Second)
	err = tracker.Started(startCtx)
	cancelStart()
	if err != nil {
		cancel()
		<-waited
		detail := strings.TrimSpace(stderr.buffer.String())
		if len(detail) > 4096 {
			detail = detail[len(detail)-4096:]
		}
		return nil, fmt.Errorf("%w: %v: %s", gateway.ErrNativeStorageUncertain, err, detail)
	}
	encoder := json.NewEncoder(input)
	if err = encoder.Encode(gateway.NativeUpgradeHelperRequest{Config: gateway.NativeConfig(cfg), Operation: op.Child()}); err != nil {
		cancel()
		<-waited
		return nil, errors.Join(gateway.ErrNativeStorageUncertain, err)
	}
	decoder := json.NewDecoder(io.LimitReader(outputR, 2<<20))
	decoder.DisallowUnknownFields()
	var sequence uint64
	var result json.RawMessage
	var actionErr error
	for {
		var frame gateway.NativeUpgradeFrame
		if err = decoder.Decode(&frame); err != nil {
			actionErr = fmt.Errorf("%w: native maintenance result unavailable: %v", gateway.ErrNativeStorageUncertain, err)
			break
		}
		if frame.Kind == "verify" {
			if frame.Sequence != sequence+1 || frame.Sequence > 65536 {
				actionErr = fmt.Errorf("%w: native verification sequence invalid", gateway.ErrNativeStorageUncertain)
				break
			}
			sequence = frame.Sequence
			verifyErr := verify(childCtx)
			if err = encoder.Encode(gateway.NativeUpgradeFrame{Kind: "verified", Sequence: sequence, Allowed: verifyErr == nil}); err != nil {
				actionErr = err
				break
			}
			continue
		}
		if frame.Kind != "result" || len(frame.Result) > 1<<20 || len(frame.Error) > 4096 || frame.Allowed || frame.Sequence != 0 {
			actionErr = fmt.Errorf("%w: native maintenance result invalid", gateway.ErrNativeStorageUncertain)
			break
		}
		if len(frame.Result) > 0 && !json.Valid(frame.Result) {
			actionErr = fmt.Errorf("%w: native maintenance result JSON invalid", gateway.ErrNativeStorageUncertain)
			break
		}
		result = frame.Result
		if frame.Error != "" {
			actionErr = errors.New(frame.Error)
		}
		if frame.Uncertain {
			actionErr = errors.Join(gateway.ErrNativeStorageUncertain, actionErr)
		}
		break
	}
	input.Close()
	if actionErr != nil {
		cancel()
	}
	select {
	case waitErr := <-waited:
		if waitErr != nil && actionErr == nil {
			actionErr = fmt.Errorf("native maintenance helper failed: %w", waitErr)
		}
	case <-ctx.Done():
		cancel()
		<-waited
		actionErr = errors.Join(gateway.ErrNativeStorageUncertain, ctx.Err())
	}
	return bytes.Clone(result), actionErr
}
