package slots

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type book struct {
	Assignment  *Assignment `json:"assignment,omitempty"`
	Digest      string      `json:"digest,omitempty"`
	path        string
	quarantined bool
}

func openBook(dir string) (*book, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	b := &book{path: filepath.Join(dir, "slot-assignment.json")}
	data, err := os.ReadFile(b.path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, b); err != nil {
		return nil, fmt.Errorf("invalid slot assignment: %w", err)
	}
	b.quarantined = b.Assignment != nil && b.Assignment.State != "stopped"
	return b, nil
}

func (b *book) begin(req gateway.CreateGatewayRequest) error {
	if b.quarantined {
		return errors.New("slot writer state requires reconciliation")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	id := fmt.Sprintf("gw-%d-%d", req.InstanceID, req.Generation)
	if a := b.Assignment; a != nil {
		if a.GatewayID == id && a.State != "stopped" && b.Digest == digest {
			return nil
		}
		if a.State != "stopped" {
			return errors.New("previous slot writer has not stopped")
		}
		if a.InstanceID == req.InstanceID && req.Generation <= a.Generation {
			return errors.New("stale generation")
		}
	}
	previous, previousDigest := b.Assignment, b.Digest
	b.Assignment = &Assignment{GatewayID: id, InstanceID: req.InstanceID, UserID: req.UserID, Generation: req.Generation, State: "starting"}
	b.Digest = digest
	if err = b.save(); err != nil {
		b.Assignment, b.Digest = previous, previousDigest
		return err
	}
	return nil
}

func (b *book) stopped(id string) error {
	if b.quarantined || b.Assignment == nil || b.Assignment.GatewayID != id {
		return errors.New("slot cannot confirm this writer stopped")
	}
	previous := b.Assignment.State
	b.Assignment.State = "stopped"
	if err := b.save(); err != nil {
		b.Assignment.State = previous
		return err
	}
	return nil
}

func (b *book) save() error { return atomicJSON(b.path, b) }
