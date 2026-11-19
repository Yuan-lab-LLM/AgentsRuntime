package openclaw

import (
	_ "embed"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

const ownerWaitEnvironment = "CLAWMANAGER_OPENCLAW_OWNER_WAIT_MS"
const controlledGatewayStart = "openclaw-controlled-gateway-start"

//go:embed controlled_startup.mjs
var nativeOwnerAdmissionScript string

func nativeOwnerWaitBudget(cfg gateway.Config) time.Duration {
	timeout := cfg.GatewayStartupTimeout
	if timeout <= 0 {
		timeout = NewProfile("openclaw").Defaults().GatewayStartupTimeout
	}
	return min(120*time.Second, timeout/2)
}

func controlledGatewayCommand(command, env []string) []string {
	wrapped := gateway.LiteTeamGatewayCommand("openclaw", command, env)
	if runtime.GOOS != "linux" || os.Getenv("RUNTIME_AGENT_ROLE") != "slot" || len(command) < 3 || filepath.Base(command[0]) != "openclaw" || command[1] != "gateway" || command[2] != "run" {
		return wrapped
	}
	budget := gateway.EnvironmentValue(env, ownerWaitEnvironment)
	if budget == "" {
		budget = strconv.FormatInt((120 * time.Second).Milliseconds(), 10)
	}
	return append([]string{"/usr/local/bin/clawmanager-agent", controlledGatewayStart, budget, "--"}, wrapped...)
}
