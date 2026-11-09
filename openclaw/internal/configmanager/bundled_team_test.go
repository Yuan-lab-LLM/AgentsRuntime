package configmanager

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDisabledTeamStillSelectsTrustedManagedBundleWithoutEnablingChannel(t *testing.T) {
	t.Setenv("CLAWMANAGER_OPENCLAW_VERSION", "2026.10.2")
	t.Setenv("CLAWMANAGER_TEAM_ENABLED", "false")
	root := t.TempDir()
	bundle := filepath.Join(root, "bundled", "redis-team")
	if err := os.MkdirAll(bundle, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, pluginManifestName), []byte(`{"id":"redis-team","channels":["redis-team"]}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"plugins": map[string]any{"entries": map[string]any{"redis-team": map[string]any{"enabled": false}}}}
	opts := channelOverrides{BundledExtensionsDir: filepath.Dir(bundle), UserExtensionsDir: filepath.Join(root, "user")}
	if err := applyChannelOverrides(cfg, opts); err != nil {
		t.Fatal(err)
	}
	plugins := cfg["plugins"].(map[string]any)
	paths := plugins["load"].(map[string]any)["paths"].([]any)
	if len(paths) != 1 || paths[0] != filepath.ToSlash(bundle) {
		t.Fatalf("managed source missing: %v", paths)
	}
	if plugins["entries"].(map[string]any)["redis-team"].(map[string]any)["enabled"] != false {
		t.Fatal("disabled Team was enabled")
	}
	if _, ok := cfg["channels"].(map[string]any)["redis-team"]; ok {
		t.Fatal("non-Team instance acquired Team channel")
	}
}

func TestManagedBundledTeamSelectorPreservesOtherLoadPathsAndIsIdempotent(t *testing.T) {
	cfg := map[string]any{"plugins": map[string]any{"load": map[string]any{"paths": []any{"/user/custom-plugin"}}}}
	root := "/usr/local/lib/node_modules/openclaw/dist/extensions/redis-team"
	selectManagedBundledTeamPath(cfg, root)
	selectManagedBundledTeamPath(cfg, root)
	paths := cfg["plugins"].(map[string]any)["load"].(map[string]any)["paths"].([]any)
	if len(paths) != 2 || paths[0] != "/user/custom-plugin" || paths[1] != root {
		t.Fatalf("load paths=%v", paths)
	}
}

func TestTeamEnvDefaultsAreVisibleToNativeAdmissionWithoutReplacingAccountOwnership(t *testing.T) {
	t.Setenv("CLAWMANAGER_TEAM_REDIS_URL", "redis://isolated:6379/0")
	t.Setenv("CLAWMANAGER_TEAM_ID", "test-team")
	t.Setenv("CLAWMANAGER_TEAM_MEMBER_ID", "worker")
	t.Setenv("CLAWMANAGER_TEAM_AUTORUN", "false")
	managed := map[string]any{"fromEnv": true}
	explicit := map[string]any{"fromEnv": true, "enabled": false, "memberId": "explicit", "autoRun": true}
	owned := map[string]any{"fromEnv": false, "redisUrl": "redis://user-owned", "memberId": "user"}
	before := map[string]any{"fromEnv": false, "redisUrl": "redis://user-owned", "memberId": "user"}
	channels := map[string]any{redisTeamChannelID: map[string]any{"accounts": map[string]any{"default": managed, "explicit": explicit, "owned": owned}}}
	materializeRedisTeamEnvDefaults(channels)
	materializeRedisTeamEnvDefaults(channels)
	if managed["enabled"] != true || managed["teamId"] != "test-team" || managed["memberId"] != "worker" || managed["redisUrl"] != "redis://isolated:6379/0" || managed["autoRun"] != false {
		t.Fatal("managed Team admission does not match env defaults")
	}
	if explicit["enabled"] != false || explicit["memberId"] != "explicit" || explicit["autoRun"] != true || !reflect.DeepEqual(owned, before) {
		t.Fatal("explicit account configuration was replaced")
	}
}
