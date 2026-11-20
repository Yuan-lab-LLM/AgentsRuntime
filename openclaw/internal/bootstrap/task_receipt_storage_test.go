package bootstrap

import (
	appconfig "github.com/iamlovingit/clawmanager-openclaw-image/internal/config"
	"github.com/iamlovingit/clawmanager-openclaw-image/internal/receiptstorage"
	"testing"
)

func TestProReceiptStorageDisabledDoesNotRequireDropUser(t *testing.T) {
	t.Setenv(receiptstorage.RootEnv, "")
	if err := prepareTaskReceiptStorage(appconfig.Config{}); err != nil {
		t.Fatal(err)
	}
}
