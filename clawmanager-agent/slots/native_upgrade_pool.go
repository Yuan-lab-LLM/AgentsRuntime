package slots

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

type nativeUpgradeRecord struct {
	Version    int                            `json:"version"`
	Operation  gateway.NativeUpgradeOperation `json:"operation"`
	LeaseToken string                         `json:"lease_token,omitempty"`
	Digest     string                         `json:"digest"`
	PodUID     string                         `json:"pod_uid"`
	Slot       int                            `json:"slot"`
	Phase      string                         `json:"phase"`
	Launched   bool                           `json:"launched"`
	Previous   *writerRecord                  `json:"previous,omitempty"`
	Intent     *containerStopIntent           `json:"intent,omitempty"`
	Proof      *containerTerminationProof     `json:"proof,omitempty"`
	Result     json.RawMessage                `json:"result,omitempty"`
	Error      string                         `json:"error,omitempty"`
}
type nativeUpgradeSession struct {
	record      *nativeUpgradeRecord
	path        string
	ctx         context.Context
	cancel      context.CancelFunc
	unlock      func()
	finishLease func() error
	writer      *os.File
	writerPath  string
	callback    net.Listener
}

func nativeOperationDigest(op gateway.NativeUpgradeOperation) (string, error) {
	raw, err := json.Marshal(op.Child())
	if err != nil {
		return "", err
	}
	raw, err = canonicalNativeOperation(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (p *Pool) nativeRecordPath(id string) (string, error) {
	if !executionTokenValid(id) {
		return "", fmt.Errorf("invalid native operation")
	}
	dir := filepath.Join(p.cfg.WorkspaceRoot, ".clawmanager-upgrades", "maintenance")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := trustedDirectory(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "operation-"+id+".json")
	if err := trustedFile(path); err != nil {
		return "", err
	}
	return path, nil
}
func (s *nativeUpgradeSession) save() error { return atomicJSON(s.path, s.record) }
func (p *Pool) nativeSession(ctx context.Context, op gateway.NativeUpgradeOperation, finalizedOnly bool) (*nativeUpgradeSession, error) {
	if s := p.maintenance[op.OperationID]; s != nil {
		digest, _ := nativeOperationDigest(op)
		if s.record.Digest != digest || s.record.LeaseToken != op.Request.LeaseToken {
			return nil, fmt.Errorf("native operation changed")
		}
		if finalizedOnly {
			if err := p.validateFinalizedNativeRecord(s.record); err != nil {
				return nil, err
			}
		}
		return s, nil
	}
	if os.Geteuid() != 10001 {
		return nil, fmt.Errorf("native coordinator requires reserved management identity")
	}
	path, err := p.nativeRecordPath(op.OperationID)
	if err != nil {
		return nil, err
	}
	digest, err := nativeOperationDigest(op)
	if err != nil {
		return nil, err
	}
	record := &nativeUpgradeRecord{Version: 1, Operation: op.Child(), LeaseToken: op.Request.LeaseToken, Digest: digest, PodUID: p.cfg.PodUID, Slot: -1, Phase: "reserved"}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) > 4<<20 || json.Unmarshal(data, record) != nil || record.Version != 1 || record.Digest != digest || record.PodUID != p.cfg.PodUID || record.LeaseToken != op.Request.LeaseToken {
			return nil, fmt.Errorf("native parent record changed")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else if finalizedOnly {
		return nil, fmt.Errorf("native finalized parent record is missing")
	}
	storedDigest, digestErr := nativeOperationDigest(record.Operation)
	if digestErr != nil || record.Operation.Validate() != nil || storedDigest != record.Digest || record.Operation.OperationID != op.OperationID {
		return nil, fmt.Errorf("native parent record identity is invalid")
	}
	if finalizedOnly {
		if err := p.validateFinalizedNativeRecord(record); err != nil {
			return nil, err
		}
	}
	scope, cancel := context.WithCancel(context.Background())
	session := &nativeUpgradeSession{record: record, path: path, ctx: scope, cancel: cancel}
	if record.Phase == "finished" || record.Phase == "released" {
		p.maintenance[op.OperationID] = session
		return session, nil
	}
	unlock, err := p.metadata.LockUpgradeOperation(op.Request.InstanceID)
	if err != nil {
		cancel()
		return nil, err
	}
	session.unlock = unlock
	abandon := func(err error) (*nativeUpgradeSession, error) { session.closeParent(false); return nil, err }
	if op.MutatesWorkspace() {
		leaseCtx, finish, leaseErr := p.metadata.KeepUpgradeWriterLease(scope, op.Request)
		if leaseErr != nil && record.Launched { // Cleanup proof must remain recoverable even after lease expiry.
			cancel()
			scope, session.cancel = context.WithCancel(context.Background())
			session.ctx = scope
		} else if leaseErr != nil {
			return abandon(leaseErr)
		} else {
			session.ctx = leaseCtx
			session.finishLease = finish
		}
		if err = session.acquireWriter(p.cfg); err != nil {
			return abandon(err)
		}
	}
	if err = session.save(); err != nil {
		return abandon(err)
	}
	if record.Phase != "proven" {
		if err = p.openNativeCallback(session, op); err != nil {
			return abandon(err)
		}
	}
	p.maintenance[op.OperationID] = session
	return session, nil
}
func (s *nativeUpgradeSession) acquireWriter(cfg gateway.Config) error {
	r := s.record.Operation.Request
	dir := filepath.Join(cfg.WorkspaceRoot, ".clawmanager-slot-locks")
	if err := trustedDirectory(dir); err != nil {
		return err
	}
	base := filepath.Join(dir, fmt.Sprintf("%s-%d-%d", cfg.RuntimeType, r.UserID, r.InstanceID))
	if err := trustedFile(base + ".lock"); err != nil {
		return err
	}
	file, err := os.OpenFile(base+".lock", os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = lockWorkspace(file); err != nil {
		file.Close()
		return err
	}
	current, err := readWriter(base + ".json")
	if err != nil {
		file.Close()
		return err
	}
	if current.InstanceID != r.InstanceID || current.UserID != r.UserID || current.Generation != r.Generation || current.RuntimeType != cfg.RuntimeType {
		file.Close()
		return fmt.Errorf("native stopped writer identity changed")
	}
	holder := "maintenance-" + s.record.Operation.OperationID
	if current.State == "stopped" {
		if s.record.Previous == nil {
			copy := *current
			s.record.Previous = &copy
			if err = s.save(); err != nil {
				file.Close()
				return err
			}
		}
		active := *current
		active.State = "upgrading"
		active.PodUID = cfg.PodUID
		active.Slot = holder
		if err = atomicJSON(base+".json", active); err != nil {
			file.Close()
			return err
		}
	} else if current.State != "upgrading" || current.Slot != holder || current.PodUID != cfg.PodUID || s.record.Previous == nil {
		file.Close()
		return fmt.Errorf("native source writer stop is not confirmed")
	}
	s.writer, s.writerPath = file, base+".json"
	return nil
}
func (s *nativeUpgradeSession) restoreWriter() error {
	if s.writer == nil {
		return nil
	}
	current, err := readWriter(s.writerPath)
	if err != nil {
		return err
	}
	previous := s.record.Previous
	if previous == nil {
		return fmt.Errorf("native previous writer missing")
	}
	holder := "maintenance-" + s.record.Operation.OperationID
	if current.State == "upgrading" && current.Slot == holder && current.InstanceID == previous.InstanceID && current.UserID == previous.UserID && current.Generation == previous.Generation && current.RuntimeType == previous.RuntimeType && current.PodUID == s.record.PodUID {
		if err = atomicJSON(s.writerPath, previous); err != nil {
			return err
		}
	} else if *current != *previous {
		return fmt.Errorf("native writer changed before finalization")
	}
	if err = s.writer.Close(); err != nil {
		return err
	}
	s.writer = nil
	return nil
}
func (s *nativeUpgradeSession) closeParent(restore bool) error {
	if restore {
		if err := s.restoreWriter(); err != nil {
			return err
		}
	} else if s.writer != nil {
		s.writer.Close()
		s.writer = nil
	}
	if s.callback != nil {
		s.callback.Close()
		s.callback = nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.finishLease != nil {
		// Completion consumes a one-shot renewal result, including on error.
		finish := s.finishLease
		s.finishLease = nil
		if err := finish(); err != nil {
			return err
		}
	}
	if s.unlock != nil {
		s.unlock()
		s.unlock = nil
	}
	return nil
}
func (p *Pool) openNativeCallback(s *nativeUpgradeSession, op gateway.NativeUpgradeOperation) error {
	path, err := nativeUpgradeCallbackPath(op.OperationID)
	if err != nil {
		return err
	}
	if err = trustedDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 || !trustedOwner(info) || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("untrusted native callback socket")
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		return err
	}
	s.callback = listener
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(2 * time.Second))
				var request [3]byte
				allowed := false
				if _, err := io.ReadFull(conn, request[:]); err == nil && string(request[:]) == "V1\n" && s.ctx.Err() == nil {
					allowed = !op.MutatesWorkspace() || p.metadata.VerifyUpgradeWriter(s.ctx, op.Request) == nil
				}
				if allowed {
					conn.Write([]byte("OK\n"))
				} else {
					conn.Write([]byte("NO\n"))
				}
			}()
		}
	}()
	return nil
}
func (p *Pool) startNativeMaintenance(ctx context.Context, op gateway.NativeUpgradeOperation) (nativeMaintenancePending, error) {
	var empty nativeMaintenancePending
	if err := op.Validate(); err != nil {
		return empty, err
	}
	p.proxyMu.Lock()
	defer p.proxyMu.Unlock()
	p.maintenanceMu.Lock()
	defer p.maintenanceMu.Unlock()
	if p.maintenance == nil {
		p.maintenance = make(map[string]*nativeUpgradeSession)
	}
	snapshots, _ := p.snapshots(ctx)
	selected := -1
	for i, snapshot := range snapshots {
		if snapshot.MaintenanceOperationID == op.OperationID {
			selected = i
			break
		}
	}
	if selected < 0 {
		for i, snapshot := range snapshots {
			if !snapshot.Quarantined && len(snapshot.Gateways) == 0 && (snapshot.Assignment == nil || snapshot.Assignment.State == "stopped") {
				selected = i
				break
			}
		}
	}
	if selected < 0 {
		return empty, fmt.Errorf("native maintenance requires an idle slot")
	}
	if err := workspaceio.PrepareNFSManagementRoots(p.cfg.WorkspaceRoot); err != nil {
		return empty, err
	}
	session, err := p.nativeSession(ctx, op, false)
	if err != nil {
		return empty, err
	}
	record := session.record
	if record.Intent != nil {
		return nativeMaintenancePending{Pending: true, Intent: *record.Intent}, nil
	}
	if record.Launched && record.Slot != selected {
		return empty, fmt.Errorf("old native maintenance slot is unavailable")
	}
	record.Slot = selected
	if !record.Launched {
		if err = prepareNativeUpgradeCapsules(p.cfg, op); err != nil {
			session.closeParent(true)
			delete(p.maintenance, op.OperationID)
			return empty, err
		}
		record.Launched = true
		if err = session.save(); err != nil {
			session.closeParent(true)
			delete(p.maintenance, op.OperationID)
			return empty, err
		}
	}
	body, _ := json.Marshal(nativeUpgradeWorkerRequest{Operation: op.Child()})
	status, data, callErr := p.call(ctx, selected, http.MethodPost, "/v1/native-upgrade", body)
	if callErr != nil {
		return empty, fmt.Errorf("native maintenance reservation is uncertain; retry the same operation")
	}
	var pending nativeMaintenancePending
	if status != 202 || json.Unmarshal(data, &pending) != nil || !pending.Pending {
		return empty, fmt.Errorf("native maintenance reservation is not confirmed")
	}
	intent := pending.Intent
	if intent.Kind != "maintenance" || intent.OperationID != op.OperationID || intent.PodUID != p.cfg.PodUID || intent.Slot != "slot-"+strconv.Itoa(selected) || intent.InstanceID != op.Request.InstanceID || intent.UserID != op.Request.UserID || intent.Generation != op.Request.Generation || intent.AssignmentDigest != record.Digest || !executionTokenValid(intent.WorkerEpoch) {
		return empty, fmt.Errorf("native worker intent changed")
	}
	record.Intent = &intent
	record.Phase = "held"
	if err = session.save(); err != nil {
		return empty, err
	}
	return pending, nil
}

// Resume only an authenticated finalized record. Unlike start, this must never
// reserve a slot, acquire a writer or recreate a missing operation.
func (p *Pool) resumeNativeMaintenance(ctx context.Context, op gateway.NativeUpgradeOperation) (nativeMaintenancePending, error) {
	p.maintenanceMu.Lock()
	defer p.maintenanceMu.Unlock()
	if p.maintenance == nil {
		p.maintenance = make(map[string]*nativeUpgradeSession)
	}
	session, err := p.nativeSession(ctx, op, true)
	if err != nil {
		return nativeMaintenancePending{}, err
	}
	return nativeMaintenancePending{Pending: true, Intent: *session.record.Intent}, nil
}

func (p *Pool) validateFinalizedNativeRecord(record *nativeUpgradeRecord) error {
	if (record.Phase != "finished" && record.Phase != "released") || !record.Launched || record.Slot < 0 || record.Slot >= p.count || record.Intent == nil || record.Proof == nil {
		return fmt.Errorf("native parent record is not finalized")
	}
	op, intent, proof := record.Operation, record.Intent, record.Proof
	if intent.Version != 1 || intent.Kind != "maintenance" || intent.Graceful || intent.OperationID != op.OperationID || intent.PodUID != record.PodUID || intent.Slot != "slot-"+strconv.Itoa(record.Slot) || intent.InstanceID != op.Request.InstanceID || intent.UserID != op.Request.UserID || intent.Generation != op.Request.Generation || intent.GatewayID != fmt.Sprintf("gw-%d-%d", op.Request.InstanceID, op.Request.Generation) || intent.AssignmentDigest != record.Digest || !executionTokenValid(intent.WorkerEpoch) || proof.containerStopIntent != *intent || !proof.CleanupConfirmed || proof.ContainerID == "" || proof.FinishedAt.IsZero() || len(record.Result) > 1<<20 || len(record.Error) > 4096 || len(record.Result) > 0 && !json.Valid(record.Result) {
		return fmt.Errorf("native finalized parent identity changed")
	}
	return nil
}

func (p *Pool) finishNativeMaintenance(ctx context.Context, proof containerTerminationProof) (json.RawMessage, error) {
	p.maintenanceMu.Lock()
	defer p.maintenanceMu.Unlock()
	if proof.Kind != "maintenance" || !proof.CleanupConfirmed || proof.ContainerID == "" || proof.FinishedAt.IsZero() {
		return nil, fmt.Errorf("native termination proof incomplete")
	}
	session := p.maintenance[proof.OperationID]
	if session == nil {
		return nil, fmt.Errorf("native parent session requires the original authenticated operation to resume")
	}
	record := session.record
	if record.Intent == nil || *record.Intent != proof.containerStopIntent {
		return nil, fmt.Errorf("native termination identity changed")
	}
	if record.Phase == "finished" || record.Phase == "released" {
		if record.Proof == nil || *record.Proof != proof {
			return nil, fmt.Errorf("native termination proof changed")
		}
		if err := session.closeParent(false); err != nil {
			return nil, err
		}
		return json.Marshal(nativeMaintenanceResult{Intent: *record.Intent, Result: record.Result, Error: record.Error, Confirmed: true})
	}
	if record.Phase == "uncertain" {
		return nil, fmt.Errorf("%w; writer remains fenced", gateway.ErrNativeStorageUncertain)
	}
	var result nativeMaintenanceResult
	var err error
	if record.Phase == "proven" {
		if record.Proof == nil || *record.Proof != proof {
			return nil, fmt.Errorf("native termination proof changed")
		}
		result = nativeMaintenanceResult{Intent: *record.Intent, Result: record.Result, Error: record.Error, Confirmed: true}
	} else {
		body, _ := json.Marshal(proof)
		status, data, err := p.call(ctx, record.Slot, http.MethodPost, "/v1/native-maintenance/reconcile", body)
		if err != nil || status != 200 {
			return nil, fmt.Errorf("native worker termination reconciliation unavailable")
		}
		if json.Unmarshal(data, &result) != nil || !result.Confirmed || result.Intent != *record.Intent || len(result.Result) > 1<<20 || len(result.Error) > 4096 {
			return nil, fmt.Errorf("native worker result identity changed")
		}
		record.Proof = &proof
		record.Result = bytes.Clone(result.Result)
		record.Error = result.Error
		record.Phase = "proven"
		if result.Uncertain {
			record.Phase = "uncertain"
			session.save()
			return nil, fmt.Errorf("%w; writer remains fenced", gateway.ErrNativeStorageUncertain)
		}
		if err = session.save(); err != nil {
			return nil, err
		}
	}
	if err = session.restoreWriter(); err != nil {
		return nil, err
	}
	if err = session.closeParent(false); err != nil {
		return nil, err
	}
	record.Phase = "finished"
	if err = session.save(); err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
func (p *Pool) releaseNativeMaintenance(ctx context.Context, proof containerTerminationProof) error {
	p.maintenanceMu.Lock()
	defer p.maintenanceMu.Unlock()
	session := p.maintenance[proof.OperationID]
	if session == nil || session.record.Proof == nil || *session.record.Proof != proof || (session.record.Phase != "finished" && session.record.Phase != "released") {
		return fmt.Errorf("native operation is not finalized")
	}
	if err := session.closeParent(false); err != nil {
		return err
	}
	if session.record.Phase == "released" {
		return nil
	}
	body, _ := json.Marshal(proof)
	status, _, err := p.call(ctx, session.record.Slot, http.MethodPost, "/v1/release-stop", body)
	if err != nil || status != 200 {
		return fmt.Errorf("native slot release is not confirmed")
	}
	session.record.Phase = "released"
	return session.save()
}
func (p *Pool) serveNativeUpgrades(out http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/v1/native-upgrades/") {
		return false
	}
	if p.cfg.SandboxProfile != "nfs-rootless-v1" {
		http.NotFound(out, r)
		return true
	}
	if r.Method != http.MethodPost {
		out.WriteHeader(405)
		return true
	}
	if r.URL.Path == "/v1/native-upgrades/start" || r.URL.Path == "/v1/native-upgrades/resume" {
		var op gateway.NativeUpgradeOperation
		if decodeNativeWorkerRequest(r, &op) != nil || op.Validate() != nil {
			http.Error(out, "invalid native operation", 400)
			return true
		}
		var pending nativeMaintenancePending
		var err error
		if r.URL.Path == "/v1/native-upgrades/resume" {
			pending, err = p.resumeNativeMaintenance(r.Context(), op)
		} else {
			pending, err = p.startNativeMaintenance(r.Context(), op)
		}
		if err != nil {
			http.Error(out, err.Error(), 409)
			return true
		}
		writeJSON(out, 202, pending)
		return true
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/native-upgrades/"), "/")
	if len(parts) != 2 || !executionTokenValid(parts[0]) {
		http.NotFound(out, r)
		return true
	}
	var proof containerTerminationProof
	if decodeContainerOperation(r, &proof) != nil || proof.OperationID != parts[0] {
		http.Error(out, "invalid native proof", 400)
		return true
	}
	switch parts[1] {
	case "finish":
		data, err := p.finishNativeMaintenance(r.Context(), proof)
		if err != nil {
			http.Error(out, err.Error(), 409)
		} else {
			out.Header().Set("Content-Type", "application/json")
			out.WriteHeader(200)
			out.Write(data)
		}
	case "release":
		if err := p.releaseNativeMaintenance(r.Context(), proof); err != nil {
			http.Error(out, err.Error(), 409)
		} else {
			writeJSON(out, 200, map[string]any{"operation_id": proof.OperationID, "released": true})
		}
	default:
		http.NotFound(out, r)
	}
	return true
}
