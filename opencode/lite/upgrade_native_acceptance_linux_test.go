//go:build linux

package opencode

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// Uses real kernel credentials and the production embedded helper. OCI worker
// stop/lease/teardown behavior remains covered by the shared slots suite.
func TestOpenCodeNativePreserveRunsAsInstanceUID(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to exercise the instance UID boundary")
	}
	base, err := os.MkdirTemp("", "opencode-native-uid-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	if err = os.Chmod(base, 0o711); err != nil {
		t.Fatal(err)
	}
	const uid = 200007
	workspace, capsule := filepath.Join(base, "workspace"), filepath.Join(base, "capsule")
	for _, path := range []string{workspace, capsule} {
		if err = os.Mkdir(path, 0o770); err != nil {
			t.Fatal(err)
		}
		owner := uid
		if path == capsule {
			owner = 10001
		}
		if err = os.Chown(path, owner, uid); err != nil {
			t.Fatal(err)
		}
		if err = os.Chmod(path, 0o770); err != nil {
			t.Fatal(err)
		}
	}
	payload, _ := json.Marshal(map[string]any{"helper": upgradePython, "schema": upgradeSchema, "workspace": workspace, "capsule": capsule})
	script := `import json,os,sqlite3,sys
from pathlib import Path
p=json.load(sys.stdin)
scope={"__name__":"native_test"};exec(compile(p["helper"],"embedded-native-helper","exec"),scope)
w=Path(p["workspace"]);data=w/"home/.local/share/opencode";data.mkdir(parents=True,mode=0o700)
config=w/"home/.opencode";config.mkdir(mode=0o700);(config/"opencode.json").write_text("{}")
db=sqlite3.connect(data/"opencode.db");db.executescript("\n".join(p["schema"]["sql"]))
db.execute("PRAGMA journal_mode=WAL");db.execute("PRAGMA wal_autocheckpoint=0")
db.execute("CREATE TABLE migration(id TEXT PRIMARY KEY,time_completed INTEGER NOT NULL)")
db.executemany("INSERT INTO migration VALUES(?,1)",[(x,) for x in p["schema"]["migrations"]]);db.commit()
request=dict(rollout_id="uid-preserve",instance_id=7,user_id=3,generation=2,uid=os.geteuid(),gid=os.getegid())
op=dict(action="migrate",workspace=p["workspace"],capsule=p["capsule"],request=request,schema=p["schema"])
result=scope["main"](op);assert result["status"]=="validated" and result["data_preserved"]
op["action"]="migration-status";assert scope["main"](op)==result
op["action"]="restore";op["request"]["preserve_session_sqlite"]=True
assert scope["main"](op)["preserved_session_sqlite"]
assert os.stat(Path(p["capsule"])/"opencode-preserve/database.sqlite").st_uid==os.geteuid()
db.close()
print("instance UID SQLite/WAL preservation verified")`
	cmd := exec.Command("/usr/bin/python3", "-I", "-B", "-c", script)
	cmd.Dir = "/tmp"
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(payload)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("instance UID native acceptance: %v\n%s", err, output)
	}
}
