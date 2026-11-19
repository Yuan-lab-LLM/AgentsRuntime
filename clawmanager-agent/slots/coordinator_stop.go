package slots

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

// The supervisor in runWorker owns this same guard before opening its book and
// until all serving/shutdown has ended. Only an offline coordinator may open a
// second book. Never cache it: a replacement must read our durable result, and
// we must read any intervening assignment before checking a retried proof.
func (p *Pool) serveOfflineContainerFinalization(out http.ResponseWriter, r *http.Request, index int, action string, body []byte) bool {
	if p.slotStateRoot == "" || action != "reconcile-stop" && action != "release-stop" {
		return false
	}
	slot := fmt.Sprintf("slot-%d", index)
	dir := filepath.Join(p.slotStateRoot, slot)
	guardPath := filepath.Join(p.slotSocketDir, slot+".sock.guard")
	// Kubelet creates the parent of the nested state mounts as root:0755.
	// It is only a traversal ancestor, never the mutable slot state itself.
	root, rootErr := filepath.EvalSymlinks(p.slotStateRoot)
	info, statErr := os.Lstat(p.slotStateRoot)
	if rootErr != nil || statErr != nil || root != p.slotStateRoot || !info.IsDir() || info.Mode().Perm()&0022 != 0 || !trustedOwner(info) && !rootOwned(info) {
		http.Error(out, "offline slot state root is unsafe", 503)
		return true
	}
	for _, path := range []string{dir, p.slotSocketDir} {
		if err := trustedDirectory(path); err != nil {
			http.Error(out, "offline slot state is unavailable", 503)
			return true
		}
	}
	if err := trustedFile(guardPath); err != nil {
		http.Error(out, "slot ownership guard is unsafe", 503)
		return true
	}
	guard, err := os.OpenFile(guardPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		http.Error(out, "slot ownership is unavailable", 503)
		return true
	}
	defer guard.Close()
	if err = lockWorkspace(guard); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return false // A live replacement handles its own in-memory book.
		}
		http.Error(out, "slot ownership is unavailable", 503)
		return true
	}
	if err = trustedFile(filepath.Join(dir, "slot-assignment.json")); err != nil {
		http.Error(out, "offline slot assignment is unsafe", 503)
		return true
	}
	b, err := openBook(dir)
	if err != nil {
		http.Error(out, "offline slot assignment is unavailable", 503)
		return true
	}
	cfg := p.cfg
	cfg.AgentDataDir = dir
	// Do not initialize a gateway manager or recover/start any execution here.
	// The existing handlers still require the exact old intent/proof and the
	// independent workspace writer lock. Empty epoch cannot equal a valid old
	// execution token; CM has already verified its kubelet termination receipt.
	w := &Worker{cfg: cfg, book: b, slotName: slot}
	request := r.Clone(r.Context())
	request.Body = io.NopCloser(bytes.NewReader(body))
	if action == "reconcile-stop" {
		w.serveContainerReconcile(out, request)
	} else {
		w.serveContainerRelease(out, request)
	}
	return true
}
