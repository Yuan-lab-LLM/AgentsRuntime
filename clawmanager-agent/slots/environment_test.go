package slots

import (
	"strings"
	"testing"
)

func TestImageEnvironmentNeverInheritsSupervisorSecrets(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "cloud-secret")
	t.Setenv("DB_PASSWORD", "db-secret")
	t.Setenv("GITHUB_TOKEN", "github-secret")
	t.Setenv("LANG", "C.UTF-8")
	result := strings.Join(imageEnvironment(nil), "\n")
	for _, secret := range []string{"cloud-secret", "db-secret", "github-secret"} {
		if strings.Contains(result, secret) {
			t.Fatalf("supervisor secret inherited: %s", result)
		}
	}
	if !strings.Contains(result, "LANG=C.UTF-8") {
		t.Fatal("static image environment lost")
	}
}
