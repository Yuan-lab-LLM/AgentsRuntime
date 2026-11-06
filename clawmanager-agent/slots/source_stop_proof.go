package slots

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type sourceWriterProofRequest struct {
	InstanceID  int    `json:"instance_id"`
	UserID      int    `json:"user_id"`
	Generation  int    `json:"generation"`
	RuntimeType string `json:"runtime_type"`
	PodUID      string `json:"pod_uid"`
	Slot        string `json:"slot"`
	Nonce       string `json:"nonce"`
}

func (p *Pool) serveSourceStoppedWriterProof(out http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		http.Error(out, "source writer proof requires TLS", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		out.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req sourceWriterProofRequest
	data, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err == nil {
		err = decodeSourceWriterJSON(data, "nonce", &req)
	}
	index, indexErr := slotIndex(strings.TrimPrefix(req.Slot, "slot-"))
	nonce, nonceErr := hex.DecodeString(req.Nonce)
	if err != nil || req.InstanceID < 1 || req.UserID < 1 || req.Generation < 1 || req.RuntimeType == "" || req.RuntimeType != p.cfg.RuntimeType || filepath.Base(req.RuntimeType) != req.RuntimeType || req.RuntimeType == "." || req.RuntimeType == ".." || req.PodUID == "" || len(req.PodUID) > 128 || strings.TrimSpace(req.PodUID) != req.PodUID || req.PodUID == p.cfg.PodUID || indexErr != nil || req.Slot != fmt.Sprintf("slot-%d", index) || nonceErr != nil || len(nonce) != 32 || req.Nonce != hex.EncodeToString(nonce) {
		http.Error(out, "invalid source writer scope", http.StatusBadRequest)
		return
	}
	digest, err := p.sourceStoppedWriterDigest(req)
	if err != nil {
		http.Error(out, "source writer stop is not confirmed", http.StatusConflict)
		return
	}
	out.Header().Set("Cache-Control", "no-store")
	writeJSON(out, http.StatusOK, struct {
		sourceWriterProofRequest
		Confirmed          bool   `json:"confirmed"`
		ObserverPodUID     string `json:"observer_pod_uid"`
		SourceRecordDigest string `json:"source_record_digest"`
	}{req, true, p.cfg.PodUID, digest})
}

// Both flat wire objects have six shared identity fields and one exact final
// field. Reject duplicate and noncanonical keys as well as unknown/trailing JSON.
func decodeSourceWriterJSON(data []byte, finalKey string, out any) error {
	if len(data) > 4096 {
		return errors.New("oversized source writer object")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("invalid source writer object")
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return errors.New("invalid source writer key")
		}
		switch key {
		case "instance_id", "user_id", "generation", "runtime_type", "pod_uid", "slot":
		default:
			if key != finalKey {
				return errors.New("unknown source writer key")
			}
		}
		seen[key] = true
		if err = d.Decode(new(json.RawMessage)); err != nil {
			return err
		}
	}
	if _, err = d.Token(); err != nil {
		return err
	}
	if len(seen) != 7 || d.Decode(new(any)) != io.EOF {
		return errors.New("incomplete or trailing source writer object")
	}
	return json.Unmarshal(data, out)
}

// Existing files only: proving a stop never initializes storage, claims a
// writer, changes a state, or consults the observer's business assignment.
func (p *Pool) sourceStoppedWriterDigest(req sourceWriterProofRequest) (string, error) {
	root := p.cfg.WorkspaceRoot
	if p.cfg.PodUID == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("invalid observer storage identity")
	}
	dir := filepath.Join(root, ".clawmanager-slot-locks")
	dirs := map[string]os.FileInfo{}
	for _, path := range []string{root, dir} {
		if err := trustedDirectory(path); err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		dirs[path] = info
	}
	base := filepath.Join(dir, fmt.Sprintf("%s-%d-%d", req.RuntimeType, req.UserID, req.InstanceID))
	// NFS implements exclusive flock through a write lock; opening this
	// existing sibling read/write changes no bytes and never creates it.
	lock, lockInfo, err := openSourceProofFile(base+".lock", os.O_RDWR)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err = lockWorkspace(lock); err != nil {
		return "", err
	}
	record, recordInfo, err := openSourceProofFile(base+".json", os.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer record.Close()
	data, err := io.ReadAll(io.LimitReader(record, 4097))
	if err != nil {
		return "", err
	}
	var stored writerRecord
	if err = decodeSourceWriterJSON(data, "state", &stored); err != nil {
		return "", err
	}
	if stored.State != "stopped" || stored.InstanceID != req.InstanceID || stored.UserID != req.UserID || stored.Generation != req.Generation || stored.RuntimeType != req.RuntimeType || stored.PodUID != req.PodUID || stored.Slot != req.Slot {
		return "", errors.New("source writer scope or state changed")
	}
	if _, err = record.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	again, err := io.ReadAll(io.LimitReader(record, 4097))
	if err != nil || !bytes.Equal(data, again) {
		return "", errors.New("source writer bytes changed")
	}
	for path, before := range dirs {
		if err = trustedDirectory(path); err != nil {
			return "", err
		}
		after, statErr := os.Stat(path)
		if statErr != nil || !os.SameFile(before, after) {
			return "", errors.New("source writer directory changed")
		}
	}
	if err = sourceProofFileUnchanged(base+".lock", lock, lockInfo); err != nil {
		return "", err
	}
	if err = sourceProofFileUnchanged(base+".json", record, recordInfo); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func openSourceProofFile(path string, flag int) (*os.File, os.FileInfo, error) {
	if err := trustedFile(path); err != nil {
		return nil, nil, err
	}
	// NOFOLLOW closes the final-component race; NONBLOCK avoids waiting on an
	// unsafe FIFO substituted between the trusted check and open.
	file, err := os.OpenFile(path, flag|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err == nil {
		err = sourceProofFileUnchanged(path, file, info)
	}
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func sourceProofFileUnchanged(path string, file *os.File, before os.FileInfo) error {
	if err := trustedFile(path); err != nil {
		return err
	}
	named, err := os.Lstat(path)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !trustedOwner(opened) || opened.Mode().Perm()&0022 != 0 || !os.SameFile(before, opened) || !os.SameFile(opened, named) || before.Size() != opened.Size() || before.Mode() != opened.Mode() || !before.ModTime().Equal(opened.ModTime()) {
		return errors.New("source writer file changed")
	}
	return nil
}
