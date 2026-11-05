package workspacegit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const streamLimit = 1024 * 1024
const diffLimit = 256 * 1024

type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *cappedBuffer) String() string { return b.buffer.String() }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	available := b.limit - b.buffer.Len()
	if n > available {
		b.overflow = true
		p = p[:available]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

type gitResult struct {
	output    string
	truncated bool
}

func contextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return failure("timeout", "Git exceeded its operation deadline. Check the repository or remote and retry.")
	}
	return failure("canceled", "The Git operation was canceled.")
}

func gitEnvironment() []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			continue
		}
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_NAMESPACE", "GIT_PREFIX", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_EXTERNAL_DIFF", "GIT_DIFF_OPTS", "GIT_PAGER", "GIT_TERMINAL_PROMPT", "GIT_OPTIONAL_LOCKS", "GIT_LITERAL_PATHSPECS", "GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS", "LC_ALL", "GCM_INTERACTIVE":
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "GCM_INTERACTIVE=never", "LC_ALL=C")
}

func runGit(ctx context.Context, dir string, limit int, stdin string, args ...string) (gitResult, error) {
	env := gitEnvironment()
	operation := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		operation = args[i]
		break
	}
	if operation == "clone" || operation == "fetch" || operation == "push" || operation == "pull" {
		sshCommand := os.Getenv("GIT_SSH_COMMAND")
		if sshCommand == "" {
			configured, err := runGit(ctx, dir, streamLimit, "", "config", "--get", "core.sshCommand")
			if err == nil {
				sshCommand = strings.TrimSpace(configured.output)
			} else if e, ok := err.(*Error); !ok || e.Code != "git_failed" {
				return gitResult{}, err
			}
		}
		if sshCommand == "" {
			if executable := os.Getenv("GIT_SSH"); executable != "" {
				sshCommand = "'" + strings.ReplaceAll(executable, "'", "'\\''") + "'"
			} else {
				sshCommand = "ssh"
			}
		}
		// Keep the instance's configured identity/key command while preventing
		// OpenSSH from opening interactive password and host-trust prompts.
		env = append(env, "GIT_SSH_COMMAND="+sshCommand+" -oBatchMode=yes", "GIT_SSH_VARIANT=ssh")
	}
	base := []string{"--no-pager", "--literal-pathspecs", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.interactive=false", "-c", "protocol.allow=never", "-c", "protocol.http.allow=always", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always", "-c", "protocol.file.allow=always", "-c", "protocol.ext.allow=never", "-c", "diff.external=", "-c", "diff.trustExitCode=false"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	// Git can spawn SSH and credential helpers. Cancel the complete process group
	// and bound pipe-drain time so migration can join the active helper operation.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	stdout := &cappedBuffer{limit: limit}
	stderr := &cappedBuffer{limit: streamLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	result := gitResult{output: stdout.String(), truncated: stdout.overflow}
	if ctx.Err() != nil {
		return result, contextError(ctx.Err())
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.ENOEXEC) {
		return result, failure("git_unavailable", "Git is not installed in this instance runtime.")
	}
	if err != nil {
		return result, classifyGitError(stderr.String())
	}
	if stderr.overflow || (stdout.overflow && limit != diffLimit) {
		return result, failure("output_limit", "Git output exceeds the operation limit. Narrow the request.")
	}
	return result, nil
}

func classifyGitError(stderr string) error {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "author identity unknown"), strings.Contains(s, "unable to auto-detect email"), strings.Contains(s, "empty ident name"), strings.Contains(s, "no email was given"), strings.Contains(s, "no name was given"):
		return failure("missing_identity", "Configure user.name and user.email in this instance before committing.")
	case strings.Contains(s, "authentication failed"), strings.Contains(s, "could not read username"), strings.Contains(s, "could not read password"), strings.Contains(s, "unable to get password"), strings.Contains(s, "permission denied"), strings.Contains(s, "terminal prompts disabled"), strings.Contains(s, "host key verification failed"):
		return failure("credentials", "Git authentication failed. Configure the instance Git credentials and SSH trust settings.")
	case strings.Contains(s, "conflict"), strings.Contains(s, "not possible to fast-forward"), strings.Contains(s, "non-fast-forward"), strings.Contains(s, "[rejected]"), strings.Contains(s, "would be overwritten"), strings.Contains(s, "unmerged"):
		return failure("conflict", "Git cannot complete this operation without resolving repository or remote changes.")
	case strings.Contains(s, "nothing to commit"), strings.Contains(s, "no changes added to commit"):
		return failure("nothing_to_commit", "Stage changes before creating a commit.")
	case strings.Contains(s, "no upstream"), strings.Contains(s, "no configured push destination"), strings.Contains(s, "no such remote"):
		return failure("no_remote", "Configure a Git remote and upstream branch in this instance.")
	default:
		return failure("git_failed", "Git could not complete the operation. Check the repository and instance Git configuration.")
	}
}

func readBounded(path string, limit int) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("file exceeds limit")
	}
	return data, nil
}

func (s *Service) status(ctx context.Context, dir string) (*Status, error) {
	res, err := runGit(ctx, dir, streamLimit, "", "status", "--porcelain=v1", "-z", "--branch", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(res.output, "\x00")
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "## ") || fields[len(fields)-1] != "" {
		return nil, failure("invalid_output", "Git returned an incomplete status.")
	}
	status := &Status{Changes: []Change{}}
	header := strings.TrimPrefix(fields[0], "## ")
	switch {
	case strings.HasPrefix(header, "HEAD "):
		status.Detached = true
	case strings.HasPrefix(header, "No commits yet on "):
		status.Branch = strings.TrimPrefix(header, "No commits yet on ")
	case strings.HasPrefix(header, "Initial commit on "):
		status.Branch = strings.TrimPrefix(header, "Initial commit on ")
	default:
		branch, _, _ := strings.Cut(header, "...")
		status.Branch = branch
		if start := strings.LastIndex(header, " ["); start >= 0 {
			for _, count := range strings.Split(strings.TrimSuffix(header[start+2:], "]"), ", ") {
				parts := strings.Fields(count)
				if len(parts) != 2 {
					continue
				}
				n, e := strconv.Atoi(parts[1])
				if e != nil {
					return nil, failure("invalid_output", "Invalid Git tracking status.")
				}
				if parts[0] == "ahead" {
					status.Ahead = n
				}
				if parts[0] == "behind" {
					status.Behind = n
				}
			}
		}
	}
	for i := 1; i < len(fields)-1; i++ {
		entry := fields[i]
		if len(entry) < 4 || entry[2] != ' ' {
			return nil, failure("invalid_output", "Git returned an incomplete change record.")
		}
		c := Change{Path: entry[3:], IndexStatus: entry[:1], WorktreeStatus: entry[1:2], Staged: entry[0] != ' ' && entry[0] != '?', Untracked: entry[:2] == "??"}
		if strings.ContainsAny(entry[:2], "RC") {
			i++
			if i >= len(fields)-1 {
				return nil, failure("invalid_output", "Git returned an incomplete rename record.")
			}
			c.PreviousPath = fields[i]
		}
		if c.Untracked && s.managementPath(dir, c.Path) {
			continue
		}
		status.Changes = append(status.Changes, c)
	}
	for _, staged := range []bool{false, true} {
		args := []string{"diff", "--no-ext-diff", "--no-textconv", "--numstat", "-z"}
		if staged {
			args = append(args, "--cached")
		}
		args = append(args, "--")
		res, err := runGit(ctx, dir, streamLimit, "", args...)
		if err != nil {
			return nil, err
		}
		if err = addNumstat(status, res.output); err != nil {
			return nil, err
		}
	}
	return status, nil
}

func addNumstat(status *Status, output string) error {
	if output == "" {
		return nil
	}
	parts := strings.Split(output, "\x00")
	if parts[len(parts)-1] != "" {
		return failure("invalid_output", "Git returned incomplete line counts.")
	}
	for i := 0; i < len(parts)-1; i++ {
		fields := strings.SplitN(parts[i], "\t", 3)
		if len(fields) != 3 {
			return failure("invalid_output", "Git returned invalid line counts.")
		}
		path := fields[2]
		if path == "" {
			if i+2 >= len(parts)-1 {
				return failure("invalid_output", "Git returned incomplete rename counts.")
			}
			path = parts[i+2]
			i += 2
		}
		add, errA := strconv.Atoi(fields[0])
		del, errD := strconv.Atoi(fields[1])
		if fields[0] == "-" && fields[1] == "-" {
			add = 0
			del = 0
		} else if errA != nil || errD != nil {
			return failure("invalid_output", "Git returned invalid line counts.")
		}
		for j := range status.Changes {
			if status.Changes[j].Path == path {
				status.Changes[j].Additions += add
				status.Changes[j].Deletions += del
				break
			}
		}
	}
	return nil
}

func (s *Service) managementPath(dir, path string) bool {
	abs := filepath.Join(dir, filepath.FromSlash(path))
	return inside(filepath.Join(s.root, ".clawmanager"), abs)
}
