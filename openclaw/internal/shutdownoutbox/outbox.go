// Package shutdownoutbox stores diagnostic observations only. It cannot authorize
// cleanup, lifecycle changes, task receipts or a runtime protocol downgrade.
package shutdownoutbox

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

var ErrUnavailable = errors.New("shutdown diagnostic outbox unavailable; retain local evidence")

const Limit = 256

var hexID = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Event struct {
	InstanceID  int    `json:"instance_id"`
	OwnerUserID int    `json:"owner_user_id"`
	Generation  int    `json:"generation"`
	ClaimID     string `json:"claim_id"`
	Outcome     string `json:"outcome"`
}
type Delivery struct {
	ID    string `json:"id"`
	Event Event  `json:"event"`
}
type Ack struct {
	Version        int      `json:"version"`
	DiagnosticOnly bool     `json:"diagnostic_only"`
	AcceptedIDs    []string `json:"accepted_ids"`
}
type envelope struct {
	Delivery Delivery `json:"delivery"`
	MAC      string   `json:"mac"`
}
type Queue struct {
	mu                sync.Mutex
	base, root, scope string
	key               []byte
	next              int
}

func digest(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }
func (e Event) Delivery() Delivery {
	raw, _ := json.Marshal(e)
	return Delivery{ID: digest(raw), Event: e}
}
func (e Event) valid() bool {
	if e.InstanceID <= 0 || e.OwnerUserID <= 0 || e.Generation < 0 || int64(e.Generation) > 2147483647 || !hexID.MatchString(e.ClaimID) {
		return false
	}
	switch e.Outcome {
	case "cleanup_unconfirmed", "cleanup_confirmed_release_unconfirmed", "cleanup_confirmed_release_observed":
		return true
	}
	return false
}
func New(root, key, scope string) *Queue {
	// A different endpoint/credential/instance scope cannot silently consume old
	// files. Rotation requires an explicit migration, not credential guessing.
	if !filepath.IsAbs(root) || filepath.Clean(root) == filepath.VolumeName(root)+string(filepath.Separator) || key == "" || scope == "" {
		return nil
	}
	namespace := digest([]byte(scope + "\x00" + key))
	return &Queue{base: root, root: filepath.Join(root, "shutdown-outbox", namespace), scope: namespace, key: []byte(key)}
}
func (q *Queue) mac(d Delivery) string {
	raw, _ := json.Marshal(d)
	m := hmac.New(sha256.New, q.key)
	m.Write([]byte("shutdown-observation-outbox-v1\x00" + q.scope + "\x00"))
	m.Write(raw)
	return hex.EncodeToString(m.Sum(nil))
}
func safeDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return ErrUnavailable
	}
	// Reject linked ancestors before and after creation; an outbox must live in
	// one manager-owned persistent directory, never a shared writable workspace.
	current := filepath.VolumeName(dir) + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(dir), current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		s, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
			return ErrUnavailable
		}
	}
	return nil
}
func ensurePrivateDirectory(dir string) error {
	if err := safeDir(dir); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return ErrUnavailable
	}
	parent := filepath.Dir(dir)
	if parent == dir || ensurePrivateDirectory(parent) != nil {
		return ErrUnavailable
	}
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return ErrUnavailable
	}
	if safeDir(dir) != nil {
		return ErrUnavailable
	}
	return syncDir(parent)
}
func (q *Queue) maintenanceAbsent() error {
	if q == nil {
		return ErrUnavailable
	}
	_, err := os.Lstat(filepath.Join(filepath.Dir(q.root), ".maintenance-"+q.scope))
	// Any marker, including an incomplete file or link, is uncertainty. Never
	// remove/adopt an operator marker or recreate a moved namespace beneath it.
	if err == nil || !os.IsNotExist(err) {
		return ErrUnavailable
	}
	return nil
}
func (q *Queue) prepare(create bool) error {
	if q.maintenanceAbsent() != nil {
		return ErrUnavailable
	}
	if err := safeDir(q.root); err == nil {
		if create {
			// A previous mkdir may have succeeded before syncing its parent
			// failed. Retry the complete owned directory chain, not just data.
			if q.syncParents() != nil {
				return ErrUnavailable
			}
		}
		return q.maintenanceAbsent()
	} else if !os.IsNotExist(err) || !create {
		return err
	}
	if ensurePrivateDirectory(q.base) != nil {
		return ErrUnavailable
	}
	for _, dir := range []string{filepath.Join(q.base, "shutdown-outbox"), q.root} {
		if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return ErrUnavailable
		}
		if safeDir(dir) != nil || syncDir(filepath.Dir(dir)) != nil {
			return ErrUnavailable
		}
	}
	if q.syncParents() != nil {
		return ErrUnavailable
	}
	return q.maintenanceAbsent()
}
func (q *Queue) syncParents() error {
	// Retry the namespace and its owned parent barriers, even when mkdir
	// already succeeded. Do not fsync unrelated mounts above AgentDataDir.
	for _, dir := range []string{q.root, filepath.Dir(q.root), q.base} {
		if safeDir(dir) != nil || syncDir(dir) != nil {
			return ErrUnavailable
		}
	}
	return nil
}
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (q *Queue) load(id string) (envelope, error) {
	if !hexID.MatchString(id) {
		return envelope{}, ErrUnavailable
	}
	file := filepath.Join(q.root, id+".json")
	before, err := os.Lstat(file)
	if err != nil {
		return envelope{}, err
	}
	if !before.Mode().IsRegular() || before.Size() > 2048 {
		return envelope{}, ErrUnavailable
	}
	f, err := os.Open(file)
	if err != nil {
		return envelope{}, ErrUnavailable
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return envelope{}, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(f, 2049))
	if err != nil || len(raw) > 2048 {
		return envelope{}, ErrUnavailable
	}
	after, err := os.Lstat(file)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return envelope{}, ErrUnavailable
	}
	var v envelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&v) != nil {
		return envelope{}, ErrUnavailable
	}
	var tail any
	if dec.Decode(&tail) != io.EOF || !v.Delivery.Event.valid() || v.Delivery.ID != id || v.Delivery.Event.Delivery().ID != id || !hmac.Equal([]byte(v.MAC), []byte(q.mac(v.Delivery))) {
		return envelope{}, ErrUnavailable
	}
	// Only our immutable producer encoding is accepted. In particular, JSON
	// duplicate keys must not acquire meaning through last-key-wins parsing.
	canonical, err := json.Marshal(v)
	if err != nil || !bytes.Equal(raw, canonical) {
		return envelope{}, ErrUnavailable
	}
	return v, nil
}
func (q *Queue) list() ([]Delivery, error) {
	if err := q.prepare(false); err != nil {
		if os.IsNotExist(err) {
			return []Delivery{}, nil
		}
		return nil, ErrUnavailable
	}
	dir, err := os.Open(q.root)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer dir.Close()
	names, err := dir.Readdirnames(Limit + 1)
	if err != nil && err != io.EOF {
		return nil, ErrUnavailable
	}
	if len(names) > Limit {
		return nil, ErrUnavailable
	}
	sort.Strings(names)
	items := []Delivery{}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			return nil, ErrUnavailable
		}
		v, err := q.load(strings.TrimSuffix(name, ".json"))
		if err != nil {
			return nil, ErrUnavailable
		}
		items = append(items, v.Delivery)
	}
	return items, nil
}
func (q *Queue) resync(id string) error {
	if _, err := q.load(id); err != nil {
		return ErrUnavailable
	}
	file := filepath.Join(q.root, id+".json")
	before, err := os.Lstat(file)
	if err != nil || !before.Mode().IsRegular() {
		return ErrUnavailable
	}
	f, err := os.OpenFile(file, os.O_RDWR, 0)
	if err != nil {
		return ErrUnavailable
	}
	opened, statErr := f.Stat()
	syncErr := f.Sync()
	closeErr := f.Close()
	after, afterErr := os.Lstat(file)
	if statErr != nil || syncErr != nil || closeErr != nil || afterErr != nil || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		return ErrUnavailable
	}
	if _, err := q.load(id); err != nil {
		return ErrUnavailable
	}
	return syncDir(q.root)
}
func (q *Queue) Put(e Event) error {
	if q == nil || !e.valid() {
		return ErrUnavailable
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.prepare(true) != nil {
		return ErrUnavailable
	}
	items, err := q.list()
	if err != nil {
		return ErrUnavailable
	}
	d := e.Delivery()
	for _, old := range items {
		if old.ID == d.ID {
			return q.resync(d.ID)
		}
	}
	if len(items) >= Limit {
		return ErrUnavailable
	}
	raw, _ := json.Marshal(envelope{Delivery: d, MAC: q.mac(d)})
	f, err := os.OpenFile(filepath.Join(q.root, d.ID+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrUnavailable
	}
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	// A partial/uncertain file is retained and blocks the queue; never overwrite
	// it or convert it into an acknowledged observation after a crash.
	if writeErr != nil || syncErr != nil || closeErr != nil || syncDir(q.root) != nil {
		return ErrUnavailable
	}
	return nil
}
func (q *Queue) Pending() ([]Delivery, error) {
	if q == nil {
		return nil, ErrUnavailable
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	items, err := q.list()
	if err != nil {
		return nil, err
	}
	if len(items) <= 32 {
		return items, nil
	}
	batch := make([]Delivery, 32)
	for i := range batch {
		batch[i] = items[(q.next+i)%len(items)]
	}
	q.next = (q.next + 32) % len(items)
	return batch, nil
}
func (q *Queue) Acknowledge(sent []Delivery, ack Ack) error {
	if q == nil || ack.Version != 1 || !ack.DiagnosticOnly || ack.AcceptedIDs == nil || len(sent) > 32 {
		return ErrUnavailable
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	expected := map[string]bool{}
	for _, d := range sent {
		if !d.Event.valid() || d.Event.Delivery().ID != d.ID || expected[d.ID] {
			return ErrUnavailable
		}
		expected[d.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range ack.AcceptedIDs {
		if !expected[id] || seen[id] {
			return ErrUnavailable
		}
		seen[id] = true
	}
	if len(seen) == 0 {
		return nil
	}
	if q.prepare(false) != nil {
		return ErrUnavailable
	}
	// Validate the ENTIRE bounded queue before any deletion, not only ack files.
	if _, err := q.list(); err != nil {
		return err
	}
	for id := range seen {
		if _, err := q.load(id); err != nil && !os.IsNotExist(err) {
			return ErrUnavailable
		}
	}
	for id := range seen {
		if err := os.Remove(filepath.Join(q.root, id+".json")); err != nil && !os.IsNotExist(err) {
			return ErrUnavailable
		}
	}
	if syncDir(q.root) != nil {
		return ErrUnavailable
	}
	return nil
}
