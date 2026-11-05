package workspacegit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type registry struct {
	Version  int       `json:"version"`
	Projects []Project `json:"projects"`
}

func (s *Service) metadataDirectory() (string, error) {
	dir := filepath.Join(s.root, ".clawmanager")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return "", failure("registry_unavailable", "The Git project registry cannot be created.")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", failure("invalid_path", "Git registry metadata must be a workspace directory.")
	}
	return dir, nil
}

// A filesystem lock also serializes separate short-lived sandbox helpers.
// ponytail: one workspace lock covers Git mutations; split per repository if
// throughput later warrants the added lock ordering and registry coordination.
func (s *Service) lock(ctx context.Context) (func(), error) {
	dir, err := s.metadataDirectory()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "git-projects.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, failure("invalid_path", "The registry lock cannot be opened safely.")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, failure("invalid_path", "The registry lock must be a regular file.")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
		f.Close()
		return nil, failure("invalid_path", "The registry lock must not be shared through hard links.")
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, failure("registry_unavailable", "The Git registry cannot be locked.")
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, contextError(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (s *Service) readRegistry(ctx context.Context) (*registry, error) {
	path := filepath.Join(s.root, ".clawmanager", "git-projects.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return &registry{Version: 1, Projects: []Project{}}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, failure("invalid_path", "The Git registry must be a regular file.")
	}
	data, err := readBounded(path, streamLimit)
	if err != nil {
		return nil, failure("invalid_registry", "The Git project registry is unreadable or exceeds its limit.")
	}
	var reg registry
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&reg); err != nil {
		return nil, failure("invalid_registry", "The Git project registry contains invalid JSON.")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || reg.Version != 1 || len(reg.Projects) > 1000 {
		return nil, failure("invalid_registry", "The Git project registry has an unsupported format or size.")
	}
	seen := map[string]bool{}
	for _, p := range reg.Projects {
		rel, _, err := s.projectPath(p.Path)
		if err != nil {
			return nil, err
		}
		if rel != p.Path || seen[rel] || !validName(p.Name) {
			return nil, failure("invalid_registry", "The Git project registry contains an invalid project.")
		}
		seen[rel] = true
		if _, err = s.repository(ctx, p.Path); err != nil {
			return nil, err
		}
		if p.Remote != "" {
			if p.Remote != publicRemote(p.Remote) {
				return nil, failure("invalid_registry", "The Git project registry must not contain URL credentials.")
			}
			if _, err = s.remote(p.Remote, s.root); err != nil {
				return nil, failure("invalid_registry", "The Git project registry contains an unsafe remote.")
			}
		}
	}
	sort.Slice(reg.Projects, func(i, j int) bool { return reg.Projects[i].Path < reg.Projects[j].Path })
	return &reg, nil
}

func (s *Service) saveRegistry(reg *registry) error {
	if len(reg.Projects) > 1000 {
		return failure("output_limit", "The Git project registry contains too many projects.")
	}
	data, err := json.Marshal(reg)
	if err != nil || len(data) > streamLimit {
		return failure("output_limit", "The Git project registry exceeds its size limit.")
	}
	dir, err := s.metadataDirectory()
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".git-projects-*")
	if err != nil {
		return failure("registry_unavailable", "The Git project registry cannot be written.")
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(dir, "git-projects.json"))
	}
	if err != nil {
		return failure("registry_unavailable", "The Git project registry could not be saved atomically.")
	}
	d, err := os.Open(dir)
	if err != nil {
		return failure("registry_unavailable", "The Git project registry directory cannot be synced.")
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return failure("registry_unavailable", "The Git project registry directory could not be synced.")
	}
	return nil
}

// Root repositories must never version the reserved directory: a whole-tree
// switch or merge could otherwise replace both the registry and the inode on
// which other helpers coordinate their lock. Check before any worktree write.
func (s *Service) protectRegistry(ctx context.Context, dir string, targets ...string) error {
	if dir != s.root {
		return nil
	}
	index, err := runGit(ctx, dir, streamLimit, "", "ls-files", "-z", "--")
	if err != nil {
		return err
	}
	if err = checkReservedEntries(index.output); err != nil {
		return err
	}
	born, err := hasHEAD(ctx, dir)
	if err != nil {
		return err
	}
	if born {
		targets = append(targets, "HEAD")
	}
	for _, target := range targets {
		tree, err := runGit(ctx, dir, streamLimit, "", "ls-tree", "-r", "-z", "--name-only", target, "--")
		if err != nil {
			return err
		}
		if err = checkReservedEntries(tree.output); err != nil {
			return err
		}
	}
	return nil
}

func checkReservedEntries(output string) error {
	if output == "" {
		return nil
	}
	if !strings.HasSuffix(output, "\x00") {
		return failure("invalid_output", "Git returned incomplete tracked paths.")
	}
	for _, path := range strings.Split(strings.TrimSuffix(output, "\x00"), "\x00") {
		first, _, _ := strings.Cut(path, "/")
		if strings.EqualFold(first, ".clawmanager") {
			return failure("reserved_metadata", "The workspace root repository tracks reserved .clawmanager metadata. Remove that directory from Git tracking before continuing.")
		}
	}
	return nil
}
