package gateway

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func revocationExitRequiresCleanup(spec GatewayStartSpec, err error) bool {
	prefix := ""
	switch spec.RuntimeType {
	case "hermes":
		prefix = "CLAWMANAGER_HERMES_"
	case "deepseek-harness":
		prefix = "CLAWMANAGER_HARNESS_"
	default:
		return false
	}
	if envValue(spec.Env, prefix+"FORCE_STOP_ON_REVOKE") != "1" ||
		envValue(spec.Env, prefix+"TASK_IDENTITY_PROTOCOL") != "1" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(envValue(spec.Env, "CLAWMANAGER_TEAM_ENABLED"))) {
	case "1", "true", "yes", "on", "enabled":
		return false
	}
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 78
}

func cleanupRevocationExit(spec GatewayStartSpec, cmd *exec.Cmd, err error) error {
	if !revocationExitRequiresCleanup(spec, err) {
		return err
	}
	if cleanupErr := cleanupExitedGatewayGroup(cmd); cleanupErr != nil {
		return errors.Join(err, fmt.Errorf("%w: revocation process group cleanup unconfirmed", ErrGatewayStopFailed))
	}
	return err
}
