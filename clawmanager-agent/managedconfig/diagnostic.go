package managedconfig

import "strings"

type migrationFailure struct {
	stage string
	cause error
}

func (e *migrationFailure) Error() string { return ErrUnsafe.Error() + ": " + e.stage }
func (e *migrationFailure) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrUnsafe}
	}
	return []error{ErrUnsafe, e.cause}
}
func (e *migrationFailure) SafeDiagnosticCode() string { return e.stage }
func migrationError(stage, name string, cause error) error {
	kind := "generated"
	if strings.Contains(name, "config.yaml") {
		kind = "yaml"
	} else if strings.Contains(name, "gateway.json") {
		kind = "json"
	} else if strings.Contains(name, ".env") {
		kind = "env"
	}
	return &migrationFailure{stage: stage + "_" + kind, cause: cause}
}
