//go:build linux

package openclaw

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// This helper executes inside the existing business sandbox and UID. The
// native classifier only reads owner evidence; it never acquires or clears it.
func (p Profile) RunCommand(args []string) (bool, error) {
	if len(args) == 0 || args[0] != controlledGatewayStart {
		return false, nil
	}
	if p.Type() != "openclaw" || len(args) < 4 || args[2] != "--" {
		return true, fmt.Errorf("invalid controlled OpenClaw startup")
	}
	budget, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || budget <= 0 || budget > 120000 {
		return true, fmt.Errorf("invalid native owner admission budget")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	wait := time.Duration(budget) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, wait+min(5*time.Second, wait/2))
	defer cancel()
	guard := exec.CommandContext(ctx, "/usr/local/bin/node", "--input-type=module", "--eval", nativeOwnerAdmissionScript, "--", "--cm-openclaw-owner-admission", args[1])
	// Loader flags and provider credentials are unnecessary for this read-only
	// check. The original command receives its unchanged business environment.
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "OPENCLAW_STATE_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			guard.Env = append(guard.Env, key+"="+value)
		}
	}
	guard.Stdout, guard.Stderr = os.Stdout, os.Stderr
	if err = guard.Run(); err != nil {
		return true, fmt.Errorf("native_owner_admission_failed: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return true, err
	}
	command := args[3:]
	path, err := exec.LookPath(command[0])
	if err != nil {
		return true, fmt.Errorf("controlled gateway executable unavailable")
	}
	return true, syscall.Exec(path, command, os.Environ())
}
