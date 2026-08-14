package hermesimage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboardGatewayScriptStartsRedisTeamConsumerWhenAutorunEnabled(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "start-hermes-dashboard-gateway"))
	if err != nil {
		t.Fatalf("read start-hermes-dashboard-gateway: %v", err)
	}
	script := string(data)
	for _, want := range []string{
		"CLAWMANAGER_TEAM_ENABLED",
		"CLAWMANAGER_TEAM_AUTORUN",
		"CLAWMANAGER_TEAM_REDIS_URL",
		"CLAWMANAGER_TEAM_ID",
		"CLAWMANAGER_TEAM_MEMBER_ID",
		"HERMES_TEAM_WORKER_PORT",
		"HERMES_TEAM_WORKER_HOME",
		`.clawmanager-team-worker`,
		`export HERMES_HOME="${team_worker_home}/.hermes"`,
		`export CLAWMANAGER_GATEWAY_PORT="${team_worker_port}"`,
		`CLAWMANAGER_TEAM_READY_FILE`,
		`[ "${team_worker_port}" -eq "${port}" ]`,
		`[ "${team_worker_port}" -gt 65535 ]`,
		`/usr/local/bin/hermes-apply-runtime-config`,
		`for managed_identity in .env config.yaml SOUL.md AGENTS.md team.json team-introduction.md`,
		`export HERMES_HOME="${team_hermes_home}"`,
		`HERMES_GATEWAY_BUSY_INPUT_MODE="${HERMES_TEAM_BUSY_INPUT_MODE:-queue}"`,
		`HERMES_GATEWAY_BUSY_TEXT_MODE="${HERMES_TEAM_BUSY_TEXT_MODE:-queue}"`,
		`HERMES_GATEWAY_BUSY_ACK_ENABLED="${HERMES_TEAM_BUSY_ACK_ENABLED:-false}"`,
		`CLAWMANAGER_HERMES_TEAM_WORKER_PROFILE=true`,
		"hermes gateway run --accept-hooks --no-supervise",
		`wait -n "${wait_pids[@]}"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("start-hermes-dashboard-gateway missing %q", want)
		}
	}
	if strings.Count(script, "HERMES_GATEWAY_BUSY_TEXT_MODE") != 1 {
		t.Fatal("Team busy-text mode must be scoped to the isolated Team gateway only")
	}
	teamStart := strings.LastIndex(script, "start_team_gateway")
	dashboardStart := strings.LastIndex(script, `echo "Starting Hermes dashboard gateway`)
	startupStateClear := strings.LastIndex(script, "prepare_team_startup_state")
	waitStart := strings.LastIndex(script, `wait -n "${wait_pids[@]}"`)
	if startupStateClear < 0 || dashboardStart < 0 || teamStart < 0 || waitStart < 0 ||
		startupStateClear > dashboardStart || dashboardStart > teamStart || teamStart > waitStart {
		t.Fatalf("dashboard and isolated Team consumer must both start before lifecycle supervision")
	}
	if strings.Contains(script, `while true; do`) &&
		strings.Contains(script, `Hermes Redis Team consumer did not become ready`) {
		t.Fatal("dashboard startup is still serialized behind the Team consumer readiness loop")
	}
}

func TestDashboardGatewayScriptReusesValidatedManagedBundledSkills(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "start-hermes-dashboard-gateway"))
	if err != nil {
		t.Fatalf("read start-hermes-dashboard-gateway: %v", err)
	}
	script := string(data)
	for _, want := range []string{
		`prepare_managed_bundled_skills`,
		`/config/.hermes/skills`,
		`.no-bundled-skills`,
		`.clawmanager-managed-bundled-skills`,
		`find "${bundled_root}"`,
		`[ -f "${opt_out_marker}" ] && [ ! -f "${managed_marker}" ]`,
		`rm -f "${opt_out_marker}" "${managed_marker}"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("start-hermes-dashboard-gateway missing managed skill fallback %q", want)
		}
	}
}

func TestDockerfilePackagesCanonicalRedisTeamAdapter(t *testing.T) {
	data, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(data)
	if !strings.Contains(dockerfile, "COPY plugins/hermes-redis-team/ /tmp/hermes-vendor-plugins/redis_team/") {
		t.Fatal("Dockerfile does not package the canonical Hermes Redis Team adapter")
	}
	if strings.Contains(dockerfile, "COPY hermes/vendor-plugins/redis_team/") {
		t.Fatal("Dockerfile still packages the stale vendor mirror")
	}
}

func TestDockerfileAppliesVersionLockedTeamCompletionStopPatch(t *testing.T) {
	data, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(data)
	for _, want := range []string{
		"apply_team_completion_stop.py",
		"/usr/local/lib/hermes-agent/agent/conversation_loop.py",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Fatalf("Dockerfile missing Hermes completion stop patch %q", want)
		}
	}
	patch, err := os.ReadFile(filepath.Join("patches", "hermes-agent", "apply_team_completion_stop.py"))
	if err != nil {
		t.Fatalf("read Team completion stop patch: %v", err)
	}
	for _, want := range []string{
		"clawmanager-team-completion-stop-v1",
		`_team_result_message.get("name") != "team_complete_task"`,
		`_team_result_payload.get("ok") is True`,
		`== "accepted"`,
		`_turn_exit_reason = "team_completion_accepted"`,
	} {
		if !strings.Contains(string(patch), want) {
			t.Fatalf("Hermes completion stop patch missing %q", want)
		}
	}
}

func TestTeamAssignedDashboardSelectsNativeTeamWorkerProfile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "start-hermes-dashboard-gateway"))
	if err != nil {
		t.Fatalf("read dashboard gateway script: %v", err)
	}
	script := string(data)
	teamBranch := strings.Index(script, "if should_start_team_gateway; then")
	dashboardStart := strings.LastIndex(script, `echo "Starting Hermes dashboard gateway`)
	if teamBranch < 0 || dashboardStart < 0 || teamBranch > dashboardStart {
		t.Fatal("Team profile selection must happen before the Dashboard starts")
	}
	teamSetup := script[teamBranch:dashboardStart]
	for _, want := range []string{
		`team_hermes_home="${team_worker_home}/.hermes"`,
		`for managed_identity in .env config.yaml SOUL.md AGENTS.md team.json team-introduction.md`,
		`export HOME="${team_worker_home}"`,
		`export HERMES_HOME="${team_hermes_home}"`,
		`export XDG_CACHE_HOME="${team_worker_home}/.cache"`,
		`/usr/local/bin/hermes-apply-runtime-config`,
		`initialize_native_session_store`,
		`prepare_team_startup_state`,
	} {
		if !strings.Contains(teamSetup, want) {
			t.Fatalf("Team-assigned Dashboard profile setup missing %q", want)
		}
	}
	if !strings.Contains(script, `SessionDB(Path(os.environ["HERMES_HOME"]) / "state.db")`) {
		t.Fatal("Team profile must initialize Hermes' native session store before concurrent consumers start")
	}
	if strings.Contains(script, "CLAWMANAGER_HERMES_TEAM_SESSION_DB") ||
		strings.Contains(script, "clawmanager-team-sessions-v1") {
		t.Fatal("Dashboard must use Hermes' native Team Worker profile, not a parallel session projection")
	}
}

func TestApplyRuntimeConfigScopesTeamWorkerToolsets(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "hermes-apply-runtime-config"))
	if err != nil {
		t.Fatalf("read hermes-apply-runtime-config: %v", err)
	}
	script := string(data)
	for _, want := range []string{
		`platform_toolsets["redis_team"]`,
		`"redis_team"`,
		`"file"`,
		`"terminal"`,
		`"code_execution"`,
		`"web"`,
		`"browser"`,
		`"vision"`,
		`truthy_env("CLAWMANAGER_HERMES_TEAM_WORKER_PROFILE")`,
		`unique_nonempty([*disabled_toolsets, "kanban"])`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("hermes-apply-runtime-config missing Team toolset contract %q", want)
		}
	}
}

func TestDashboardGatewayScriptEnsuresBasicAuthBeforeBind(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "start-hermes-dashboard-gateway"))
	if err != nil {
		t.Fatalf("read start-hermes-dashboard-gateway: %v", err)
	}
	script := string(data)
	for _, want := range []string{
		"ensure_dashboard_basic_auth",
		"HERMES_DASHBOARD_BASIC_AUTH_USERNAME",
		"HERMES_DASHBOARD_BASIC_AUTH_PASSWORD",
		"CLAWMANAGER_DASHBOARD_BASIC_AUTH_PASSWORD",
		"CLAWMANAGER_INSTANCE_ACCESS_TOKEN",
		"CLAWMANAGER_INSTANCE_TOKEN",
		"CLAWMANAGER_GATEWAY_TOKEN",
		".clawmanager-dashboard-basic-auth",
		`--host "${host}"`,
		`--port "${port}"`,
		"--no-open",
		"--skip-build",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("start-hermes-dashboard-gateway missing %q", want)
		}
	}
	if idx := strings.Index(script, "hermes dashboard"); idx < 0 {
		t.Fatal("start-hermes-dashboard-gateway missing hermes dashboard launch")
	} else if strings.Contains(script[idx:], "--insecure") {
		t.Fatal("hermes dashboard launch must not pass --insecure; basic auth is required for non-loopback binds")
	}
}

func TestDashboardGatewayScriptStartsClawManagerInstanceAgent(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "start-hermes-dashboard-gateway"))
	if err != nil {
		t.Fatalf("read start-hermes-dashboard-gateway: %v", err)
	}
	script := string(data)
	for _, want := range []string{
		"agent_pid",
		"CLAWMANAGER_AGENT_ENABLED",
		"/usr/local/bin/clawmanager-agent",
		"unset RUNTIME_AGENT_CONTROL_TOKEN",
		"unset RUNTIME_AGENT_REPORT_TOKEN",
		"unset RUNTIME_AGENT_DATA_DIR",
		"unset RUNTIME_AGENT_PUBLIC_PORT",
		"unset RUNTIME_AGENT_LISTEN_ADDR",
		`kill "${agent_pid}"`,
		`wait "${agent_pid}"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("start-hermes-dashboard-gateway missing %q", want)
		}
	}
}

func TestApplyRuntimeConfigAliasesClawManagerProviderAsCustom(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "hermes-apply-runtime-config"))
	if err != nil {
		t.Fatalf("read hermes-apply-runtime-config: %v", err)
	}
	script := string(data)
	for _, want := range []string{
		`if provider_key == "clawmanager":`,
		`custom_entry = dict(provider_entry)`,
		`custom_entry["name"] = "custom"`,
		`providers_cfg["custom"] = custom_entry`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("hermes-apply-runtime-config missing %q", want)
		}
	}
}

func TestApplyRuntimeConfigAppliesScheduledTasks(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "hermes-apply-runtime-config"))
	if err != nil {
		t.Fatalf("read hermes-apply-runtime-config: %v", err)
	}
	script := string(data)
	for _, want := range []string{
		`"scheduled_tasks": (`,
		`CLAWMANAGER_HERMES_SCHEDULED_TASKS_JSON`,
		`def apply_scheduled_tasks(hermes_home):`,
		`jobs_path = cron_dir / "jobs.json"`,
		`scheduled_tasks_record = apply_scheduled_tasks(hermes_home)`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("hermes-apply-runtime-config missing %q", want)
		}
	}
}

func TestStartHermesGatewayEnsuresDefaultProfileAndProStart(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "start-hermes-gateway"))
	if err != nil {
		t.Fatalf("read start-hermes-gateway: %v", err)
	}
	script := string(data)
	for _, want := range []string{
		"ensure_default_gateway_profile",
		"hermes profile create default",
		"has_scheduled_tasks_env",
		"is_hermes_pro_desktop",
		"CLAWMANAGER_HERMES_SCHEDULED_TASKS_JSON",
		"hermes gateway run --accept-hooks --no-supervise",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("start-hermes-gateway missing %q", want)
		}
	}
	if strings.Contains(script, "exec hermes gateway'") || strings.Contains(script, "exec hermes gateway\"") {
		t.Fatal("start-hermes-gateway must not exec bare `hermes gateway` without run")
	}
	if strings.Contains(script, "&& exec hermes gateway\n") || strings.Contains(script, "&& exec hermes gateway'") {
		t.Fatal("start-hermes-gateway must not exec bare `hermes gateway` without run")
	}
}

func TestDockerfilePinsHermesAgentVersion(t *testing.T) {
	data, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(data)
	for _, want := range []string{
		"ARG HERMES_VERSION=0.16.0",
		"ARG HERMES_GIT_REF=v2026.6.5",
		"raw.githubusercontent.com/NousResearch/hermes-agent/${HERMES_GIT_REF}/scripts/install.sh",
		`--branch "${HERMES_GIT_REF}"`,
		`hermes-agent[dingtalk,messaging,matrix,wecom]==${HERMES_VERSION}`,
	} {
		if !strings.Contains(dockerfile, want) {
			t.Fatalf("Dockerfile missing %q", want)
		}
	}
}

func TestDashboardGatewayScriptAppliesRuntimeConfigBeforeReadingEnvFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "start-hermes-dashboard-gateway"))
	if err != nil {
		t.Fatalf("read start-hermes-dashboard-gateway: %v", err)
	}
	script := string(data)
	applyIndex := strings.Index(script, "/usr/local/bin/hermes-apply-runtime-config")
	envFileIndex := strings.Index(script, `env_file="${HERMES_HOME}/.env"`)
	if applyIndex < 0 {
		t.Fatal("start-hermes-dashboard-gateway missing hermes-apply-runtime-config")
	}
	if envFileIndex < 0 {
		t.Fatal("start-hermes-dashboard-gateway missing HERMES_HOME env file assignment")
	}
	if applyIndex >= envFileIndex {
		t.Fatal("hermes-apply-runtime-config must run before reading HERMES_HOME/.env")
	}
}
