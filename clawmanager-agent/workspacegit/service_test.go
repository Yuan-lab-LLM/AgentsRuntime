package workspacegit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeTest(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func repoTest(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "init", "-b", "main")
	gitTest(t, dir, "config", "user.name", "Workbench Test")
	gitTest(t, dir, "config", "user.email", "workbench@example.test")
	writeTest(t, filepath.Join(dir, "one.txt"), "one\n")
	writeTest(t, filepath.Join(dir, "two.txt"), "two\n")
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-m", "initial")
	return dir
}

func executeTest(t *testing.T, s *Service, req Request) Response {
	t.Helper()
	res, err := s.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("%s: %v", req.Operation, err)
	}
	return res
}

func errorTest(t *testing.T, s *Service, req Request, code string) {
	t.Helper()
	_, err := s.Execute(context.Background(), req)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("%s: want %s, got %v", req.Operation, code, err)
	}
}

func TestLocalCommitLeavesOtherChangesAndPushes(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitTest(t, root, "init", "--bare", remote)
	repo := repoTest(t, root, "project")
	gitTest(t, repo, "remote", "add", "origin", remote)
	gitTest(t, repo, "push", "-u", "origin", "main")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "project"})
	writeTest(t, filepath.Join(repo, "one.txt"), "one changed\n")
	writeTest(t, filepath.Join(repo, "two.txt"), "two changed\n")
	executeTest(t, s, Request{Operation: "stage", Path: "project", Paths: []string{"one.txt"}})
	executeTest(t, s, Request{Operation: "commit", Path: "project", Message: "commit only one; $(touch should-not-exist)"})
	res := executeTest(t, s, Request{Operation: "status", Path: "project"})
	if res.Status == nil || len(res.Status.Changes) != 1 || res.Status.Changes[0].Path != "two.txt" || res.Status.Changes[0].Staged {
		t.Fatalf("remaining changes: %+v", res.Status)
	}
	executeTest(t, s, Request{Operation: "push", Path: "project"})
	if got, want := gitTest(t, root, "--git-dir", remote, "rev-parse", "refs/heads/main"), gitTest(t, repo, "rev-parse", "HEAD"); got != want {
		t.Fatalf("remote %s != local %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(repo, "should-not-exist")); !os.IsNotExist(err) {
		t.Fatal("commit message executed")
	}
}

func TestRegistryCloneImportPersistence(t *testing.T) {
	root := t.TempDir()
	repo := repoTest(t, root, "original")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "original", Name: "Original"})
	executeTest(t, s, Request{Operation: "clone", Path: "nested/copy", URL: repo, Name: "Copy"})
	s, err = New(root)
	if err != nil {
		t.Fatal(err)
	}
	res := executeTest(t, s, Request{Operation: "projects"})
	if len(res.Projects) != 2 || res.Projects[0].Path != "nested/copy" || res.Projects[1].Name != "Original" {
		t.Fatalf("projects: %+v", res.Projects)
	}
	errorTest(t, s, Request{Operation: "clone", Path: "original", URL: repo}, "already_exists")
	if _, err := os.Stat(filepath.Join(root, ".clawmanager", "git-projects.json")); err != nil {
		t.Fatal(err)
	}
}

func TestChangesDiffUnstageAndRename(t *testing.T) {
	root := t.TempDir()
	repo := repoTest(t, root, "repo")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	writeTest(t, filepath.Join(repo, "one.txt"), "staged line\n")
	executeTest(t, s, Request{Operation: "stage", Path: "repo", Paths: []string{"one.txt"}})
	writeTest(t, filepath.Join(repo, "one.txt"), "unstaged line\n")
	for _, tc := range []struct {
		staged bool
		want   string
	}{{true, "+staged line"}, {false, "+unstaged line"}} {
		res := executeTest(t, s, Request{Operation: "diff", Path: "repo", Staged: tc.staged})
		if !strings.Contains(res.Diff, tc.want) {
			t.Fatalf("diff: %q", res.Diff)
		}
	}
	executeTest(t, s, Request{Operation: "unstage", Path: "repo", Paths: []string{"one.txt"}})
	gitTest(t, repo, "mv", "two.txt", "renamed\nfile.txt")
	writeTest(t, filepath.Join(repo, "--odd name.txt"), "new\n")
	res := executeTest(t, s, Request{Operation: "status", Path: "repo"})
	foundRename, foundUntracked := false, false
	for _, c := range res.Status.Changes {
		if c.Path == "renamed\nfile.txt" && c.PreviousPath == "two.txt" {
			foundRename = true
		}
		if c.Path == "--odd name.txt" && c.Untracked {
			foundUntracked = true
		}
	}
	if !foundRename || !foundUntracked {
		t.Fatalf("changes: %+v", res.Status.Changes)
	}
	executeTest(t, s, Request{Operation: "stage", Path: "repo", Paths: []string{"--odd name.txt"}})
}

func TestBranchesHistoryAndDirtyProtection(t *testing.T) {
	root := t.TempDir()
	repo := repoTest(t, root, "repo")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	executeTest(t, s, Request{Operation: "create-branch", Path: "repo", Branch: "feature/demo"})
	executeTest(t, s, Request{Operation: "checkout", Path: "repo", Branch: "feature/demo"})
	if got := executeTest(t, s, Request{Operation: "status", Path: "repo"}).Status.Branch; got != "feature/demo" {
		t.Fatalf("branch %q", got)
	}
	res := executeTest(t, s, Request{Operation: "branches", Path: "repo"})
	if len(res.Branches) != 2 {
		t.Fatalf("branches %+v", res.Branches)
	}
	res = executeTest(t, s, Request{Operation: "history", Path: "repo", Limit: 1})
	if len(res.History) != 1 || res.History[0].Message != "initial" {
		t.Fatalf("history %+v", res.History)
	}
	writeTest(t, filepath.Join(repo, "untracked"), "keep")
	errorTest(t, s, Request{Operation: "checkout", Path: "repo", Branch: "main"}, "dirty_worktree")
	errorTest(t, s, Request{Operation: "pull", Path: "repo"}, "dirty_worktree")
	if err := os.Remove(filepath.Join(repo, "untracked")); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "checkout", "--detach")
	if !executeTest(t, s, Request{Operation: "status", Path: "repo"}).Status.Detached {
		t.Fatal("expected detached")
	}
	errorTest(t, s, Request{Operation: "create-branch", Path: "repo", Branch: "--upload-pack=evil"}, "invalid_ref")
}

func TestUnbornAndMissingIdentity(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	gitTest(t, root, "init", "-b", "main")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import"})
	if res := executeTest(t, s, Request{Operation: "history"}); len(res.History) != 0 {
		t.Fatal(res.History)
	}
	writeTest(t, filepath.Join(root, "new"), "new")
	executeTest(t, s, Request{Operation: "stage", Paths: []string{"new"}})
	executeTest(t, s, Request{Operation: "unstage", Paths: []string{"new"}})
	if c := executeTest(t, s, Request{Operation: "status"}).Status.Changes; c[0].Staged || !c[0].Untracked {
		t.Fatal(c)
	}
	executeTest(t, s, Request{Operation: "stage", Paths: []string{"new"}})
	errorTest(t, s, Request{Operation: "commit", Message: "new"}, "missing_identity")
}

func TestUnsafePathsMetadataAndRemotes(t *testing.T) {
	root := t.TempDir()
	outside := repoTest(t, t.TempDir(), "outside")
	repo := repoTest(t, root, "repo")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", outside, "a/../../outside", ".clawmanager"} {
		errorTest(t, s, Request{Operation: "import", Path: path}, "invalid_path")
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	errorTest(t, s, Request{Operation: "import", Path: "escape"}, "invalid_path")
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	for _, path := range []string{"../outside", "/etc/passwd", ":(top)*", ".git/config"} {
		errorTest(t, s, Request{Operation: "stage", Path: "repo", Paths: []string{path}}, "invalid_path")
	}
	for _, remote := range []string{"ext::sh -c evil", "file:///etc/passwd", "-ufoo", "http://example.test/repo\nsecret"} {
		errorTest(t, s, Request{Operation: "clone", Path: "new", URL: remote}, "invalid_remote")
	}
	gitTest(t, repo, "remote", "add", "origin", "ext::sh -c evil")
	errorTest(t, s, Request{Operation: "push", Path: "repo"}, "invalid_remote")
	if err := os.Rename(filepath.Join(repo, ".git"), filepath.Join(root, "saved-git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, ".git"), filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
	errorTest(t, s, Request{Operation: "status", Path: "repo"}, "invalid_path")
}

func TestOutputLimitsAreExplicit(t *testing.T) {
	root := t.TempDir()
	repo := repoTest(t, root, "repo")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	writeTest(t, filepath.Join(repo, "one.txt"), strings.Repeat("large diff line\n", 40000))
	res := executeTest(t, s, Request{Operation: "diff", Path: "repo"})
	if !res.Truncated || len(res.Diff) > diffLimit {
		t.Fatalf("diff len=%d truncated=%v", len(res.Diff), res.Truncated)
	}
	cmd := exec.Command("git", "commit", "--allow-empty", "-F", "-")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader("large body\n\n" + strings.Repeat("message line\n", 100000))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("large commit: %v: %s", err, out)
	}
	errorTest(t, s, Request{Operation: "history", Path: "repo", Limit: 1}, "output_limit")
	for i := 0; i < 5000; i++ {
		writeTest(t, filepath.Join(repo, strconv.Itoa(i)+strings.Repeat("x", 220)), "")
	}
	errorTest(t, s, Request{Operation: "status", Path: "repo"}, "output_limit")
}

func TestConcurrentRegistryWritersAndTampering(t *testing.T) {
	root := t.TempDir()
	const count = 6
	for i := 0; i < count; i++ {
		repoTest(t, root, strconv.Itoa(i))
	}
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := New(root)
			if err == nil {
				_, err = s.Execute(context.Background(), Request{Operation: "import", Path: strconv.Itoa(i)})
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(executeTest(t, s, Request{Operation: "projects"}).Projects); got != count {
		t.Fatalf("got %d projects", got)
	}
	registryPath := filepath.Join(root, ".clawmanager", "git-projects.json")
	writeTest(t, registryPath, `{"version":1,"projects":[{"path":"../escape","name":"bad"}]}`)
	errorTest(t, s, Request{Operation: "projects"}, "invalid_path")
	writeTest(t, registryPath, strings.Repeat("x", streamLimit+1))
	errorTest(t, s, Request{Operation: "projects"}, "invalid_registry")
	if err := os.Remove(registryPath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "registry")
	writeTest(t, outside, "preserve")
	if err := os.Symlink(outside, registryPath); err != nil {
		t.Fatal(err)
	}
	errorTest(t, s, Request{Operation: "import", Path: "0"}, "invalid_path")
	if data, err := os.ReadFile(outside); err != nil || string(data) != "preserve" {
		t.Fatal("registry overwrote outside file")
	}
}

func TestMalformedBranchExpansionAndMetadataPointers(t *testing.T) {
	root := t.TempDir()
	repo := repoTest(t, root, "repo")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	gitTest(t, repo, "switch", "-c", "other")
	gitTest(t, repo, "switch", "main")
	errorTest(t, s, Request{Operation: "checkout", Path: "repo", Branch: "@{-1}"}, "invalid_ref")
	metadata := filepath.Join(repo, ".git")
	if err := os.Rename(metadata, filepath.Join(root, "saved")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "gitfile")
	writeTest(t, outside, "gitdir: "+filepath.Join(root, "saved")+"\n")
	if err := os.Symlink(outside, metadata); err != nil {
		t.Fatal(err)
	}
	errorTest(t, s, Request{Operation: "status", Path: "repo"}, "invalid_path")
}

func TestMetadataObjectLinksAndLocalRemoteCannotEscape(t *testing.T) {
	root := t.TempDir()
	repo := repoTest(t, root, "repo")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	out := t.TempDir()
	shared := filepath.Join(root, "shared")
	if err := os.Mkdir(shared, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(shared, "aa")); err != nil {
		t.Fatal(err)
	}
	objects := filepath.Join(repo, ".git", "objects")
	if err := os.Rename(objects, filepath.Join(root, "saved-objects")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, objects); err != nil {
		t.Fatal(err)
	}
	errorTest(t, s, Request{Operation: "status", Path: "repo"}, "invalid_path")
	root2 := t.TempDir()
	remote := repoTest(t, root2, "source")
	s2, err := New(root2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(remote, ".git", "objects", "aa")); err != nil {
		t.Fatal(err)
	}
	errorTest(t, s2, Request{Operation: "clone", Path: "copy", URL: remote}, "invalid_path")
}

func TestCleanPullAndDivergenceProtectLocalCommits(t *testing.T) {
	root := t.TempDir()
	source := repoTest(t, root, "source")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "clone", Path: "copy", URL: source})
	copy := filepath.Join(root, "copy")
	gitTest(t, copy, "config", "user.name", "Test")
	gitTest(t, copy, "config", "user.email", "test@example.test")
	writeTest(t, filepath.Join(source, "one.txt"), "remote update\n")
	gitTest(t, source, "commit", "-am", "update")
	executeTest(t, s, Request{Operation: "pull", Path: "copy"})
	if data, err := os.ReadFile(filepath.Join(copy, "one.txt")); err != nil || string(data) != "remote update\n" {
		t.Fatal("pull did not update working copy")
	}
	writeTest(t, filepath.Join(source, "one.txt"), "remote divergent\n")
	gitTest(t, source, "commit", "-am", "remote diverge")
	writeTest(t, filepath.Join(copy, "one.txt"), "local divergent\n")
	gitTest(t, copy, "commit", "-am", "local diverge")
	before := gitTest(t, copy, "rev-parse", "HEAD")
	errorTest(t, s, Request{Operation: "pull", Path: "copy"}, "conflict")
	if got := gitTest(t, copy, "rev-parse", "HEAD"); got != before {
		t.Fatal("pull changed local HEAD on divergence")
	}
}

func TestCancellationMissingGitAndSafeErrors(t *testing.T) {
	root := t.TempDir()
	repoTest(t, root, "repo")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.Execute(ctx, Request{Operation: "status", Path: "repo"})
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != "canceled" {
		t.Fatalf("canceled: %v", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = s.Execute(ctx, Request{Operation: "status", Path: "repo"})
	if !errors.As(err, &typed) || typed.Code != "timeout" {
		t.Fatalf("timeout: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	errorTest(t, s, Request{Operation: "status", Path: "repo"}, "git_unavailable")
	err = classifyGitError("fatal: https://user:topsecret@example.test/repo\x1b[31m authentication failed")
	if strings.Contains(err.Error(), "topsecret") || hasControl(err.Error()) {
		t.Fatalf("unsafe error %q", err.Error())
	}
}

func TestConfiguredWorktreeAndRewrittenRemoteCannotEscape(t *testing.T) {
	root := t.TempDir()
	repo := repoTest(t, root, "repo")
	outside := repoTest(t, t.TempDir(), "outside")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	gitTest(t, repo, "config", "core.worktree", outside)
	errorTest(t, s, Request{Operation: "status", Path: "repo"}, "invalid_path")
	gitTest(t, repo, "config", "--unset", "core.worktree")
	gitTest(t, root, "init", "--bare", "remote.git")
	gitTest(t, repo, "config", "url."+outside+".insteadOf", "https://safe.example.test/")
	gitTest(t, repo, "remote", "add", "origin", "https://safe.example.test/")
	errorTest(t, s, Request{Operation: "push", Path: "repo"}, "invalid_remote")
}

func TestIgnoredFilesSurviveCheckoutAndPull(t *testing.T) {
	root := t.TempDir()
	source := repoTest(t, root, "source")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(source, ".gitignore"), "ignored\n")
	gitTest(t, source, "add", ".gitignore")
	gitTest(t, source, "commit", "-m", "ignore")
	executeTest(t, s, Request{Operation: "clone", Path: "copy", URL: source})
	copy := filepath.Join(root, "copy")
	gitTest(t, source, "switch", "-c", "with-file")
	writeTest(t, filepath.Join(source, "ignored"), "remote tracked")
	gitTest(t, source, "add", "-f", "ignored")
	gitTest(t, source, "commit", "-m", "tracked ignored file")
	gitTest(t, copy, "fetch", "origin")
	gitTest(t, copy, "branch", "with-file", "origin/with-file")
	writeTest(t, filepath.Join(copy, "ignored"), "keep local ignored")
	errorTest(t, s, Request{Operation: "checkout", Path: "copy", Branch: "with-file"}, "conflict")
	gitTest(t, source, "switch", "main")
	gitTest(t, source, "merge", "--ff-only", "with-file")
	errorTest(t, s, Request{Operation: "pull", Path: "copy"}, "conflict")
	if data, err := os.ReadFile(filepath.Join(copy, "ignored")); err != nil || string(data) != "keep local ignored" {
		t.Fatal("ignored work was overwritten")
	}
}

func TestHooksAndExternalDiffAreDisabled(t *testing.T) {
	root := t.TempDir()
	repo := repoTest(t, root, "repo")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	marker := filepath.Join(root, "executed")
	for _, name := range []string{"pre-commit", "post-commit", "post-checkout"} {
		path := filepath.Join(repo, ".git", "hooks", name)
		writeTest(t, path, "#!/bin/sh\ntouch '"+marker+"'\nexit 1\n")
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	gitTest(t, repo, "config", "diff.external", "touch '"+marker+"'")
	writeTest(t, filepath.Join(repo, "one.txt"), "changed\n")
	executeTest(t, s, Request{Operation: "diff", Path: "repo"})
	executeTest(t, s, Request{Operation: "stage", Path: "repo", Paths: []string{"one.txt"}})
	executeTest(t, s, Request{Operation: "commit", Path: "repo", Message: "safe"})
	executeTest(t, s, Request{Operation: "create-branch", Path: "repo", Branch: "other"})
	executeTest(t, s, Request{Operation: "checkout", Path: "repo", Branch: "other"})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("hook or external diff ran")
	}
}

func TestRemoteCredentialsAndCancellation(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	root := t.TempDir()
	repo := repoTest(t, root, "repo")
	gitTest(t, repo, "remote", "add", "origin", server.URL+"/repo.git")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	errorTest(t, s, Request{Operation: "push", Path: "repo"}, "credentials")
	started := filepath.Join(root, "started")
	marker := filepath.Join(root, "credential-writer-survived")
	helper := filepath.Join(root, "helper.sh")
	writeTest(t, helper, "#!/bin/sh\ntouch '"+started+"'\nsleep 1\ntouch '"+marker+"'\n")
	if err = os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "config", "credential.helper", "!"+helper)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Execute(ctx, Request{Operation: "push", Path: "repo"}); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("Git did not invoke the existing credential helper")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		var typed *Error
		if !errors.As(err, &typed) || typed.Code != "canceled" {
			t.Fatalf("canceled remote: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled Git did not join")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("cancellation left the credential subprocess running")
	}
}

func TestSSHIsNoninteractiveAndKeepsConfiguredCredentials(t *testing.T) {
	root := t.TempDir()
	repo := repoTest(t, root, "repo")
	argsFile := filepath.Join(root, "ssh-args")
	ssh := filepath.Join(root, "ssh.sh")
	writeTest(t, ssh, "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '"+argsFile+"'\nexit 1\n")
	if err := os.Chmod(ssh, 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "config", "core.sshCommand", ssh+" -i instance-key")
	gitTest(t, repo, "remote", "add", "origin", "ssh://git@example.test/repo")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	_, _ = s.Execute(context.Background(), Request{Operation: "push", Path: "repo"})
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "instance-key") || !strings.Contains(string(args), "BatchMode=yes") {
		t.Fatalf("SSH args missing existing credentials or noninteractive policy: %q", args)
	}
}

func TestSpecialMetadataCannotBlockPathValidation(t *testing.T) {
	root := t.TempDir()
	repo := repoTest(t, root, "repo")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	metadata := filepath.Join(repo, ".git")
	if err = os.Rename(metadata, filepath.Join(root, "metadata")); err != nil {
		t.Fatal(err)
	}
	if err = syscall.Mkfifo(metadata, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Execute(context.Background(), Request{Operation: "status", Path: "repo"})
		done <- err
	}()
	select {
	case err := <-done:
		var typed *Error
		if !errors.As(err, &typed) || typed.Code != "invalid_path" {
			t.Fatalf("special metadata: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("metadata validation blocked on a FIFO")
	}
}

func TestExistingURLCredentialsAreUsedButNeverDisplayedOrRegistered(t *testing.T) {
	root := t.TempDir()
	repo := repoTest(t, root, "repo")
	gitTest(t, repo, "remote", "add", "origin", "https://user:topsecret@example.test/repo.git")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	res := executeTest(t, s, Request{Operation: "import", Path: "repo"})
	if res.Project.Remote != "https://example.test/repo.git" {
		t.Fatalf("unsafe displayed remote %q", res.Project.Remote)
	}
	data, err := os.ReadFile(filepath.Join(root, ".clawmanager", "git-projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "topsecret") {
		t.Fatal("registry persisted URL credentials")
	}
}

func TestPushHonorsConfiguredPushRemote(t *testing.T) {
	root := t.TempDir()
	repo := repoTest(t, root, "repo")
	for _, name := range []string{"upstream", "fork"} {
		gitTest(t, root, "init", "--bare", name+".git")
		gitTest(t, repo, "remote", "add", name, filepath.Join(root, name+".git"))
	}
	gitTest(t, repo, "push", "-u", "upstream", "main")
	gitTest(t, repo, "config", "branch.main.pushRemote", "fork")
	before := gitTest(t, repo, "rev-parse", "HEAD")
	writeTest(t, filepath.Join(repo, "one.txt"), "fork only\n")
	gitTest(t, repo, "commit", "-am", "fork update")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import", Path: "repo"})
	executeTest(t, s, Request{Operation: "push", Path: "repo"})
	if got := gitTest(t, root, "--git-dir", filepath.Join(root, "upstream.git"), "rev-parse", "refs/heads/main"); got != before {
		t.Fatal("push changed the upstream instead of the configured fork")
	}
	if got, want := gitTest(t, root, "--git-dir", filepath.Join(root, "fork.git"), "rev-parse", "refs/heads/main"), gitTest(t, repo, "rev-parse", "HEAD"); got != want {
		t.Fatal("configured push remote was not updated")
	}
}

func TestUnbornUnstageAfterWorktreeEditPreservesFile(t *testing.T) {
	root := t.TempDir()
	gitTest(t, root, "init", "-b", "main")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import"})
	path := filepath.Join(root, "new.txt")
	writeTest(t, path, "staged version\n")
	executeTest(t, s, Request{Operation: "stage", Paths: []string{"new.txt"}})
	writeTest(t, path, "newer unstaged version\n")
	res := executeTest(t, s, Request{Operation: "unstage", Paths: []string{"new.txt"}})
	if len(res.Status.Changes) != 1 || res.Status.Changes[0].Path != "new.txt" || !res.Status.Changes[0].Untracked || res.Status.Changes[0].Staged {
		t.Fatalf("unstaged change: %+v", res.Status.Changes)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "newer unstaged version\n" {
		t.Fatal("unstage changed the working file")
	}
	if files := gitTest(t, root, "ls-files"); files != "" {
		t.Fatalf("index still contains %q", files)
	}
}

func trackedRegistryTest(t *testing.T, repo string) {
	t.Helper()
	dir := filepath.Join(repo, ".clawmanager")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(dir, "git-projects.json"), `{"version":1,"projects":[]}`)
	writeTest(t, filepath.Join(dir, "git-projects.lock"), "tracked lock contents\n")
	gitTest(t, repo, "add", ".clawmanager/git-projects.json", ".clawmanager/git-projects.lock")
	gitTest(t, repo, "commit", "-m", "track reserved metadata")
}

func registryPreservedTest(t *testing.T, root string, registry []byte, lock os.FileInfo) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".clawmanager", "git-projects.json"))
	if err != nil || string(data) != string(registry) {
		t.Fatal("Git replaced the project registry")
	}
	after, err := os.Stat(filepath.Join(root, ".clawmanager", "git-projects.lock"))
	if err != nil || !os.SameFile(lock, after) {
		t.Fatal("Git replaced the live registry lock inode")
	}
}

func TestRootCheckoutRejectsReservedDestinationTree(t *testing.T) {
	root := t.TempDir()
	repoTest(t, root, "")
	gitTest(t, root, "switch", "-c", "reserved")
	trackedRegistryTest(t, root)
	gitTest(t, root, "switch", "main")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import"})
	registry, err := os.ReadFile(filepath.Join(root, ".clawmanager", "git-projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.Stat(filepath.Join(root, ".clawmanager", "git-projects.lock"))
	if err != nil {
		t.Fatal(err)
	}
	errorTest(t, s, Request{Operation: "checkout", Branch: "reserved"}, "reserved_metadata")
	registryPreservedTest(t, root, registry, lock)
	if branch := gitTest(t, root, "branch", "--show-current"); branch != "main" {
		t.Fatalf("checkout changed branch to %q", branch)
	}
	if projects := executeTest(t, s, Request{Operation: "projects"}).Projects; len(projects) != 1 || projects[0].Path != "" {
		t.Fatalf("registered root lost: %+v", projects)
	}
}

func TestRootCheckoutCannotOverwriteTrackedRegistryAndLock(t *testing.T) {
	root := t.TempDir()
	repoTest(t, root, "")
	trackedRegistryTest(t, root)
	registryPath := filepath.Join(root, ".clawmanager", "git-projects.json")
	lockPath := filepath.Join(root, ".clawmanager", "git-projects.lock")
	writeTest(t, registryPath, `{"version":1,"projects":[{"path":"","name":"Root"}]}`)
	gitTest(t, root, "add", ".clawmanager/git-projects.json")
	gitTest(t, root, "commit", "-m", "registered root")
	gitTest(t, root, "switch", "-c", "reserved")
	writeTest(t, registryPath, `{"version":1,"projects":[]}`)
	writeTest(t, lockPath, "replacement lock\n")
	gitTest(t, root, "add", ".clawmanager")
	gitTest(t, root, "commit", "-m", "different registry and lock")
	gitTest(t, root, "switch", "main")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	errorTest(t, s, Request{Operation: "checkout", Branch: "reserved"}, "reserved_metadata")
	registryPreservedTest(t, root, registry, lock)
	if projects := executeTest(t, s, Request{Operation: "projects"}).Projects; len(projects) != 1 {
		t.Fatal("checkout removed root registration")
	}
}

func TestRootImportRejectsTrackedReservedMetadata(t *testing.T) {
	root := t.TempDir()
	repoTest(t, root, "")
	trackedRegistryTest(t, root)
	registry, err := os.ReadFile(filepath.Join(root, ".clawmanager", "git-projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.Stat(filepath.Join(root, ".clawmanager", "git-projects.lock"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	errorTest(t, s, Request{Operation: "import"}, "reserved_metadata")
	registryPreservedTest(t, root, registry, lock)
}

func TestRootStageAndCommitRejectExternallyStagedRegistry(t *testing.T) {
	root := t.TempDir()
	repoTest(t, root, "")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import"})
	registry, err := os.ReadFile(filepath.Join(root, ".clawmanager", "git-projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.Stat(filepath.Join(root, ".clawmanager", "git-projects.lock"))
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "add", ".clawmanager/git-projects.json", ".clawmanager/git-projects.lock")
	writeTest(t, filepath.Join(root, "one.txt"), "business edit\n")
	errorTest(t, s, Request{Operation: "stage", Paths: []string{"one.txt"}}, "reserved_metadata")
	before := gitTest(t, root, "rev-parse", "HEAD")
	errorTest(t, s, Request{Operation: "commit", Message: "must not commit management metadata"}, "reserved_metadata")
	if after := gitTest(t, root, "rev-parse", "HEAD"); after != before {
		t.Fatal("commit changed HEAD")
	}
	registryPreservedTest(t, root, registry, lock)
}

func TestRootPullRejectsReservedRemoteTree(t *testing.T) {
	root := t.TempDir()
	source := repoTest(t, root, "source")
	target := filepath.Join(root, "working")
	gitTest(t, root, "clone", source, target)
	// A loopback Git server would be a valid external remote, but the focused
	// fixture uses a workspace-local source so only repository semantics matter.
	insideRemote := filepath.Join(target, "remote.git")
	gitTest(t, target, "init", "--bare", insideRemote)
	gitTest(t, source, "push", insideRemote, "main")
	gitTest(t, target, "remote", "set-url", "origin", insideRemote)
	writeTest(t, filepath.Join(target, ".git", "info", "exclude"), "remote.git/\n")
	s, err := New(target)
	if err != nil {
		t.Fatal(err)
	}
	executeTest(t, s, Request{Operation: "import"})
	registry, err := os.ReadFile(filepath.Join(target, ".clawmanager", "git-projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.Stat(filepath.Join(target, ".clawmanager", "git-projects.lock"))
	if err != nil {
		t.Fatal(err)
	}
	trackedRegistryTest(t, source)
	gitTest(t, source, "push", insideRemote, "main")
	before := gitTest(t, target, "rev-parse", "HEAD")
	errorTest(t, s, Request{Operation: "pull"}, "reserved_metadata")
	if after := gitTest(t, target, "rev-parse", "HEAD"); after != before {
		t.Fatal("pull changed HEAD")
	}
	registryPreservedTest(t, target, registry, lock)
}
