package openclaw

import (
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
	"os"
	"path/filepath"
	"testing"
)

func TestUpgradePluginCapsulePreservesBinLinkThroughAbsolutePackageLink(t *testing.T) {
	m, _, workspace := scopedUpgradeService(t)
	state := filepath.Join(workspace, "home", ".openclaw")
	source := filepath.Join(state, "npm")
	modules := filepath.Join(source, "projects", "sample", "node_modules")
	realPackage := filepath.Join(state, "packages", "sample")
	for _, dir := range []string{filepath.Join(modules, ".bin"), filepath.Join(modules, "@sample"), filepath.Join(realPackage, "bin")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(realPackage, "bin", "tool.js"), []byte("image-owned binary"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPackage, filepath.Join(modules, "@sample", "pkg")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(modules, ".bin", "tool")
	const original = "../@sample/pkg/bin/tool.js"
	if err := os.Symlink(original, link); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(m.io.controlRoot(), "npm")
	if _, _, err := m.copyUpgradePluginTree(source, target, state); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(target, "projects", "sample", "node_modules", ".bin", "tool")
	if actual, err := os.Readlink(copied); err != nil || actual != original {
		t.Fatalf("capsule link = %q %v", actual, err)
	}
	if _, err := os.Lstat(filepath.Join(target, "packages")); !os.IsNotExist(err) {
		t.Fatalf("link referent copied into capsule: %v", err)
	}
	imageFS, err := workspaceio.Open(realPackage)
	if err != nil {
		t.Fatal(err)
	}
	m.io.scopes = append(m.io.scopes, upgradeScope{imageFS, false})
	if err := m.validateUpgradePluginLinks(state); err != nil {
		t.Fatalf("validate indirect readonly package: %v", err)
	}
	if err := os.Remove(filepath.Join(realPackage, "bin", "tool.js")); err != nil {
		t.Fatal(err)
	}
	if err := m.validateUpgradePluginLinks(state); err == nil {
		t.Fatal("validation accepted a missing package binary")
	}
}

func TestUpgradePluginLinkResolutionCrossesOnlyApprovedRoots(t *testing.T) {
	for _, kind := range []string{"image", "missing_image_package", "external_package", "image_escape", "loop", "parent_after_symlink"} {
		t.Run(kind, func(t *testing.T) {
			m, _, workspace := scopedUpgradeService(t)
			state := filepath.Join(workspace, "home", ".openclaw")
			modules := filepath.Join(state, "npm", "node_modules")
			for _, dir := range []string{filepath.Join(modules, ".bin"), filepath.Join(modules, "@sample")} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			image := t.TempDir()
			imageFS, err := workspaceio.Open(image)
			if err != nil {
				t.Fatal(err)
			}
			m.io.scopes = append(m.io.scopes, upgradeScope{imageFS, false})
			packagePath := filepath.Join(image, "package")
			expected := filepath.Join(packagePath, "bin", "tool.js")
			if kind != "missing_image_package" {
				if err := os.MkdirAll(filepath.Dir(expected), 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "image_escape" {
					if err := os.Symlink("/etc/passwd", expected); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(expected, []byte("immutable"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "external_package" || kind == "parent_after_symlink" {
				packagePath = "/etc/untrusted/package"
			}
			if err := os.Symlink(packagePath, filepath.Join(modules, "@sample", "pkg")); err != nil {
				t.Fatal(err)
			}
			target := modules + "/@sample/pkg/bin/tool.js"
			if kind == "loop" {
				if err := os.Symlink("cycle", filepath.Join(modules, "cycle")); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(modules, "cycle")
			}
			if kind == "parent_after_symlink" {
				if err := os.WriteFile(filepath.Join(modules, "good.js"), []byte("not the actual target"), 0600); err != nil {
					t.Fatal(err)
				}
				target = modules + "/@sample/pkg/../../good.js"
			}
			actual, err := m.resolveUpgradePluginPath(target, true, state, image)
			valid := kind == "image" || kind == "missing_image_package"
			if valid && (err != nil || actual != expected) {
				t.Fatalf("resolved=%q err=%v", actual, err)
			}
			if kind == "missing_image_package" {
				if _, err := m.resolveUpgradePluginPath(target, false, state, image); err == nil {
					t.Fatal("strict validation accepted missing image target")
				}
			}
			if !valid && err == nil {
				t.Fatalf("unsafe %s target accepted: %s", kind, actual)
			}
		})
	}
}

func TestUpgradeSeedValidatesIndirectImageBinTargets(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "approved_image", true: "external_target"}[external], func(t *testing.T) {
			m, _, workspace := scopedUpgradeService(t)
			defaults := t.TempDir()
			image := filepath.Join(defaults, "project", "node_modules")
			pkg := filepath.Join(defaults, "packages", "sample")
			dest := filepath.Join(workspace, "home", ".openclaw", "npm", "project", "node_modules")
			for _, dir := range []string{filepath.Join(image, ".bin"), filepath.Join(image, "@sample"), filepath.Join(pkg, "bin"), filepath.Join(dest, ".bin")} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(pkg, "bin", "tool.js"), []byte("immutable"), 0600); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(dest, ".bin", "user-file")
			if err := os.WriteFile(sentinel, []byte("preserved"), 0600); err != nil {
				t.Fatal(err)
			}
			if external {
				pkg = "/etc/untrusted/package"
			}
			if err := os.Symlink(pkg, filepath.Join(image, "@sample", "pkg")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../@sample/pkg/bin/tool.js", filepath.Join(image, ".bin", "tool")); err != nil {
				t.Fatal(err)
			}
			imageFS, err := workspaceio.Open(defaults)
			if err != nil {
				t.Fatal(err)
			}
			m.io.scopes = append(m.io.scopes, upgradeScope{imageFS, false})
			err = m.seedImagePluginTreeWithin(image, dest, defaults)
			if external {
				if err == nil {
					t.Fatal("seed accepted an external image link")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if actual, err := os.Readlink(filepath.Join(dest, ".bin", "tool")); err != nil || actual != filepath.Join(pkg, "bin", "tool.js") {
				t.Fatalf("seed bin=%q %v", actual, err)
			}
			if actual, err := os.ReadFile(sentinel); err != nil || string(actual) != "preserved" {
				t.Fatalf("user override=%q %v", actual, err)
			}
		})
	}
}
