package process

import (
	"context"
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"strings"
	"testing"
)

func TestOpenClawActivationBeforeDoctorOrProcessStart(t *testing.T) {
	for _, flags := range [][3]string{{"invalid", "0", "0"}, {"0", "1", "0"}, {"0", "0", "1"}} {
		t.Setenv("CLAWMANAGER_OPENCLAW_TASK_PROTOCOL", flags[0])
		t.Setenv("CLAWMANAGER_OPENCLAW_CAPABILITY_RENEWAL", flags[1])
		t.Setenv("CLAWMANAGER_OPENCLAW_CAPABILITY_ENROLLMENT", flags[2])
		manager := New(appconfig.Config{OpenClawCommand: []string{"must-not-run"}})
		err := manager.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "启用校验") {
			t.Fatalf("gate not reached: %v", err)
		}
		state := manager.Snapshot()
		if state.Status != StatusCrashed || state.PID != 0 || !strings.Contains(state.LastExitReason, "启用校验") {
			t.Fatalf("incorrect failure state: %+v", state)
		}
	}
}
