package llmconfig

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"testing"
)

func TestManagedContextWindowRoundTrip(t *testing.T) {
	req := gateway.CreateGatewayRequest{Environment: map[string]string{
		"CLAWMANAGER_LLM_CONTEXT_WINDOW":  "32000",
		"CLAWMANAGER_LLM_CONTEXT_WINDOWS": `{"vendor/model":64000,"model":128000,"unknown":0}`,
	}}
	settings, err := ResolveGateway(gateway.Config{}, req, ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if settings.ContextFor("vendor", "model") != 64000 || settings.ContextFor("other", "model") != 128000 || settings.ContextFor("other", "unknown") != 0 || settings.ContextFor("other", "new") != 32000 {
		t.Fatal("context precedence lost")
	}
	cfg := settings.ApplyTo(gateway.Config{})
	round := FromGatewayConfig(cfg)
	if round.ContextFor("vendor", "model") != 64000 || round.Canonical("vendor").ContextWindows["unknown"] != 0 {
		t.Fatal("context transport lost")
	}
	round.ContextWindows["model"] = 1
	if cfg.LLMContextWindows["model"] != 128000 {
		t.Fatal("context map aliased")
	}
}

func TestManagedContextWindowRejectsInvalid(t *testing.T) {
	for _, raw := range []string{`null`, `{"x":-1}`, `{"x":1.2}`, `{"x":2147483648}`, `{"":10}`} {
		_, err := ResolveGateway(gateway.Config{}, gateway.CreateGatewayRequest{Environment: map[string]string{"CLAWMANAGER_LLM_CONTEXT_WINDOWS": raw}}, ResolveOptions{})
		if err == nil {
			t.Fatalf("accepted invalid context: %s", raw)
		}
	}
}
