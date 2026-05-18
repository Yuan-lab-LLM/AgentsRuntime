package process

import (
	"reflect"
	"testing"

	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
)

// TEMP(testing): matches manager.go — OPENCLAW_SKIP_CHANNELS=1 is not injected.
func TestOpenClawStartEnvNoSkipWhenChannelsEnvMissing(t *testing.T) {
	env := openClawStartEnv([]string{"PATH=/usr/bin"}, "", false)

	if got := envValue(env, skipChannelsEnv); got != "" {
		t.Fatalf("expected %s absent when %s is missing (temp: no inject), got %q", skipChannelsEnv, channelsJSONEnv, got)
	}
}

func TestOpenClawStartEnvNoSkipWhenChannelsEnvBlank(t *testing.T) {
	env := openClawStartEnv([]string{"PATH=/usr/bin", skipChannelsEnv + "=0"}, "  \t\n", true)

	if got := envValue(env, skipChannelsEnv); got != "" {
		t.Fatalf("expected %s stripped when %s is blank (temp: no inject), got %q", skipChannelsEnv, channelsJSONEnv, got)
	}
}

func TestOpenClawStartEnvDoesNotSkipChannelsWhenChannelsEnvPresent(t *testing.T) {
	env := openClawStartEnv([]string{"PATH=/usr/bin", skipChannelsEnv + "=1"}, `{"dingtalk":{"enabled":true}}`, true)

	if got := envValue(env, skipChannelsEnv); got != "" {
		t.Fatalf("expected %s to be absent when %s is present, got %q", skipChannelsEnv, channelsJSONEnv, got)
	}
}

func TestGatewayModelsWarmupCommand(t *testing.T) {
	bin, args := gatewayModelsWarmupCommand(appconfig.Config{
		OpenClawCommand: []string{"/usr/local/bin/openclaw", "gateway", "run"},
	})

	if bin != "/usr/local/bin/openclaw" {
		t.Fatalf("expected warmup to use configured openclaw binary, got %q", bin)
	}
	want := []string{"gateway", "call", "models.list", "--params", "{}", "--timeout", "180000", "--json"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected warmup args:\nwant %#v\n got %#v", want, args)
	}
}

func TestGatewayReadyURL(t *testing.T) {
	tests := map[string]string{
		"http://127.0.0.1:18789/health":            "http://127.0.0.1:18789/readyz",
		"http://localhost:18789/health?check=true": "http://localhost:18789/readyz",
		"https://gateway.example/health":           "https://gateway.example/readyz",
		"":                                         "",
		"://bad":                                   "",
	}

	for input, want := range tests {
		if got := gatewayReadyURL(input); got != want {
			t.Fatalf("gatewayReadyURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGatewayChannelSettleDelayFromOS(t *testing.T) {
	t.Setenv(channelsJSONEnv, "")
	if got := gatewayChannelSettleDelayFromOS(); got != 0 {
		t.Fatalf("blank channels env settle delay = %s, want 0", got)
	}

	t.Setenv(channelsJSONEnv, `{"dingtalk":{"enabled":true}}`)
	if got := gatewayChannelSettleDelayFromOS(); got != gatewayChannelSettleDelay {
		t.Fatalf("configured channels env settle delay = %s, want %s", got, gatewayChannelSettleDelay)
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if len(item) >= len(prefix) && item[:len(prefix)] == prefix {
			return item[len(prefix):]
		}
	}
	return ""
}
