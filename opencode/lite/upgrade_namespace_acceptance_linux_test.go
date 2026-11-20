//go:build linux

package opencode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/sandbox"
	"github.com/iamlovingit/clawmanager-agent/slots"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "opencode-namespace-payload-proof" && os.Getenv("CM_TEST_OPENCODE_NAMESPACE_DISPOSABLE") == "1" {
		runNamespacePayloadProbe()
		return
	}
	if handled, err := gateway.RunNativeStorageHelper(namespaceAcceptanceProfile{NewProfile("opencode")}, os.Args[1:], os.Stdin, os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	os.Exit(m.Run())
}

// Diagnostics run after the real provider, with the production nondumpable
// helper and seccomp still in force. No mapping or validation is mocked.
type namespaceAcceptanceProfile struct{ Profile }

func (p namespaceAcceptanceProfile) RunScopedNativeUpgrade(ctx context.Context, cfg gateway.Config, op gateway.NativeUpgradeOperation, host gateway.UpgradeHost) (json.RawMessage, error) {
	actualHost := host
	if strings.HasPrefix(op.Request.SnapshotID, "fault-") {
		proof := host.(interface {
			NativeUpgradeProof() *gateway.NativeUpgradeParentProof
		}).NativeUpgradeProof()
		switch op.Request.SnapshotID {
		case "fault-missing":
			proof = nil
		case "fault-inode":
			proof.Roots[0].Inode++
		case "fault-path":
			proof.Roots[0].Path += "-other"
		case "fault-owner":
			proof.Roots[0].OwnerUID = 12345
		case "fault-gid":
			proof.Roots[0].OwnerGID++
		case "fault-actor":
			proof.ActorUID++
		case "fault-extra":
			proof.Roots = append(proof.Roots, proof.Roots[0])
		}
		actualHost = namespaceProofHost{UpgradeHost: host, proof: proof}
	}
	result, err := p.Profile.RunScopedNativeUpgrade(ctx, cfg, op, actualHost)
	if err != nil {
		u, ue := os.ReadFile("/proc/self/uid_map")
		g, ge := os.ReadFile("/proc/self/gid_map")
		roots := map[string]any{}
		for _, path := range []string{filepath.Join(cfg.WorkspaceRoot, "opencode", fmt.Sprintf("user-%d", op.Request.UserID), fmt.Sprintf("instance-%d", op.Request.InstanceID)), filepath.Join(cfg.WorkspaceRoot, ".clawmanager-upgrades", "rollout-"+op.Request.RolloutID, fmt.Sprintf("instance-%d", op.Request.InstanceID))} {
			if info, e := os.Lstat(path); e == nil {
				st := info.Sys().(*syscall.Stat_t)
				roots[path] = map[string]any{"dev": st.Dev, "ino": st.Ino, "uid": st.Uid, "gid": st.Gid, "mode": info.Mode().String()}
			}
		}
		rawRoots, _ := json.Marshal(roots)
		time.Sleep(time.Second) // Lets the disposable-Pod host auditor pin this same helper PID.
		return result, fmt.Errorf("%w; pid=%d euid=%d egid=%d uid_map=%q(%v) gid_map=%q(%v) roots=%s", err, os.Getpid(), os.Geteuid(), os.Getegid(), u, ue, g, ge, rawRoots)
	}
	return result, nil
}

type namespaceProofHost struct {
	gateway.UpgradeHost
	proof *gateway.NativeUpgradeParentProof
}

func (h namespaceProofHost) NativeUpgradeProof() *gateway.NativeUpgradeParentProof { return h.proof }

// Opt-in: only a disposable Pod with EmptyDir at /workspaces and the pinned
// native payload. This reexecutes the actual Go scoped provider through the
// shared production bwrap/credential/frame path, rather than Python alone.
func TestOpenCodeScopedNamespacePreserveAcceptance(t *testing.T) {
	if os.Getenv("CM_TEST_OPENCODE_NAMESPACE_DISPOSABLE") != "1" {
		t.Skip("requires disposable image/EmptyDir namespace fixture on server54")
	}
	if os.Geteuid() != 0 {
		t.Fatal("fixture setup requires root")
	}
	const uid = 200007
	const root = "/workspaces/opencode-namespace-acceptance"
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	workspace := filepath.Join(root, "opencode/user-1/instance-7")
	capsule := filepath.Join(root, ".clawmanager-upgrades/rollout-ns-test/instance-7")
	for _, path := range []string{workspace, capsule} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		// Both real NFS grants are management-owned; only their descendants
		// belong to the business UID.
		owner := 10001
		if err := os.Chmod(path, 0770); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, owner, uid); err != nil {
			t.Fatal(err)
		}
	}
	seed := exec.Command("/usr/bin/python3", "-I", "-B", "-c", namespaceSQLiteFixture, workspace, string(upgradeSchema))
	seed.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
	input, err := seed.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := seed.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	seed.Stderr = os.Stderr
	if err := seed.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		input.Close()
		if err := seed.Wait(); err != nil {
			t.Error(err)
		}
	}()
	lines := bufio.NewScanner(output)
	ready := func() {
		t.Helper()
		if !lines.Scan() || lines.Text() != "ready" {
			t.Fatal("SQLite/WAL fixture is unavailable", lines.Text(), lines.Err())
		}
	}
	ready()
	profile := NewProfile("opencode")
	cfg := gateway.Config{Runtime: profile, RuntimeType: "opencode", PodUID: "disposable-ns", WorkspaceRoot: root, SandboxProfile: "nfs-rootless-v1", NetworkSecurityProfile: "isolated-network-v1"}
	req := gateway.WorkspaceUpgradeRequest{ExpectedPodUID: cfg.PodUID, RolloutID: "ns-test", UserID: 1, InstanceID: 7, Generation: 1, UID: uid, GID: uid, PreserveSessionSQLite: true}
	for i, action := range []string{"compatibility", "inventory", "migrate", "migration-status", "restore", "restore-status"} {
		t.Run(action, func(t *testing.T) {
			if action == "restore" {
				fmt.Fprintln(input, "advance")
				ready()
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			result, err := slots.ExecuteNativeUpgrade(ctx, cfg, gateway.NativeUpgradeOperation{OperationID: fmt.Sprintf("%032x", i+1), Action: action, Request: req}, func(context.Context) error { return nil })
			if err != nil {
				t.Fatal("full Go scoped provider via bwrap:", err)
			}
			var fields map[string]any
			if json.Unmarshal(result, &fields) != nil {
				t.Fatal("invalid native result")
			}
			if action == "migrate" && (fields["data_preserved"] != true || fields["session_count"] != float64(1)) {
				t.Fatalf("missing preservation proof: %s", result)
			}
			if strings.HasPrefix(action, "restore") && fields["preserved_session_sqlite"] != true {
				t.Fatalf("missing restore preservation proof: %s", result)
			}
		})
	}
	for i, fault := range []struct{ name, want string }{{"missing", "trusted parent grant proof"}, {"inode", "audited inode"}, {"path", "parent grant identity"}, {"owner", "parent grant identity"}, {"gid", "parent grant identity"}, {"actor", "trusted parent grant proof"}, {"extra", "trusted parent grant proof"}} {
		t.Run("parent-proof-"+fault.name, func(t *testing.T) {
			altered := req
			altered.SnapshotID = "fault-" + fault.name
			_, err := slots.ExecuteNativeUpgrade(context.Background(), cfg, gateway.NativeUpgradeOperation{OperationID: fmt.Sprintf("%032x", 30+i), Action: "compatibility", Request: altered}, func(context.Context) error { return nil })
			if err == nil || !strings.Contains(err.Error(), fault.want) {
				t.Fatalf("missing parent proof rejection %q: %v", fault.want, err)
			}
		})
	}
	t.Run("management-descendant-rejected-before-launch", func(t *testing.T) {
		badWorkspace := filepath.Join(root, "opencode/user-1/instance-8")
		badCapsule := filepath.Join(root, ".clawmanager-upgrades/rollout-ns-test/instance-8")
		for _, path := range []string{badWorkspace, badCapsule} {
			if err := os.MkdirAll(path, 0755); err != nil {
				t.Fatal(err)
			}
			if path == badWorkspace {
				foreign := filepath.Join(path, "foreign-state")
				if err := os.WriteFile(foreign, []byte("management-owned descendant"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chown(foreign, 10001, 200008); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(path, 0770); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(path, 10001, 200008); err != nil {
				t.Fatal(err)
			}
		}
		altered := req
		altered.InstanceID = 8
		altered.UID = 200008
		altered.GID = 200008
		_, err := slots.ExecuteNativeUpgrade(context.Background(), cfg, gateway.NativeUpgradeOperation{OperationID: fmt.Sprintf("%032x", 60), Action: "compatibility", Request: altered}, func(context.Context) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "NFS private inode validation failed") {
			t.Fatal("management-owned descendant entered native helper", err)
		}
	})
	fmt.Fprintln(input, "check")
	ready()
	if !t.Failed() {
		t.Log("six native actions through Go scoped provider and production bwrap; live WAL, original/new history and project marker retained")
	}
}

const namespaceSQLiteFixture = `import json,os,sqlite3,sys
from pathlib import Path
w=Path(sys.argv[1]);s=json.loads(sys.argv[2]);d=w/'home/.local/share/opencode';d.mkdir(parents=True,mode=0o700)
c=w/'home/.opencode';c.mkdir(mode=0o700);(c/'opencode.json').write_text('{"provider":{}}')
(w/'project').mkdir();(w/'project/marker').write_text('project-retained')
db=sqlite3.connect(d/'opencode.db');db.executescript('\n'.join(s['sql']));db.execute('PRAGMA journal_mode=WAL');db.execute('PRAGMA wal_autocheckpoint=0');db.execute(s['journal_sql']);db.executemany('INSERT INTO migration VALUES(?,1)',[(v,) for v in s['migrations']])
db.execute("INSERT INTO project(id,worktree,time_created,time_updated,sandboxes) VALUES('prj','/project',1,1,'[]')")
db.execute("INSERT INTO session(id,project_id,slug,directory,title,version,time_created,time_updated) VALUES('ses','prj','s','/project','Fixture','1.18.13',1,1)")
db.execute("INSERT INTO session_message(id,session_id,type,seq,time_created,time_updated,data) VALUES('msg','ses','user',1,1,1,'{}')")
db.execute("INSERT INTO message(id,session_id,time_created,time_updated,data) VALUES('legacy','ses',1,1,'{}')")
db.execute("INSERT INTO part(id,message_id,session_id,time_created,time_updated,data) VALUES('part','legacy','ses',1,1,'{}')")
db.execute("INSERT INTO session_input(id,session_id,prompt,delivery,admitted_seq,time_created) VALUES('input','ses','{}','steer',1,1)")
db.execute("INSERT INTO session_context_epoch(session_id,baseline,snapshot,baseline_seq) VALUES('ses','baseline','{}',1)");db.commit()
assert (d/'opencode.db-wal').stat().st_size>0
print('ready',flush=True)
for line in sys.stdin:
 if line.strip()=='advance':
  db.execute("INSERT INTO session_message(id,session_id,type,seq,time_created,time_updated,data) VALUES('new','ses','user',2,2,2,'{}')");db.commit()
 if line.strip()=='check':
  assert db.execute('SELECT count(*) FROM session_message').fetchone()[0]==2
  for table in ('message','part','session_input','session_context_epoch'): assert db.execute('SELECT count(*) FROM '+table).fetchone()[0]==1
  assert (w/'project/marker').read_text()=='project-retained'
 print('ready',flush=True)
db.close()
`

type namespacePayloadProbe struct {
	Binary, Attestation string
	UID, GID            int
	Startup             bool
	Proof               *gateway.NativeUpgradeParentProof
	RootPaths           [2]string
}

func runNamespacePayloadProbe() {
	var probe namespacePayloadProbe
	if err := json.NewDecoder(os.Stdin).Decode(&probe); err != nil {
		panic(err)
	}
	paths := deliveredNativePayload
	if probe.Binary != "" {
		paths.binary = probe.Binary
	}
	if probe.Attestation != "" {
		paths.attestation = probe.Attestation
	}
	paths.scopedUID, paths.scopedGID = probe.UID, probe.GID
	paths.scopedProof, paths.scopedRoots = probe.Proof, probe.RootPaths
	result := map[string]any{"uid": os.Geteuid(), "gid": os.Getegid()}
	for _, name := range []string{"uid_map", "gid_map"} {
		raw, _ := os.ReadFile("/proc/self/" + name)
		result[name] = string(raw)
	}
	var files []map[string]any
	for _, path := range []string{paths.binary, paths.attestation} {
		info, err := os.Lstat(path)
		if err != nil {
			panic(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		var fs syscall.Statfs_t
		syscall.Statfs(path, &fs)
		files = append(files, map[string]any{"path": path, "uid": st.Uid, "gid": st.Gid, "mode": info.Mode().String(), "links": st.Nlink, "readonly": fs.Flags&1 != 0})
	}
	result["files"] = files
	var err error
	if probe.Startup {
		deliveredNativePayload = paths
		os.Setenv("CLAWMANAGER_OPENCODE_UPGRADE_PROTOCOL", OpenCodeUpgradeProtocol)
		err = validateOpenCodeNativePayload(context.Background())
		metadata := NewProfile("opencode").RegistrationMetadata(gateway.Config{})
		result["caps"] = len(metadata.Capabilities)
	} else {
		var cache nativePayloadProofCache
		checks := 0
		for i := 0; i < 2; i++ {
			err = cache.check(context.Background(), paths, func(ctx context.Context) error { checks++; return validateOpenCodeNativePayloadFiles(ctx, paths) })
			if err != nil {
				break
			}
		}
		if err == nil {
			paths.scopedProof.Roots[0].Inode++
			changed := cache.check(context.Background(), paths, func(context.Context) error { checks++; return nil })
			paths.scopedProof.Roots[0].Inode--
			result["changed_root_rejected"] = changed != nil && checks == 1
		}
		result["checks"] = checks
	}
	if err != nil {
		result["error"] = err.Error()
	} else {
		result["error"] = ""
	}
	json.NewEncoder(os.Stdout).Encode(result)
}

func TestOpenCodeNamespaceIDMaps(t *testing.T) {
	for _, raw := range []string{"200007 200007 1\n", " 200007   200007  1 ", "200007 0 1"} {
		if !singleNativePayloadIDMap([]byte(raw), 200007) {
			t.Fatal("valid single mapping rejected")
		}
	}
	for _, raw := range []string{"", "0 0 4294967295", "200007 200008 1", "200008 200007 1", "200007 200007 2", "200007 200007 1\n0 0 1", "200007 200007 1 trailing"} {
		if singleNativePayloadIDMap([]byte(raw), 200007) {
			t.Fatalf("untrusted ID map accepted: %q", raw)
		}
	}
}

func TestOpenCodeNamespacePayloadBoundaryAcceptance(t *testing.T) {
	if os.Getenv("CM_TEST_OPENCODE_NAMESPACE_DISPOSABLE") != "1" {
		t.Skip("requires disposable namespace/EmptyDir fixture")
	}
	const uid = 200007
	dir := "/workspaces/payload-proof/opencode/user-1/instance-7"
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, raw []byte, owner int, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, raw, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, owner, owner); err != nil {
			t.Fatal(err)
		}
		return path
	}
	correct := write("attestation", upgradeAttestation, 0, 0644)
	business := write("business", upgradeAttestation, uid, 0644)
	overflow := write("overflow", upgradeAttestation, 65534, 0644)
	var manifest map[string]any
	json.Unmarshal(upgradeAttestation, &manifest)
	manifest["journal_sha256"] = "forged"
	badRaw, _ := json.Marshal(manifest)
	badAttestation := write("forged-attestation", badRaw, 0, 0644)
	badBinary := write("forged-binary", []byte("forged"), 0, 0755)
	writableMode := write("writable-mode", upgradeAttestation, 0, 0666)
	// Explicit chmod defeats the process umask in this negative fixture.
	if err := os.Chmod(writableMode, 0666); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(correct, symlink); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(dir, "hardlink")
	if err := os.Link(correct, hardlink); err != nil {
		t.Fatal(err)
	}
	rootPaths := [2]string{filepath.Join(dir, "workspace-root"), filepath.Join(dir, "capsule-root")}
	proof := &gateway.NativeUpgradeParentProof{ActorUID: uid, ActorGID: uid}
	for _, path := range rootPaths {
		if err := os.Mkdir(path, 0770); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0770); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, 10001, uid); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		proof.Roots = append(proof.Roots, gateway.NativeUpgradeRootProof{Path: path, Device: uint64(st.Dev), Inode: st.Ino, OwnerUID: st.Uid, OwnerGID: st.Gid, Mode: uint32(st.Mode)})
	}
	executable, _ := os.Executable()
	for _, tc := range []struct {
		name, binary, attestation, want string
		rw, startup, outside            bool
		actor                           int
	}{
		{name: "fixed-image", actor: uid},
		{name: "startup-unmapped-owner", startup: true, want: "immutable root-owned", actor: 0},
		{name: "startup-outer-overflow", attestation: overflow, startup: true, outside: true, want: "immutable root-owned", actor: 0},
		{name: "business-owner", attestation: business, want: "immutable image", actor: uid},
		{name: "writable-mount", attestation: badAttestation, rw: true, want: "read-only image", actor: uid},
		{name: "bad-sha", binary: badBinary, want: "differs from the preserved source image", actor: uid},
		{name: "bad-attestation", attestation: badAttestation, want: "attestation differs", actor: uid},
		{name: "writable-mode", attestation: writableMode, want: "immutable image", actor: uid},
		{name: "symlink", attestation: symlink, want: "immutable image", actor: uid},
		{name: "hardlink", attestation: hardlink, want: "immutable image", actor: uid},
		{name: "broad-outer-map", outside: true, want: "single identity mapping", actor: uid},
		{name: "wrong-actor", want: "exact business actor", actor: uid + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := namespacePayloadProbe{Binary: tc.binary, Attestation: tc.attestation, UID: tc.actor, GID: tc.actor, Startup: tc.startup, Proof: proof, RootPaths: rootPaths}
			raw, _ := json.Marshal(probe)
			var cmd *exec.Cmd
			if tc.outside {
				cmd = exec.Command(executable, "opencode-namespace-payload-proof")
			} else {
				argv, err := sandbox.Command(sandbox.Spec{ContainerLifecycle: true, OfflineNetwork: true, UID: uid, GID: uid, Dir: dir, Args: []string{executable, "opencode-namespace-payload-proof"}, Env: []string{"PATH=/usr/bin:/bin", "CM_TEST_OPENCODE_NAMESPACE_DISPOSABLE=1"}, Mounts: []sandbox.Mount{{Source: dir, Target: dir, ReadOnly: !tc.rw}}})
				if err != nil {
					t.Fatal(err)
				}
				cmd = exec.Command(argv[0], argv[1:]...)
			}
			cmd.Env = []string{"PATH=/usr/bin:/bin", "CM_TEST_OPENCODE_NAMESPACE_DISPOSABLE=1"}
			if !(tc.outside && tc.startup) {
				gateway.ConfigureGatewayCommand(cmd, uid, uid)
			}
			cmd.Stdin = bytes.NewReader(raw)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("payload probe: %v %s", err, output)
			}
			var result map[string]any
			if json.Unmarshal(output, &result) != nil {
				t.Fatalf("invalid proof: %s", output)
			}
			failure, _ := result["error"].(string)
			if tc.want == "" {
				if failure != "" || result["checks"] != float64(1) || result["changed_root_rejected"] != true {
					t.Fatalf("trusted immutable scope rejected/rehashed: %s", output)
				}
			} else if !strings.Contains(failure, tc.want) {
				t.Fatalf("missing rejection %q: %s", tc.want, output)
			}
			if tc.startup && result["caps"] != float64(0) {
				t.Fatalf("startup advertised untrusted payload: %s", output)
			}
			if tc.want == "" {
				hash := sha256.New()
				f, err := os.Open(deliveredNativePayload.binary)
				if err != nil {
					t.Fatal(err)
				}
				// The outer image hash is recorded independently of the scoped proof.
				if _, err := io.Copy(hash, f); err != nil {
					t.Fatal(err)
				}
				f.Close()
				t.Logf("actual image native SHA %x; scoped metadata %s", hash.Sum(nil), output)
			}
		})
	}
}

func TestOpenCodeScopedPayloadRejectsManagementActor(t *testing.T) {
	if err := validateScopedOpenCodeNativePayload(context.Background(), 0, 0, nil, [2]string{}); err == nil || !strings.Contains(err.Error(), "exact business actor") {
		t.Fatal("management actor entered scoped payload mode", err)
	}
}
