package slots

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type writerRecord struct {
	InstanceID  int    `json:"instance_id"`
	UserID      int    `json:"user_id"`
	Generation  int    `json:"generation"`
	RuntimeType string `json:"runtime_type"`
	PodUID      string `json:"pod_uid"`
	Slot        string `json:"slot"`
	State       string `json:"state"`
}

// Callers hold the stable sibling lock file across read/compare/rename and for
// the entire writer lifetime. The record is outside every user-visible mount.
func claimWriter(path string, req gateway.CreateGatewayRequest, slot string) error {
	if req.InstanceID < 1 || req.UserID < 1 || req.Generation < 1 || req.PodUID == "" || slot == "" {
		return fmt.Errorf("writer identity is incomplete")
	}
	previous, err := readWriter(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if previous != nil {
		if previous.InstanceID != req.InstanceID || previous.UserID != req.UserID || previous.RuntimeType != req.AgentType {
			return fmt.Errorf("writer identity changed")
		}
		if req.Generation <= previous.Generation {
			return fmt.Errorf("writer generation is not newer")
		}
		if previous.State != "stopped" {
			return fmt.Errorf("previous writer stop is unconfirmed")
		}
	}
	return atomicJSON(path, writerRecord{InstanceID: req.InstanceID, UserID: req.UserID, Generation: req.Generation, RuntimeType: req.AgentType, PodUID: req.PodUID, Slot: slot, State: "running"})
}

func stopWriter(path string, req gateway.CreateGatewayRequest, slot string) error {
	previous, err := readWriter(path)
	if err != nil {
		return err
	}
	if previous.InstanceID != req.InstanceID || previous.UserID != req.UserID || previous.Generation != req.Generation || previous.RuntimeType != req.AgentType || previous.PodUID != req.PodUID || previous.Slot != slot {
		return fmt.Errorf("writer stop identity changed")
	}
	previous.State = "stopped"
	return atomicJSON(path, previous)
}

func readWriter(path string) (*writerRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var record writerRecord
	if err = json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	if record.InstanceID < 1 || record.UserID < 1 || record.Generation < 1 || record.PodUID == "" || record.Slot == "" || (record.State != "running" && record.State != "stopped" && record.State != "upgrading") {
		return nil, fmt.Errorf("invalid writer record")
	}
	return &record, nil
}

func atomicJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".assignment-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
