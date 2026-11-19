package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

const (
	defaultHermesStartupTimeout = 90 * time.Second
	teamReadinessPollInterval   = 100 * time.Millisecond
	maxTeamStartupStateBytes    = 64 << 10
)

type healthChecker struct {
	cfg  gateway.Config
	http gateway.GatewayHealthChecker
}

type teamStartupExpectation struct {
	workspace  string
	readyFile  string
	teamID     string
	memberID   string
	instanceID int
	generation int
}

type teamStartupState struct {
	Ready      bool   `json:"ready"`
	State      string `json:"state"`
	Runtime    string `json:"runtime"`
	TeamID     string `json:"teamId"`
	MemberID   string `json:"memberId"`
	InstanceID int    `json:"instanceId"`
	Generation int    `json:"generation"`
	Error      struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

type componentHealthResult struct {
	component string
	err       error
}

func newHealthChecker(cfg gateway.Config) gateway.GatewayHealthChecker {
	return &healthChecker{
		cfg:  cfg,
		http: gateway.NewHTTPGatewayHealthChecker(cfg),
	}
}

func (h *healthChecker) WaitReady(ctx context.Context, spec gateway.GatewayStartSpec) error {
	if truthy(envValue(spec.Env, "CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED")) {
		expectation, required, err := teamStartupExpectationFor(spec)
		if err != nil {
			return err
		}
		if !required {
			return h.waitDashboardReady(ctx, spec)
		}
		return h.waitRuntimeAndTeam(ctx, spec, expectation, "Hermes Dashboard", h.waitDashboardReady)
	}
	expectation, required, err := teamStartupExpectationFor(spec)
	if err != nil {
		return err
	}
	if !required {
		return h.http.WaitReady(ctx, spec)
	}
	return h.waitRuntimeAndTeam(ctx, spec, expectation, "Hermes runtime", h.http.WaitReady)
}

func (h *healthChecker) waitRuntimeAndTeam(
	ctx context.Context,
	spec gateway.GatewayStartSpec,
	expectation teamStartupExpectation,
	runtimeName string,
	runtimeReady func(context.Context, gateway.GatewayStartSpec) error,
) error {
	timeout := h.cfg.GatewayStartupTimeout
	if timeout <= 0 {
		timeout = defaultHermesStartupTimeout
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	results := make(chan componentHealthResult, 2)
	go func() {
		results <- componentHealthResult{
			component: runtimeName,
			err:       runtimeReady(readyCtx, spec),
		}
	}()
	go func() {
		results <- componentHealthResult{
			component: "Hermes Team consumer",
			err:       waitTeamConsumerReady(readyCtx, expectation),
		}
	}()

	for completed := 0; completed < 2; completed++ {
		result := <-results
		if result.err != nil {
			cancel()
			return fmt.Errorf("%s not ready: %w", result.component, result.err)
		}
	}
	return nil
}

func teamStartupExpectationFor(spec gateway.GatewayStartSpec) (teamStartupExpectation, bool, error) {
	enabled := truthy(envValue(spec.Env, "CLAWMANAGER_TEAM_ENABLED"))
	if !enabled || falsey(envValue(spec.Env, "CLAWMANAGER_TEAM_AUTORUN")) {
		return teamStartupExpectation{}, false, nil
	}

	teamID := strings.TrimSpace(envValue(spec.Env, "CLAWMANAGER_TEAM_ID"))
	memberID := strings.TrimSpace(envValue(spec.Env, "CLAWMANAGER_TEAM_MEMBER_ID"))
	redisURL := strings.TrimSpace(envValue(spec.Env, "CLAWMANAGER_TEAM_REDIS_URL"))
	if teamID == "" || memberID == "" || redisURL == "" {
		return teamStartupExpectation{}, false, fmt.Errorf(
			"Hermes Team startup configuration is incomplete: redis=%t team=%t member=%t",
			redisURL != "",
			teamID != "",
			memberID != "",
		)
	}

	readyFile := strings.TrimSpace(envValue(spec.Env, "CLAWMANAGER_TEAM_READY_FILE"))
	if readyFile == "" || !filepath.IsAbs(readyFile) {
		return teamStartupExpectation{}, false, fmt.Errorf("Hermes Team readiness file must be an absolute managed path")
	}
	expectedReadyFile := filepath.Join(
		spec.WorkspacePath,
		"home",
		".clawmanager-team-worker",
		".hermes",
		"runtime",
		"redis-team.ready.json",
	)
	if filepath.Clean(readyFile) != filepath.Clean(expectedReadyFile) {
		return teamStartupExpectation{}, false, fmt.Errorf(
			"Hermes Team readiness file escaped the managed worker home: got %s want %s",
			readyFile,
			expectedReadyFile,
		)
	}

	instanceID := spec.InstanceID
	if raw := strings.TrimSpace(envValue(spec.Env, "CLAWMANAGER_INSTANCE_ID")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed != spec.InstanceID {
			return teamStartupExpectation{}, false, fmt.Errorf("Hermes Team instance identity does not match the gateway start")
		}
		instanceID = parsed
	}
	generation := spec.Generation
	if raw := strings.TrimSpace(envValue(spec.Env, "CLAWMANAGER_GATEWAY_GENERATION")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed != spec.Generation {
			return teamStartupExpectation{}, false, fmt.Errorf("Hermes Team generation identity does not match the gateway start")
		}
		generation = parsed
	}

	return teamStartupExpectation{
		workspace:  spec.WorkspacePath,
		readyFile:  filepath.Clean(readyFile),
		teamID:     teamID,
		memberID:   memberID,
		instanceID: instanceID,
		generation: generation,
	}, true, nil
}

func waitTeamConsumerReady(ctx context.Context, expected teamStartupExpectation) error {
	var lastErr error
	for {
		if failure, exists, err := readTeamStartupState(expected.workspace, expected.readyFile+".failed"); err != nil {
			return fmt.Errorf("consumer readiness failure file is invalid: %w", err)
		} else if exists {
			if err := validateTeamStartupIdentity(failure, expected); err != nil {
				lastErr = err
			} else {
				code := strings.TrimSpace(failure.Error.Code)
				if code == "" {
					code = "startup_failed"
				}
				message := strings.TrimSpace(failure.Error.Message)
				if message == "" {
					message = "Hermes Team consumer reported a startup failure"
				}
				return fmt.Errorf("%s: %s", code, message)
			}
		}

		if state, exists, err := readTeamStartupState(expected.workspace, expected.readyFile); err != nil {
			return fmt.Errorf("consumer readiness file is invalid: %w", err)
		} else if exists {
			if err := validateTeamStartupIdentity(state, expected); err != nil {
				lastErr = err
			} else if state.Ready && strings.EqualFold(strings.TrimSpace(state.State), "ready") {
				return nil
			} else {
				lastErr = fmt.Errorf("consumer readiness state is %q", state.State)
			}
		} else if lastErr == nil {
			lastErr = fmt.Errorf("consumer readiness file has not been published")
		}

		timer := time.NewTimer(teamReadinessPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return fmt.Errorf("consumer readiness was not established: %w", lastErr)
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func readTeamStartupState(workspace, path string) (teamStartupState, bool, error) {
	root, err := workspaceio.Open(workspace)
	if err != nil {
		return teamStartupState{}, false, err
	}
	defer root.Close()
	info, err := root.Lstat(path)
	if os.IsNotExist(err) {
		return teamStartupState{}, false, nil
	}
	if err != nil {
		return teamStartupState{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return teamStartupState{}, false, fmt.Errorf("startup state must be a regular file")
	}
	if info.Size() <= 0 || info.Size() > maxTeamStartupStateBytes {
		return teamStartupState{}, false, fmt.Errorf("startup state size %d is invalid", info.Size())
	}
	data, err := root.ReadFile(path)
	if err != nil {
		return teamStartupState{}, false, err
	}
	var state teamStartupState
	if err := json.Unmarshal(data, &state); err != nil {
		return teamStartupState{}, false, err
	}
	return state, true, nil
}

func validateTeamStartupIdentity(state teamStartupState, expected teamStartupExpectation) error {
	if !strings.EqualFold(strings.TrimSpace(state.Runtime), "hermes") {
		return fmt.Errorf("consumer readiness runtime %q does not match hermes", state.Runtime)
	}
	if strings.TrimSpace(state.TeamID) != expected.teamID {
		return fmt.Errorf("consumer readiness Team %q does not match %q", state.TeamID, expected.teamID)
	}
	if strings.TrimSpace(state.MemberID) != expected.memberID {
		return fmt.Errorf("consumer readiness member %q does not match %q", state.MemberID, expected.memberID)
	}
	if state.InstanceID != expected.instanceID {
		return fmt.Errorf("consumer readiness instance %d does not match %d", state.InstanceID, expected.instanceID)
	}
	if state.Generation != expected.generation {
		return fmt.Errorf("consumer readiness generation %d does not match %d", state.Generation, expected.generation)
	}
	return nil
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for index := len(env) - 1; index >= 0; index-- {
		if strings.HasPrefix(env[index], prefix) {
			return strings.TrimPrefix(env[index], prefix)
		}
	}
	return ""
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on", "enabled":
		return true
	default:
		return false
	}
}

func falsey(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "n", "off", "disabled":
		return true
	default:
		return false
	}
}
