package process

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var errTaskActivation = errors.New("OpenClaw 新版任务协议尚未通过启用校验；请保持关闭，完成运行时验收后再启用")

// No external command is executed in default/legacy mode.
func checkOpenClawTaskActivation(ctx context.Context, env []string) error {
	return checkTaskActivation(ctx, env, runTaskActivationProbe)
}

func checkTaskActivation(ctx context.Context, env []string, probe func(context.Context, []string) ([]byte, error)) error {
	if _, err := capabilityFlags(env); err != nil {
		return err
	}
	value := ""
	count := 0
	for _, item := range env {
		if strings.HasPrefix(item, "CLAWMANAGER_OPENCLAW_TASK_PROTOCOL=") {
			value = strings.TrimPrefix(item, "CLAWMANAGER_OPENCLAW_TASK_PROTOCOL=")
			count++
		}
	}
	if count > 1 {
		return errTaskActivation
	}
	if value == "" || value == "0" {
		return nil
	}
	if value != "1" {
		return errTaskActivation
	}
	// Do not allow a request environment to replace the package root or inject
	// code into the verifier. The probe path is image-owned, not user supplied.
	safe := make([]string, 0, len(env))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		switch strings.ToUpper(key) {
		case "NODE_OPTIONS", "NODE_PATH", "OPENCLAW_PACKAGE_ROOT":
			continue
		}
		safe = append(safe, item)
	}
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := probe(deadline, safe)
	if err != nil || deadline.Err() != nil || len(raw) > 4096 {
		return errTaskActivation
	}
	var report struct {
		RuntimeVersion string `json:"runtime_version"`
		AdapterVersion string `json:"adapter_version"`
		Digest         string `json:"artifact_sha256"`
		Integrity      bool   `json:"integrity_verified"`
		NodeMatches    bool   `json:"node_version_matches"`
		Accepted       bool   `json:"release_accepted"`
		Ready          bool   `json:"activation_ready"`
	}
	if json.Unmarshal(raw, &report) != nil || report.RuntimeVersion != "2026.10.2" || report.AdapterVersion != "cm-openclaw-native-1" ||
		!report.Integrity || !report.NodeMatches || !report.Accepted || !report.Ready || !regexp.MustCompile("^[a-f0-9]{64}$").MatchString(report.Digest) {
		return errTaskActivation
	}
	return nil
}

func runTaskActivationProbe(ctx context.Context, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "node", "/usr/local/share/openclaw/patches/patch_managed_model_reference_102.mjs", "--inspect", "--require-activation-ready")
	cmd.Env = env
	cmd.WaitDelay = time.Second
	output := &activationProbeOutput{}
	cmd.Stdout = output
	err := cmd.Run()
	return output.data, err
}

type activationProbeOutput struct{ data []byte }

func (b *activationProbeOutput) Write(p []byte) (int, error) {
	remaining := 4096 - len(b.data)
	if len(p) > remaining {
		b.data = append(b.data, p[:remaining]...)
		return remaining, errTaskActivation
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
