package gateway

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestOpenClawGenerationUsesLifecycleIdentity(t *testing.T) {
	for _, generation := range []int{0, 7, 8} {
		t.Run(strconv.Itoa(generation), func(t *testing.T) {
			base := []string{"CLAWMANAGER_GATEWAY_GENERATION=2", "CLAWMANAGER_GATEWAY_GENERATION=3", "KEEP=value"}
			original := append([]string(nil), base...)
			req := CreateGatewayRequest{Generation: generation, Environment: map[string]string{"CLAWMANAGER_GATEWAY_GENERATION": "4"}, Env: map[string]string{"CLAWMANAGER_GATEWAY_GENERATION": "5"}}
			env := OpenClawGatewayEnv(base, Config{}, req, t.TempDir(), 20000)
			count := 0
			for _, value := range env {
				if strings.HasPrefix(value, "CLAWMANAGER_GATEWAY_GENERATION=") {
					count++
					if value != "CLAWMANAGER_GATEWAY_GENERATION="+strconv.Itoa(generation) {
						t.Fatalf("stale generation: %s", value)
					}
				}
			}
			if count != 1 {
				t.Fatalf("generation entries=%d", count)
			}
			if !reflect.DeepEqual(base, original) {
				t.Fatal("base environment mutated")
			}
			if req.Environment["CLAWMANAGER_GATEWAY_GENERATION"] != "4" || req.Env["CLAWMANAGER_GATEWAY_GENERATION"] != "5" {
				t.Fatal("request mutated")
			}
			if envValue(env, "CLAWMANAGER_OPENCLAW_TASK_PROTOCOL") != "" {
				t.Fatal("task protocol enabled implicitly")
			}
		})
	}
}
