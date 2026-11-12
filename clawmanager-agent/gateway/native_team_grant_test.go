package gateway

import (
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeTeamGrantPinsExactAuthorizedDirectory(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix directory identity")
	}
	base := t.TempDir()
	shared := filepath.Join(base, "teams", "user-1", "team-7-shared")
	if err := os.MkdirAll(shared, 0750); err != nil {
		t.Fatal(err)
	}
	root, err := workspaceio.Open(shared)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	gid, _ := teamDirectoryGroup(root)
	dev, ino, err := nativeTeamDirectoryIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	grant := &NativeTeamGrant{Path: shared, GID: gid, Device: dev, Inode: ino}
	if !nativeTeamGrantForRoot(root, shared, gid, grant) {
		t.Fatal("valid inode grant rejected")
	}
	for _, mutate := range []func(*NativeTeamGrant){
		func(g *NativeTeamGrant) { g.Path += "-other" },
		func(g *NativeTeamGrant) { g.GID++ },
		func(g *NativeTeamGrant) { g.Device++ },
		func(g *NativeTeamGrant) { g.Inode++ },
	} {
		bad := *grant
		mutate(&bad)
		if nativeTeamGrantForRoot(root, shared, gid, &bad) {
			t.Fatal("changed grant accepted")
		}
	}
	if nativeTeamGrantForRoot(root, shared, gid, nil) {
		t.Fatal("missing proof accepted")
	}
	if err := os.Rename(shared, shared+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(shared, 0750); err != nil {
		t.Fatal(err)
	}
	replaced, err := workspaceio.Open(shared)
	if err != nil {
		t.Fatal(err)
	}
	defer replaced.Close()
	if nativeTeamGrantForRoot(replaced, shared, gid, grant) {
		t.Fatal("replacement inode accepted")
	}
}
