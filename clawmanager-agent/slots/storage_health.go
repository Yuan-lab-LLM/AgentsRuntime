package slots

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

const (
	storageProbeDirectory = ".clawmanager-storage-health"
	storageProbeInterval  = 10 * time.Second
	storageProbeBudget    = 2 * time.Second
	storageProbeFreshness = 2 * storageProbeInterval
)

var errStorageHealthUnavailable = errors.New("workspace storage health is unavailable")

type storageHealthSample struct {
	verified time.Time
	pending  time.Time
}

type slotStorageHealth struct {
	probe  func() error
	sample atomic.Pointer[storageHealthSample]
}

func (h *slotStorageHealth) health() error {
	sample := h.sample.Load()
	if sample == nil || time.Since(sample.verified) >= storageProbeFreshness ||
		(!sample.pending.IsZero() && time.Since(sample.pending) >= storageProbeBudget) {
		return errStorageHealthUnavailable
	}
	return nil
}

// A hard-mounted NFS syscall may outlive any request deadline. This loop
// never starts another probe while the current probe is still in the kernel.
func (h *slotStorageHealth) run(ctx context.Context, interval time.Duration) {
	defer h.sample.Store(nil)
	var verified time.Time
	for ctx.Err() == nil {
		start := time.Now()
		h.sample.Store(&storageHealthSample{verified: verified, pending: start})
		err := h.probe()
		verified = time.Time{}
		if err == nil && time.Since(start) < storageProbeBudget {
			verified = time.Now()
		}
		h.sample.Store(&storageHealthSample{verified: verified})
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

func probeSlotStorage(root string) error {
	probe, err := workspaceio.OpenAt(root, filepath.Join(root, storageProbeDirectory))
	if err != nil {
		return err
	}
	defer probe.Close()
	token, err := newExecutionToken()
	if err != nil {
		return err
	}
	file, err := probe.OpenFile(token, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	return errors.Join(file.Close(), probe.Root.Remove(token))
}
