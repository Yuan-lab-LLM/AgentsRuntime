package dshproxy

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestProxyRevocationExitHelper(t *testing.T) {
	if os.Getenv("CM_PROXY_REVOCATION_HELPER") == "1" {
		os.Exit(78)
	}
}

func TestCommandExitCodePreservesOnlyOptInRealExit(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestProxyRevocationExitHelper$")
	cmd.Env = append(os.Environ(), "CM_PROXY_REVOCATION_HELPER=1")
	err := cmd.Run()
	if err == nil {
		t.Fatal("helper did not exit")
	}
	t.Setenv("CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL", "")
	t.Setenv("CLAWMANAGER_HARNESS_FORCE_STOP_ON_REVOKE", "")
	t.Setenv("CLAWMANAGER_TEAM_ENABLED", "")
	if CommandExitCode(err) != 1 {
		t.Fatal("legacy exit changed")
	}
	t.Setenv("CLAWMANAGER_HARNESS_TASK_IDENTITY_PROTOCOL", "1")
	t.Setenv("CLAWMANAGER_HARNESS_FORCE_STOP_ON_REVOKE", "1")
	if CommandExitCode(unexpectedChildExit(err)) != 78 {
		t.Fatal("real child exit lost across proxy")
	}
	if CommandExitCode(errors.New("exit status 78")) != 1 {
		t.Fatal("text used as evidence")
	}
	t.Setenv("CLAWMANAGER_TEAM_ENABLED", "true")
	if CommandExitCode(err) != 1 {
		t.Fatal("Team exit changed")
	}
}
