package workspacegit

import (
	"context"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func relativePath(path string, allowRoot bool) (string, error) {
	if strings.ContainsRune(path, 0) || strings.Contains(path, "\\") || filepath.IsAbs(path) || strings.HasPrefix(path, ":") {
		return "", failure("invalid_path", "Use a workspace-relative path.")
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." || strings.EqualFold(part, ".git") || strings.EqualFold(part, ".clawmanager") {
			return "", failure("invalid_path", "The path enters protected metadata or leaves the workspace.")
		}
	}
	path = filepath.Clean(path)
	if path == "." {
		path = ""
	}
	if path == "" && !allowRoot {
		return "", failure("invalid_path", "Choose a file or a new project directory.")
	}
	return filepath.ToSlash(path), nil
}

// confined resolves existing ancestors as well as missing destinations. Checking
// the closest existing ancestor prevents a symlinked clone parent from escaping.
func confined(root, path string) (string, error) {
	path = filepath.Clean(path)
	if !inside(root, path) {
		return "", failure("invalid_path", "The path leaves the instance workspace.")
	}
	current := path
	var missing []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", failure("invalid_path", "The path cannot be inspected.")
		}
		missing = append(missing, filepath.Base(current))
		parent := filepath.Dir(current)
		if parent == current {
			return "", failure("invalid_path", "The path cannot be resolved.")
		}
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil || !inside(root, resolved) {
		return "", failure("invalid_path", "A symbolic link leaves the instance workspace.")
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return resolved, nil
}

func (s *Service) projectPath(path string) (string, string, error) {
	rel, err := relativePath(path, true)
	if err != nil {
		return "", "", err
	}
	abs, err := confined(s.root, filepath.Join(s.root, filepath.FromSlash(rel)))
	return rel, abs, err
}

func (s *Service) repository(ctx context.Context, path string) (string, error) {
	_, dir, err := s.projectPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", failure("not_repository", "The project directory is unavailable.")
	}
	metadata := filepath.Join(dir, ".git")
	metadata, err = confined(s.root, metadata)
	if err != nil {
		return "", err
	}
	info, err = os.Stat(metadata)
	if err != nil {
		return "", failure("not_repository", "The selected directory is not a working Git repository.")
	}
	if !info.IsDir() {
		data, err := readBounded(metadata, 4096)
		if err != nil || !strings.HasPrefix(string(data), "gitdir: ") {
			return "", failure("invalid_path", "Invalid Git metadata pointer.")
		}
		metadata = strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir: "))
		if !filepath.IsAbs(metadata) {
			metadata = filepath.Join(dir, metadata)
		}
	}
	metadata, err = confined(s.root, metadata)
	if err != nil {
		return "", err
	}
	if err = s.validateMetadata(ctx, metadata); err != nil {
		return "", err
	}
	commonFile := filepath.Join(metadata, "commondir")
	if _, err := os.Lstat(commonFile); err == nil {
		data, err := readBounded(commonFile, 4096)
		if err != nil {
			return "", failure("invalid_path", "Invalid shared Git metadata.")
		}
		common := strings.TrimSpace(string(data))
		if !filepath.IsAbs(common) {
			common = filepath.Join(metadata, common)
		}
		common, err = confined(s.root, common)
		if err != nil {
			return "", err
		}
		if err = s.validateMetadata(ctx, common); err != nil {
			return "", err
		}
	}
	top, err := runGit(ctx, dir, streamLimit, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	worktree, err := filepath.EvalSymlinks(strings.TrimSuffix(top.output, "\n"))
	if err != nil || worktree != dir {
		return "", failure("invalid_path", "The Git working tree must match the selected project directory.")
	}
	return dir, nil
}

func (s *Service) validateMetadata(ctx context.Context, dir string) error {
	count := 0
	visited := map[string]bool{}
	var walk func(string) error
	walk = func(dir string) error {
		if visited[dir] {
			return nil
		}
		visited[dir] = true
		return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return failure("invalid_path", "Git metadata cannot be inspected.")
			}
			if ctx.Err() != nil {
				return contextError(ctx.Err())
			}
			count++
			if count > 100000 {
				return failure("output_limit", "Git metadata exceeds the inspection limit.")
			}
			if !d.IsDir() && !d.Type().IsRegular() && d.Type()&os.ModeSymlink == 0 {
				return failure("invalid_path", "Git metadata contains a special filesystem entry.")
			}
			if d.Type()&os.ModeSymlink != 0 {
				resolved, err := confined(s.root, path)
				if err != nil {
					return err
				}
				if info, err := os.Stat(resolved); err != nil {
					return failure("invalid_path", "Invalid Git metadata link.")
				} else if info.IsDir() {
					return walk(resolved)
				}
			}
			if d.Name() == "alternates" && filepath.Base(filepath.Dir(path)) == "info" {
				data, err := readBounded(path, 64*1024)
				if err != nil {
					return failure("invalid_path", "Invalid Git object alternates.")
				}
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					if line == "" {
						continue
					}
					if !filepath.IsAbs(line) {
						line = filepath.Join(filepath.Dir(filepath.Dir(path)), line)
					}
					resolved, err := confined(s.root, line)
					if err != nil {
						return err
					}
					if err = walk(resolved); err != nil {
						return err
					}
				}
			}
			return nil
		})
	}
	return walk(dir)
}

func hasControl(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }

// remote permits built-in network transports and local repositories confined to
// this workspace. It excludes remote helpers and option syntax. HTTP URL
// credentials remain private Git configuration; publicRemote strips them.
func (s *Service) remote(raw, base string) (string, error) {
	if raw == "" || len(raw) > 4096 || hasControl(raw) || strings.HasPrefix(raw, "-") || strings.Contains(raw, "\\") {
		return "", failure("invalid_remote", "Use an HTTP, HTTPS, SSH or workspace-local Git remote.")
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
			return "", failure("invalid_remote", "Invalid Git remote URL.")
		}
		if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" {
			return "", failure("invalid_remote", "The Git remote protocol is not permitted.")
		}
		if u.User != nil && u.Scheme == "ssh" {
			if _, pass := u.User.Password(); pass {
				return "", failure("invalid_remote", "Configure SSH keys or an SSH agent in this instance.")
			}
		}
		return raw, nil
	}
	if i := strings.IndexByte(raw, ':'); i >= 0 {
		host := raw[:i]
		if i == 0 || strings.ContainsAny(host, "/ ") || strings.HasPrefix(raw[i+1:], ":") || raw[i+1:] == "" {
			return "", failure("invalid_remote", "Invalid SSH Git remote.")
		}
		return raw, nil
	}
	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	// macOS commonly exposes temporary workspaces through /var -> /private/var.
	// Canonicalize an absolute remote before comparing it with the canonical root.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	path, err := confined(s.root, path)
	if err != nil {
		return "", failure("invalid_remote", "Local Git remotes must stay in the instance workspace.")
	}
	return path, nil
}

func publicRemote(remote string) string {
	if strings.Contains(remote, "://") {
		if u, err := url.Parse(remote); err == nil {
			u.User = nil
			return u.String()
		}
	}
	if colon := strings.IndexByte(remote, ':'); colon >= 0 {
		if at := strings.LastIndexByte(remote[:colon], '@'); at >= 0 {
			return remote[at+1:]
		}
	}
	return remote
}
