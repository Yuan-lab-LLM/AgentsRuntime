package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNetworkRoutingFilePolicy(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "0123456789abcdef0123456789abcdef")
	if err = os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "hosts")
	if err = os.WriteFile(file, []byte("127.0.0.1 localhost\n"), 0444); err != nil {
		t.Fatal(err)
	}
	if err = validateNetworkRoutingFile(file, "hosts", root, uint32(os.Geteuid())); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0644, 0666, 0400} {
		if err = os.Chmod(file, mode); err != nil {
			t.Fatal(err)
		}
		if validateNetworkRoutingFile(file, "hosts", root, uint32(os.Geteuid())) == nil {
			t.Fatalf("accepted routing file mode %o", mode)
		}
	}
	os.Chmod(file, 0444)
	if validateNetworkRoutingFile(file, "resolv.conf", root, uint32(os.Geteuid())) == nil {
		t.Fatal("accepted wrong fixed file")
	}
	if validateNetworkRoutingFile(file, "hosts", root, uint32(os.Geteuid()+1)) == nil {
		t.Fatal("accepted wrong owner")
	}
	link := filepath.Join(dir, "resolv.conf")
	if err = os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if validateNetworkRoutingFile(link, "resolv.conf", root, uint32(os.Geteuid())) == nil {
		t.Fatal("accepted symlink")
	}
	os.Chmod(dir, 0777)
	if validateNetworkRoutingFile(file, "hosts", root, uint32(os.Geteuid())) == nil {
		t.Fatal("accepted writable public directory")
	}
}
func TestNetworkRoutingFilesRejectArbitrarySources(t *testing.T) {
	spec := testSpec(t)
	spec.NetworkHostsFile = filepath.Join(spec.Mounts[0].Source, "hosts")
	spec.NetworkResolvConf = filepath.Join(spec.Mounts[0].Source, "resolv.conf")
	if _, err := buildCommand(spec, "/usr/bin/bwrap", nil); err == nil {
		t.Fatal("accepted arbitrary routing file source")
	}
}
