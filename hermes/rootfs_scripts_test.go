package hermesimage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestLiteDockerfilePackagesCanonicalTeamAdapterAndCoHostedSupervisor(t *testing.T) {
	data, err := os.ReadFile("Dockerfile.lite")
	if err != nil {
		t.Fatalf("read Dockerfile.lite: %v", err)
	}
	dockerfile := string(data)
	for _, want := range []string{
		"COPY plugins/hermes-redis-team/ /opt/hermes-agent/plugins/platforms/redis_team/",
		"apply_lite_team_completion_stop.py /opt/hermes-agent/agent/turn_tool_round.py",
		"start-hermes-lite-runtime /usr/local/bin/start-hermes-lite-runtime",
		"hermes-apply-runtime-config /usr/local/bin/hermes-apply-runtime-config",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Fatalf("Dockerfile.lite missing Team compatibility contract %q", want)
		}
	}
	launcher, err := os.ReadFile(filepath.Join("scripts", "start-hermes-lite-runtime"))
	if err != nil {
		t.Fatalf("read Lite supervisor: %v", err)
	}
	for _, want := range []string{
		"start-hermes-lite-backend",
		"hermes gateway run --accept-hooks --no-supervise",
		"plugins/platforms/redis_team/adapter.py",
		"hermes-apply-runtime-config",
		"wait -n",
	} {
		if !strings.Contains(string(launcher), want) {
			t.Fatalf("Lite supervisor missing %q", want)
		}
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
		`_turn_exit_reason = "team_completion_accepted" if`,
	} {
		if !strings.Contains(string(patch), want) {
			t.Fatalf("Hermes completion stop patch missing %q", want)
		}
	}
}

func TestDockerfileRemovesObsoleteDashboardLiveSessionsPatch(t *testing.T) {
	data, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(data)
	completionRunIndex := strings.Index(dockerfile, "/usr/local/lib/hermes-agent/venv/bin/python /tmp/apply_team_completion_stop.py")
	desktopBuildIndex := strings.Index(dockerfile, "hermes desktop --build-only")
	if completionRunIndex < 0 || desktopBuildIndex < 0 || completionRunIndex > desktopBuildIndex {
		t.Fatal("Hermes Team completion patch must run before the Desktop build")
	}
	for _, removed := range []string{
		"apply_team_live_sessions.py",
		"clawmanager-team-live-session-poll-v1",
		"SessionsPage.tsx",
	} {
		if strings.Contains(dockerfile, removed) {
			t.Fatalf("Dockerfile still contains obsolete Dashboard live-session entry %q", removed)
		}
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

func TestApplyRuntimeConfigUsesSharedLLMResolver(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "hermes-apply-runtime-config"))
	if err != nil {
		t.Fatalf("read hermes-apply-runtime-config: %v", err)
	}
	script := string(data)
	for _, want := range []string{
		`clawmanager-agent llm-config`,
		`--format canonical`,
		`CLAWMANAGER_LLM_RESOLVED_CONFIG_JSON`,
		`resolved_llm.get("providers")`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("hermes-apply-runtime-config missing shared LLM resolver contract %q", want)
		}
	}
	if strings.Contains(script, `model_ref.partition("/")`) {
		t.Fatal("hermes-apply-runtime-config must not reimplement provider/model parsing")
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

func TestStartHermesGatewayDoesNotDuplicateDesktopBackend(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "start-hermes-gateway"))
	if err != nil {
		t.Fatalf("read start-hermes-gateway: %v", err)
	}
	script := string(data)
	for _, want := range []string{
		"ensure_default_gateway_profile",
		"hermes profile create default",
		"has_scheduled_tasks_env",
		"CLAWMANAGER_HERMES_SCHEDULED_TASKS_JSON",
		"hermes gateway run --accept-hooks --no-supervise",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("start-hermes-gateway missing %q", want)
		}
	}
	if strings.Contains(script, "is_hermes_pro_desktop") {
		t.Fatal("the channel gateway must not auto-start just because the Desktop is running")
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
		"ARG HERMES_VERSION=0.21.0",
		"ARG HERMES_GIT_REF=v2026.8.31",
		"ARG HERMES_GIT_COMMIT=29112bef099274229cadff79cdff7bf7b99c4b77",
		"raw.githubusercontent.com/NousResearch/hermes-agent/${HERMES_GIT_REF}/scripts/install.sh",
		`--branch "${HERMES_GIT_REF}"`,
		`hermes-agent[dingtalk,messaging,matrix,pty,web,wecom]==${HERMES_VERSION}`,
		`git -C /usr/local/lib/hermes-agent rev-parse HEAD`,
	} {
		if !strings.Contains(dockerfile, want) {
			t.Fatalf("Dockerfile missing %q", want)
		}
	}
}

func TestProImageBuildsAndAutostartsHermesDesktop(t *testing.T) {
	dockerData, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(dockerData)
	for _, want := range []string{
		"hermes desktop --build-only",
		"apps/desktop/release",
		"chrome-sandbox",
		"/usr/local/bin/start-hermes-desktop",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Fatalf("Dockerfile missing Desktop contract %q", want)
		}
	}
	for _, removed := range []string{"npm run build -w web", "start-hermes-dashboard-gateway", "start-hermes-terminal"} {
		if strings.Contains(dockerfile, removed) {
			t.Fatalf("Dockerfile still contains obsolete Pro entry %q", removed)
		}
	}

	launcherData, err := os.ReadFile(filepath.Join("rootfs", "usr", "local", "bin", "start-hermes-desktop"))
	if err != nil {
		t.Fatalf("read Desktop launcher: %v", err)
	}
	launcher := string(launcherData)
	for _, want := range []string{
		"hermes desktop --skip-build",
		"--cwd /config",
		"--hermes-root /usr/local/lib/hermes-agent",
		"hermes-apply-runtime-config",
		"DBUS_SESSION_BUS_ADDRESS",
		"pgrep -o -x plasmashell",
	} {
		if !strings.Contains(launcher, want) {
			t.Fatalf("Desktop launcher missing %q", want)
		}
	}
	if strings.Contains(launcher, "konsole") || strings.Contains(launcher, "xterm") {
		t.Fatal("Desktop launcher must not wrap Hermes in a terminal")
	}

	serviceData, err := os.ReadFile(filepath.Join("rootfs", "etc", "s6-overlay", "s6-rc.d", "hermes-desktop", "run"))
	if err != nil {
		t.Fatalf("read Desktop s6 service: %v", err)
	}
	service := string(serviceData)
	for _, want := range []string{
		"s6-setuidgid abc /usr/local/bin/start-hermes-desktop",
		"RUNTIME_AGENT_CONTROL_TOKEN",
		"HERMES_AUTOSTART",
	} {
		if !strings.Contains(service, want) {
			t.Fatalf("Desktop s6 service missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join("rootfs", "etc", "s6-overlay", "s6-rc.d", "user", "contents.d", "hermes-desktop")); err != nil {
		t.Fatalf("Desktop s6 service is not in the user bundle: %v", err)
	}

	initData, err := os.ReadFile(filepath.Join("rootfs", "custom-cont-init.d", "99-hermes-config"))
	if err != nil {
		t.Fatalf("read Desktop autostart config: %v", err)
	}
	initScript := string(initData)
	if !strings.Contains(initScript, "Exec=/usr/local/bin/start-hermes-desktop") ||
		!strings.Contains(initScript, "sed -i '\\|start-hermes-terminal|d; \\|start-hermes-desktop|d'") {
		t.Fatal("Desktop autostart must replace persisted terminal entries")
	}
}
