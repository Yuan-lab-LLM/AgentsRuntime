package managedconfig

import (
	"github.com/iamlovingit/clawmanager-agent/gateway"
	"strings"
	"testing"
)

func TestManagedEnvironmentPreservesCustomAndReplacesDuplicates(t *testing.T) {
	req := gateway.CreateGatewayRequest{SecurityProfile: "isolated-network-v1", Environment: map[string]string{"CLAWMANAGER_INSTANCE_TOKEN": "ui1_random", "CLAWMANAGER_LLM_API_KEY": "cm-local-model", "CLAWMANAGER_LLM_BASE_URL": ModelURL, "CLAWMANAGER_TEAM_REDIS_URL": "redis://clawmanager-local-services.invalid:31283/3"}}
	values, err := SafeEnvironment(gateway.Config{NetworkSecurityProfile: "isolated-network-v1"}, req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Environment([]byte("# keep\nexport OPENAI_API_KEY='old\nsecret'\nOPENAI_API_KEY=old2\nCUSTOM_KEY=third-party\nCLAWMANAGER_AGENT_CONTROL_TOKEN=oldcontrol\n"), values)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "old") || !strings.Contains(string(out), "CUSTOM_KEY=third-party") || !strings.Contains(string(out), "OPENAI_API_KEY=cm-local-model") {
		t.Fatalf("bad migration %q", out)
	}
	if _, err = Environment([]byte("OPENAI_API_KEY='unterminated"), values); err == nil {
		t.Fatal("accepted incomplete env")
	}
	req.SecurityProfile = ""
	if _, err = SafeEnvironment(gateway.Config{NetworkSecurityProfile: "isolated-network-v1"}, req); err == nil {
		t.Fatal("untrusted profile accepted")
	}
}
func TestManagedJSONRejectsDuplicateFields(t *testing.T) {
	for _, raw := range []string{`{"models":{"apiKey":"a","apiKey":"b"}}`, `null`, `{} {}`} {
		if _, err := JSON([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestManagedJSONPreservesEmptyCollectionsAndNumbers(t *testing.T) {
	object, err := JSON([]byte(`{"empty":[],"map":{},"large":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"empty": []`) || !strings.Contains(string(encoded), "9007199254740993") {
		t.Fatalf("changed unrelated JSON value: %s", encoded)
	}
}
