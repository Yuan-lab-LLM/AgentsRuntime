package gateway

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrNativeStorageUncertain = errors.New("native storage outcome is uncertain")

// NativeUpgradeOperation is a fixed native action, never an arbitrary command.
// The lease token belongs only to the trusted parent; Child removes it.
type NativeUpgradeOperation struct {
	OperationID string                  `json:"operation_id"`
	Action      string                  `json:"action"`
	Request     WorkspaceUpgradeRequest `json:"request"`
}

func (o NativeUpgradeOperation) Validate() error {
	id, err := hex.DecodeString(o.OperationID)
	if err != nil || len(id) != 16 || hex.EncodeToString(id) != o.OperationID {
		return fmt.Errorf("invalid native operation identity")
	}
	r := o.Request
	if r.UserID < 1 || r.InstanceID < 1 || r.Generation < 1 || r.UID < 200001 || uint64(r.UID) >= 1<<32-1 || r.GID != r.UID || validateUpgradeID("rollout_id", r.RolloutID) != nil {
		return fmt.Errorf("invalid native storage identity")
	}
	if r.ResumeRolloutID != "" && validateUpgradeID("resume_rollout_id", r.ResumeRolloutID) != nil {
		return fmt.Errorf("invalid resume capsule")
	}
	switch o.Action {
	case "compatibility", "inventory", "migrate", "migration-status", "restore", "restore-status":
		return nil
	}
	return fmt.Errorf("unsupported native storage action")
}
func (o NativeUpgradeOperation) Child() NativeUpgradeOperation { o.Request.LeaseToken = ""; return o }
func (o NativeUpgradeOperation) MutatesWorkspace() bool {
	return o.Action == "migrate" || o.Action == "restore"
}
func (o NativeUpgradeOperation) WritesCapsule() bool {
	return o.MutatesWorkspace() || o.Action == "compatibility"
}

// ScopedNativeUpgradeProvider is implemented by the native adapter. Its caller
// has already mounted the exact capsule and owns the outer container boundary.
type ScopedNativeUpgradeProvider interface {
	RunScopedNativeUpgrade(context.Context, Config, NativeUpgradeOperation, UpgradeHost) (json.RawMessage, error)
}
