package openclaw

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
	"github.com/iamlovingit/clawmanager-agent/slots"
	"github.com/iamlovingit/clawmanager-agent/workspaceio"
)

type upgradeScope struct {
	root  *workspaceio.FS
	write bool
}
type upgradeIO struct {
	scopes           []upgradeScope
	pool             string
	operation        WorkspaceUpgradeRequest
	scratch          []string
	writerFinish     func(bool) error
	commandUncertain bool
}

// Hold the same durable single-writer fence for management IO and every native
// command in one mutation. Release only after all namespaces are confirmed gone.
func (m *upgradeService) beginMutationIO(req WorkspaceUpgradeRequest) (func() error, error) {
	closeIO, err := m.beginIO(req, true)
	if err != nil {
		return nil, err
	}
	state := m.io
	if m.cfg.IsolatedLifecycle {
		finish, err := slots.AcquireStoppedCommandWriter(m.cfg, req)
		if err != nil {
			closeIO()
			return nil, err
		}
		state.writerFinish = finish
	}
	return func() error {
		var finishErr error
		if state.writerFinish != nil {
			finishErr = state.writerFinish(!state.commandUncertain)
			state.writerFinish = nil
		}
		closeIO()
		if state.commandUncertain {
			finishErr = errors.Join(finishErr, errors.New("native upgrade command teardown remains unconfirmed"))
		}
		return finishErr
	}, nil
}

func (m *upgradeService) beginIO(req WorkspaceUpgradeRequest, write bool) (func(), error) {
	if m.io != nil {
		return func() {}, nil
	}
	workspace, err := m.host.WorkspaceForUpgrade(req)
	if err != nil {
		return nil, err
	}
	own, err := workspaceio.OpenAt(m.cfg.WorkspaceRoot, workspace)
	if err != nil {
		return nil, err
	}
	state := &upgradeIO{pool: m.cfg.WorkspaceRoot, operation: req, scopes: []upgradeScope{{own, write}}}
	rollout := req.RolloutID
	if rollout == "" {
		rollout = "preflight"
	} else if err := gateway.ValidateUpgradeID("rollout_id", rollout); err != nil {
		own.Close()
		return nil, err
	}
	capsule := filepath.Join(m.host.UpgradeRoot(), "rollout-"+rollout, fmt.Sprintf("instance-%d", req.InstanceID))
	var control *workspaceio.FS
	if write {
		if err := workspaceio.PrepareManagementRoots(m.cfg.WorkspaceRoot, ".clawmanager-upgrades"); err != nil {
			own.Close()
			return nil, err
		}
		control, err = workspaceio.CreateAt(m.cfg.WorkspaceRoot, capsule, 0700)
	} else {
		control, err = workspaceio.OpenAt(m.cfg.WorkspaceRoot, capsule)
	}
	if err != nil && !os.IsNotExist(err) {
		own.Close()
		return nil, err
	}
	if control != nil {
		state.scopes = append(state.scopes, upgradeScope{control, write})
	}
	if req.ResumeRolloutID != "" && req.ResumeRolloutID != req.RolloutID {
		if err := gateway.ValidateUpgradeID("resume_rollout_id", req.ResumeRolloutID); err != nil {
			for _, scope := range state.scopes {
				scope.root.Close()
			}
			return nil, err
		}
		priorPath := filepath.Join(m.host.UpgradeRoot(), "rollout-"+req.ResumeRolloutID, fmt.Sprintf("instance-%d", req.InstanceID))
		prior, priorErr := workspaceio.OpenAt(m.cfg.WorkspaceRoot, priorPath)
		if priorErr != nil && !os.IsNotExist(priorErr) {
			for _, scope := range state.scopes {
				scope.root.Close()
			}
			return nil, priorErr
		}
		if prior != nil {
			state.scopes = append(state.scopes, upgradeScope{prior, false})
		}
	}
	m.io = state
	return func() {
		for _, scope := range state.scopes {
			scope.root.Close()
		}
		if !state.commandUncertain {
			for _, path := range state.scratch {
				os.RemoveAll(path)
			}
		}
		m.io = nil
	}, nil
}
func (s *upgradeIO) scope(name string, write bool) (*workspaceio.FS, error) {
	if write && s.commandUncertain {
		return nil, errors.New("upgrade mutation blocked: command teardown is unconfirmed")
	}
	name = filepath.Clean(name)
	var selected *upgradeScope
	for i := range s.scopes {
		scope := &s.scopes[i]
		if gateway.PathWithin(scope.root.Base, name) && (selected == nil || len(scope.root.Base) > len(selected.root.Base)) {
			selected = scope
		}
	}
	if selected != nil {
		if write && !selected.write {
			return nil, fmt.Errorf("upgrade readonly scope")
		}
		return selected.root, nil
	}
	if !write {
		for _, base := range []string{"/usr", "/defaults", "/opt"} {
			if gateway.PathWithin(base, name) {
				root, err := workspaceio.Open(base)
				if err != nil {
					return nil, err
				}
				s.scopes = append(s.scopes, upgradeScope{root, false})
				return root, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: upgrade path outside assigned instance and operation capsule", os.ErrNotExist)
}

// Image trees are immutable management inputs and contain absolute links to
// other image packages. Resolve those links only between fixed image roots;
// instance roots never receive this exception.
func (s *upgradeIO) readScope(p string) (*workspaceio.FS, string, error) {
	root, err := s.scope(p, false)
	if err != nil {
		return nil, "", err
	}
	if root.Base != "/usr" && root.Base != "/defaults" && root.Base != "/opt" {
		return root, p, nil
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, "", err
	}
	if !gateway.PathWithin("/usr", resolved) && !gateway.PathWithin("/defaults", resolved) && !gateway.PathWithin("/opt", resolved) {
		return nil, "", fmt.Errorf("image input link leaves fixed image roots")
	}
	target, err := s.scope(resolved, false)
	return target, resolved, err
}

func (s *upgradeIO) ReadFile(p string) ([]byte, error) {
	root, p, err := s.readScope(p)
	if err != nil {
		return nil, err
	}
	return root.ReadFile(p)
}
func (s *upgradeIO) WriteFile(p string, data []byte, mode os.FileMode) error {
	root, err := s.scope(p, true)
	if err != nil {
		return err
	}
	return root.WriteFileAtomic(p, data, mode)
}
func (s *upgradeIO) AtomicWriteFile(p string, data []byte, mode os.FileMode) error {
	return s.WriteFile(p, data, mode)
}
func (s *upgradeIO) Open(p string) (*os.File, error) {
	root, p, err := s.readScope(p)
	if err != nil {
		return nil, err
	}
	return root.Open(p)
}
func (s *upgradeIO) OpenFile(p string, flags int, mode os.FileMode) (*os.File, error) {
	write := flags&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0
	root, err := s.scope(p, write)
	if err != nil {
		return nil, err
	}
	return root.OpenFile(p, flags, mode)
}
func (s *upgradeIO) OpenRoot(p string) (*os.Root, error) {
	if s.commandUncertain {
		return nil, errors.New("upgrade root access blocked: command teardown is unconfirmed")
	}
	root, p, err := s.readScope(p)
	if err != nil {
		return nil, err
	}
	rel, err := root.Rel(p)
	if err != nil {
		return nil, err
	}
	return root.Root.OpenRoot(rel)
}
func (s *upgradeIO) Stat(p string) (os.FileInfo, error) {
	root, p, err := s.readScope(p)
	if err != nil {
		return nil, err
	}
	return root.Stat(p)
}
func (s *upgradeIO) Lstat(p string) (os.FileInfo, error) {
	root, err := s.scope(p, false)
	if err != nil {
		return nil, err
	}
	return root.Lstat(p)
}
func (s *upgradeIO) ReadDir(p string) ([]os.DirEntry, error) {
	root, p, err := s.readScope(p)
	if err != nil {
		return nil, err
	}
	return root.ReadDir(p)
}
func (s *upgradeIO) Readlink(p string) (string, error) {
	root, err := s.scope(p, false)
	if err != nil {
		return "", err
	}
	return root.Readlink(p)
}
func (s *upgradeIO) Mkdir(p string, mode os.FileMode) error {
	root, err := s.scope(p, true)
	if err != nil {
		return err
	}
	return root.Mkdir(p, mode)
}
func (s *upgradeIO) MkdirAll(p string, mode os.FileMode) error {
	root, err := s.scope(p, true)
	if err != nil {
		return err
	}
	return root.MkdirAll(p, mode)
}
func (s *upgradeIO) Remove(p string) error {
	root, err := s.scope(p, true)
	if err != nil {
		return err
	}
	return root.Remove(p)
}
func (s *upgradeIO) RemoveAll(p string) error {
	root, err := s.scope(p, true)
	if err != nil {
		return err
	}
	return root.RemoveAll(p)
}
func (s *upgradeIO) Chown(p string, uid, gid int) error {
	root, err := s.scope(p, true)
	if err != nil {
		return err
	}
	return root.Chown(p, uid, gid)
}
func (s *upgradeIO) Rename(old, new string) error {
	a, err := s.scope(old, true)
	if err != nil {
		return err
	}
	b, err := s.scope(new, true)
	if err != nil {
		return err
	}
	if a != b {
		return renameUpgradeScopes(a, old, b, new)
	}
	return a.Rename(old, new)
}
func (s *upgradeIO) Symlink(old, new string) error {
	root, err := s.scope(new, true)
	if err != nil {
		return err
	}
	return root.Symlink(old, new)
}
func (s *upgradeIO) WalkDir(p string, fn fs.WalkDirFunc) error {
	root, err := s.scope(p, false)
	if err != nil {
		return err
	}
	rel, err := root.Rel(p)
	if err != nil {
		return err
	}
	return fs.WalkDir(root.Root.FS(), filepath.ToSlash(rel), func(name string, entry fs.DirEntry, walkErr error) error {
		return fn(filepath.Join(root.Base, filepath.FromSlash(name)), entry, walkErr)
	})
}
func (s *upgradeIO) Glob(pattern string) ([]string, error) {
	root, err := s.scope(pattern, false)
	if err != nil {
		return nil, err
	}
	rel, err := root.Rel(pattern)
	if err != nil {
		return nil, err
	}
	matches, err := fs.Glob(root.Root.FS(), filepath.ToSlash(rel))
	if err != nil {
		return nil, err
	}
	for i := range matches {
		matches[i] = filepath.Join(root.Base, filepath.FromSlash(matches[i]))
	}
	return matches, nil
}
func (s *upgradeIO) EvalSymlinks(p string) (string, error) {
	if filepath.Clean(p) == filepath.Clean(s.pool) {
		return filepath.EvalSymlinks(p)
	}
	root, resolved, err := s.readScope(p)
	if err != nil {
		return "", err
	}
	if _, err := root.Stat(resolved); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(resolved)
}
func (s *upgradeIO) MkdirTemp(dir, pattern string) (string, error) {
	if s.commandUncertain {
		return "", errors.New("upgrade staging blocked: command teardown is unconfirmed")
	}
	if dir == "/tmp" {
		path, err := os.MkdirTemp("/tmp", pattern)
		if err != nil {
			return "", err
		}
		root, err := workspaceio.Open(path)
		if err != nil {
			os.RemoveAll(path)
			return "", err
		}
		s.scopes = append(s.scopes, upgradeScope{root, true})
		s.scratch = append(s.scratch, path)
		return path, nil
	}
	root, err := s.scope(dir, true)
	if err != nil {
		return "", err
	}
	for i := 0; i < 8; i++ {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", err
		}
		name := pattern + hex.EncodeToString(nonce[:])
		if strings.Contains(pattern, "*") {
			name = strings.Replace(pattern, "*", hex.EncodeToString(nonce[:]), 1)
		}
		path := filepath.Join(dir, name)
		if err := root.Mkdir(path, 0700); os.IsExist(err) {
			continue
		} else if err != nil {
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("allocate upgrade staging directory")
}
func (s *upgradeIO) CopyRegularFile(source, target string, mode fs.FileMode) error {
	in, err := s.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := s.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func (s *upgradeIO) controlRoot() string {
	for _, scope := range s.scopes {
		if strings.Contains(scope.root.Base, ".clawmanager-upgrades") {
			return scope.root.Base
		}
	}
	return s.pool
}
