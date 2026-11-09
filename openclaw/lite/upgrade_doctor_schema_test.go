package openclaw

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Execute fake SDK entrypoints through the real command runner. These legacy
// runner fixtures use the test process identity; slot identity/fencing has its
// own tests and is not replaced or bypassed by the schema sequencing change.
func doctorSchemaFixture(t *testing.T, mode string) (*upgradeService, WorkspaceUpgradeRequest, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX SDK command fixture")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	state := filepath.Join(home, ".openclaw")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{filepath.Join(state, "state"), bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		filepath.Join(state, "openclaw.json"):                        "{}",
		filepath.Join(state, "state", "openclaw.sqlite"):             "old schema",
		filepath.Join(state, ".clawmanager-plugin-convergence.json"): "{}",
	} {
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	node := `#!/bin/sh
set -eu
[ "$1" = /usr/local/share/openclaw/patches/converge_plugin_records_102.mjs ]
printf 'metadata\n' >> "$HOME/.fixture-events"
schema_error() {
 printf '%s\n' 'OpenClawStateDatabaseSchemaMigrationRequiredError: OpenClaw state database schema migration required (audit-events-v2) at fixture; run openclaw doctor --fix to migrate it.' >&2
}
case "$CM_FAKE_DOCTOR_MODE" in
 metadata-rejected) printf 'Error: metadata rejected\n' >&2; exit 1;;
 schema-lookalike) printf 'OpenClawStateDatabaseSchemaMigrationRequiredError: unrelated audit-events-v2 issue; run openclaw doctor --fix\n' >&2; exit 1;;
 metadata-busy) schema_error; printf 'database is locked\n' >&2; exit 1;;
 always-schema) schema_error; exit 1;;
esac
if [ "$(cat "$OPENCLAW_STATE_DIR/state/openclaw.sqlite")" != migrated ]; then schema_error; exit 1; fi
if [ "$CM_FAKE_DOCTOR_MODE" = metadata-after-schema-fails ]; then printf 'Error: metadata rejected after migration\n' >&2; exit 1; fi
printf applied > "$HOME/.fixture-metadata-applied"
`
	doctor := `#!/bin/sh
set -eu
[ "$*" = 'doctor --fix --yes --non-interactive' ]
[ "$CLAWMANAGER_OPENCLAW_DOCTOR_MIGRATION" = 1 ]
printf 'doctor\n' >> "$HOME/.fixture-events"
case "$CM_FAKE_DOCTOR_MODE" in
 doctor-no-progress) printf 'Error: Doctor cannot repair\n' >&2; exit 1;;
 doctor-success-no-progress) exit 0;;
 always-schema) printf x >> "$OPENCLAW_STATE_DIR/state/openclaw.sqlite"; exit 0;;
esac
if [ "$(cat "$OPENCLAW_STATE_DIR/state/openclaw.sqlite")" != migrated ]; then
 printf migrated > "$OPENCLAW_STATE_DIR/state/openclaw.sqlite"
 if [ "$CM_FAKE_DOCTOR_MODE" = doctor-progress-failure ]; then printf 'Error: additional Doctor input discovered\n' >&2; exit 1; fi
else
 [ -f "$HOME/.fixture-metadata-applied" ]
fi
`
	for name, script := range map[string]string{"node": node, "openclaw": doctor} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CM_FAKE_DOCTOR_MODE", mode)
	m := &upgradeService{io: newFixtureUpgradeIO(root)}
	t.Cleanup(func() {
		for _, scope := range m.io.scopes {
			scope.root.Close()
		}
	})
	req := WorkspaceUpgradeRequest{InstanceID: 7, UserID: 1, Generation: 1, UID: 0, GID: 0, RolloutID: "schema"}
	return m, req, home, filepath.Join(state, "openclaw.json")
}

func TestDoctorDefersMetadataOnlyForRequiredStateSchema(t *testing.T) {
	for _, tc := range []struct{ mode, wantEvents, wantError string }{
		{"success", "metadata doctor metadata doctor", ""},
		{"doctor-progress-failure", "metadata doctor metadata doctor", ""},
		{"metadata-rejected", "metadata", "MANAGED_PLUGIN_METADATA_FAILED"},
		{"schema-lookalike", "metadata", "MANAGED_PLUGIN_METADATA_FAILED"},
		{"metadata-busy", "metadata", "MANAGED_PLUGIN_METADATA_FAILED"},
		{"doctor-no-progress", "metadata doctor", "NO_PROGRESS"},
		{"doctor-success-no-progress", "metadata doctor", "NO_PROGRESS"},
		{"metadata-after-schema-fails", "metadata doctor metadata", "MANAGED_PLUGIN_METADATA_FAILED"},
		{"always-schema", "metadata doctor metadata doctor metadata doctor metadata doctor", "DID_NOT_CONVERGE"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			m, req, home, config := doctorSchemaFixture(t, tc.mode)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := m.runOpenClawDoctorRepair(ctx, filepath.Dir(home), home, config, req, io.Discard)
			if tc.wantError == "" && err != nil {
				t.Fatalf("official Doctor repair blocked: %v", err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error=%v want %s", err, tc.wantError)
			}
			events, readErr := os.ReadFile(filepath.Join(home, ".fixture-events"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if actual := strings.Join(strings.Fields(string(events)), " "); actual != tc.wantEvents {
				t.Fatalf("SDK sequence=%q want %q", actual, tc.wantEvents)
			}
		})
	}
}

func TestMetadataSchemaDeferralRequiresConfirmedCLIExit(t *testing.T) {
	schema := []byte("OpenClawStateDatabaseSchemaMigrationRequiredError: OpenClaw state database schema migration required (audit-events-v2); run openclaw doctor --fix to migrate it.")
	exit := &exec.ExitError{}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name, phase     string
		ctx             context.Context
		runErr          error
		uncertain, want bool
	}{
		{"normal_cli_exit", "managed-plugin-metadata", context.Background(), exit, false, true},
		{"uncertain_namespace", "managed-plugin-metadata", context.Background(), exit, true, false},
		{"canceled", "managed-plugin-metadata", canceled, exit, false, false},
		{"joined_teardown_failure", "managed-plugin-metadata", context.Background(), errors.Join(exit, errors.New("teardown unconfirmed")), false, false},
		{"controller_failure", "managed-plugin-metadata", context.Background(), errors.New("tracker failed"), false, false},
		{"successful_exit", "managed-plugin-metadata", context.Background(), nil, false, false},
		{"doctor_phase", "doctor-fix-pass-1", context.Background(), exit, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDeferredMetadataSchemaError(tc.ctx, tc.phase, schema, tc.runErr, tc.uncertain); got != tc.want {
				t.Fatalf("schema deferral=%v want %v", got, tc.want)
			}
		})
	}
}
