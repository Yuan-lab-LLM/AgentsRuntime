package managedconfig

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
)

func TestMigrationFailurePreservesGuardAndSystemCauseWithoutSecretText(t *testing.T) {
	e := migrationError("replace", ".env", fmt.Errorf("private-secret /private/path: %w", syscall.EACCES))
	if !errors.Is(e, ErrUnsafe) || !errors.Is(e, syscall.EACCES) || strings.Contains(e.Error(), "private-secret") {
		t.Fatal("unsafe diagnostic or lost rejection cause")
	}
	if e.(interface{ SafeDiagnosticCode() string }).SafeDiagnosticCode() != "replace_env" {
		t.Fatal("wrong fixed category")
	}
}
