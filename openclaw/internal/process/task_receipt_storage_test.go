package process

import (
	"context"
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/receiptstorage"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProReceiptStorageDefaultPreservesLegacyEnvironment(t *testing.T) {
	base := []string{"KEEP=value", receiptstorage.DirectoryEnv + "=/legacy/explicit"}
	got, err := proReceiptEnvironment(appconfig.Config{}, base)
	if err != nil || !reflect.DeepEqual(got, base) {
		t.Fatal("legacy environment changed")
	}
	got[0] = "changed"
	if base[0] != "KEEP=value" {
		t.Fatal("caller environment mutated")
	}
}
func TestProReceiptStorageFailurePrecedesProcessStart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	t.Setenv(receiptstorage.RootEnv, root)
	t.Setenv("CLAWMANAGER_OPENCLAW_TASK_PROTOCOL", "0")
	manager := New(appconfig.Config{OpenClawCommand: []string{"must-not-start"}})
	if manager.Start(context.Background()) == nil {
		t.Fatal("invalid receipt storage started")
	}
	if manager.Snapshot().Status != StatusCrashed || manager.Snapshot().PID != 0 {
		t.Fatal("incorrect failure state")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("startup created an untrusted mount")
	}
}
