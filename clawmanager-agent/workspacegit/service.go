package workspacegit

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Service struct{ root string }

func New(root string) (*Service, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, failure("invalid_path", "The workspace root is invalid.")
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, failure("invalid_path", "The workspace root is unavailable.")
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, failure("invalid_path", "The workspace root must be an existing directory.")
	}
	return &Service{root: abs}, nil
}

func validName(name string) bool { return name != "" && len(name) <= 200 && !hasControl(name) }

func (s *Service) Execute(ctx context.Context, req Request) (Response, error) {
	switch req.Operation {
	case "projects", "import", "clone", "status", "diff", "stage", "unstage", "branches", "checkout", "create-branch", "history", "commit", "pull", "push":
	default:
		return Response{}, failure("invalid_operation", "Unsupported Git operation.")
	}
	ceiling := 15 * time.Second
	if req.Operation == "clone" || req.Operation == "pull" || req.Operation == "push" {
		ceiling = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, ceiling)
	defer cancel()
	if ctx.Err() != nil {
		return Response{}, contextError(ctx.Err())
	}
	if resolved, err := filepath.EvalSymlinks(s.root); err != nil || resolved != s.root {
		return Response{}, failure("invalid_path", "The workspace root changed or became unavailable.")
	}
	if len(req.Path) > 4096 || len(req.Paths) > 1000 || len(req.Message) > 64*1024 || len(req.Branch) > 1024 {
		return Response{}, failure("invalid_request", "The Git request exceeds its input limit.")
	}
	rel, _, err := s.projectPath(req.Path)
	if err != nil {
		return Response{}, err
	}
	req.Path = rel
	if req.Name != "" && !validName(req.Name) {
		return Response{}, failure("invalid_request", "Choose a project name without control characters, up to 200 bytes.")
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return Response{}, err
	}
	defer unlock()
	reg, err := s.readRegistry(ctx)
	if err != nil {
		return Response{}, err
	}
	if req.Operation == "projects" {
		return Response{Projects: reg.Projects}, nil
	}
	if req.Operation == "import" || req.Operation == "clone" {
		return s.register(ctx, reg, req)
	}
	var project *Project
	for i := range reg.Projects {
		if reg.Projects[i].Path == req.Path {
			project = &reg.Projects[i]
			break
		}
	}
	if project == nil {
		return Response{}, failure("not_registered", "Import the Git repository before using it in this workspace.")
	}
	dir, err := s.repository(ctx, req.Path)
	if err != nil {
		return Response{}, err
	}
	paths, err := s.filePaths(dir, req.Paths)
	if err != nil {
		return Response{}, err
	}
	res := Response{Project: project}
	if req.Operation == "stage" || req.Operation == "commit" || req.Operation == "pull" {
		if err = s.protectRegistry(ctx, dir); err != nil {
			return Response{}, err
		}
	}
	switch req.Operation {
	case "status":
		res.Status, err = s.status(ctx, dir)
	case "diff":
		args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color"}
		if req.Staged {
			args = append(args, "--cached")
		}
		args = append(args, "--")
		args = append(args, paths...)
		var out gitResult
		out, err = runGit(ctx, dir, diffLimit, "", args...)
		res.Diff = out.output
		res.Truncated = out.truncated
	case "stage", "unstage":
		if len(paths) == 0 {
			return Response{}, failure("invalid_path", "Choose explicit repository-relative files to stage or unstage.")
		}
		var args []string
		if req.Operation == "stage" {
			args = []string{"add", "--all", "--"}
		} else {
			var born bool
			born, err = hasHEAD(ctx, dir)
			if err != nil {
				break
			}
			if born {
				args = []string{"restore", "--staged", "--source=HEAD", "--"}
			} else {
				// Force applies only to index removal. Newer worktree content must
				// survive even when it differs from the staged unborn version.
				args = []string{"rm", "--cached", "--force", "--ignore-unmatch", "--"}
			}
		}
		_, err = runGit(ctx, dir, streamLimit, "", append(args, paths...)...)
		if err == nil {
			res.Status, err = s.status(ctx, dir)
		}
	case "branches":
		res.Branches, err = s.branches(ctx, dir)
	case "checkout", "create-branch":
		if err = validBranch(ctx, dir, req.Branch); err != nil {
			break
		}
		if req.Operation == "checkout" {
			if err = s.protectRegistry(ctx, dir, "refs/heads/"+req.Branch); err != nil {
				break
			}
			if err = s.requireClean(ctx, dir); err != nil {
				break
			}
			// switch accepts a local branch name and never interprets it as a path,
			// arbitrary revision, remote helper or detached commit request.
			_, err = runGit(ctx, dir, streamLimit, "", "switch", "--no-guess", "--no-overwrite-ignore", "--", req.Branch)
		} else {
			_, err = runGit(ctx, dir, streamLimit, "", "branch", "--", req.Branch)
		}
		if err == nil {
			res.Status, err = s.status(ctx, dir)
		}
	case "history":
		res.History, err = s.history(ctx, dir, req.Limit)
	case "commit":
		if strings.TrimSpace(req.Message) == "" || strings.ContainsRune(req.Message, 0) {
			return Response{}, failure("invalid_request", "Provide a nonempty commit message, up to 64 KiB.")
		}
		_, err = runGit(ctx, dir, streamLimit, req.Message, "-c", "commit.gpgSign=false", "-c", "user.useConfigOnly=true", "commit", "--no-verify", "--cleanup=verbatim", "--file=-")
		if err == nil {
			res.Status, err = s.status(ctx, dir)
			res.Message = "Local commit created."
		}
	case "pull", "push":
		if req.Operation == "pull" {
			if err = s.requireClean(ctx, dir); err != nil {
				break
			}
		}
		err = s.syncRemote(ctx, dir, req.Operation)
		if err == nil {
			res.Status, err = s.status(ctx, dir)
		}
	}
	if err != nil {
		return Response{}, err
	}
	return res, nil
}

func (s *Service) register(ctx context.Context, reg *registry, req Request) (Response, error) {
	_, dir, err := s.projectPath(req.Path)
	if err != nil {
		return Response{}, err
	}
	if req.Operation == "clone" {
		if req.Path == "" {
			return Response{}, failure("already_exists", "Cloning requires a new project directory.")
		}
		if _, err = os.Lstat(dir); err == nil {
			return Response{}, failure("already_exists", "The clone destination already exists; import it or choose a new directory.")
		}
		if !os.IsNotExist(err) {
			return Response{}, failure("invalid_path", "The clone destination cannot be inspected.")
		}
		remote, err := s.remote(req.URL, s.root)
		if err != nil {
			return Response{}, err
		}
		if err = s.validateCloneRemote(ctx, remote); err != nil {
			return Response{}, err
		}
		if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
			return Response{}, failure("invalid_path", "The clone parent directory cannot be created.")
		}
		if _, err = s.projectPathCheck(req.Path, dir); err != nil {
			return Response{}, err
		}
		if err = os.Mkdir(dir, 0700); err != nil {
			return Response{}, failure("already_exists", "The clone destination was created by another operation.")
		}
		_, err = runGit(ctx, s.root, streamLimit, "", "clone", "--no-local", "--no-hardlinks", "--template=", "--", remote, dir)
		if err != nil {
			return Response{}, err
		}
	}
	dir, err = s.repository(ctx, req.Path)
	if err != nil {
		return Response{}, err
	}
	if err = s.protectRegistry(ctx, dir); err != nil {
		return Response{}, err
	}
	name := req.Name
	if name == "" {
		name = filepath.Base(dir)
	}
	if !validName(name) {
		return Response{}, failure("invalid_request", "Provide a project name without control characters.")
	}
	remote, err := s.projectRemote(ctx, dir)
	if err != nil {
		return Response{}, err
	}
	project := Project{Path: req.Path, Name: name, Remote: remote}
	found := false
	for i := range reg.Projects {
		if reg.Projects[i].Path == req.Path {
			reg.Projects[i] = project
			found = true
			break
		}
	}
	if !found {
		reg.Projects = append(reg.Projects, project)
	}
	if err = s.saveRegistry(reg); err != nil {
		return Response{}, err
	}
	return Response{Project: &project, Projects: reg.Projects}, nil
}

func (s *Service) projectPathCheck(path, want string) (string, error) {
	_, got, err := s.projectPath(path)
	if err != nil {
		return "", err
	}
	if got != want {
		return "", failure("invalid_path", "The project path changed during the operation.")
	}
	return got, nil
}

func (s *Service) filePaths(dir string, paths []string) ([]string, error) {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if len(path) > 4096 {
			return nil, failure("invalid_path", "A repository path exceeds its input limit.")
		}
		rel, err := relativePath(path, false)
		if err != nil {
			return nil, err
		}
		abs, err := confined(s.root, filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		if !inside(dir, abs) {
			return nil, failure("invalid_path", "Repository files must stay inside the selected project.")
		}
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return nil, failure("invalid_path", "Choose individual files instead of directories.")
		}
		result = append(result, rel)
	}
	return result, nil
}

func validBranch(ctx context.Context, dir, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "refs/") || hasControl(branch) || branch == "HEAD" {
		return failure("invalid_ref", "Choose a valid local Git branch name.")
	}
	_, err := runGit(ctx, dir, streamLimit, "", "check-ref-format", "refs/heads/"+branch)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return failure("invalid_ref", "Choose a valid local Git branch name.")
	}
	return nil
}

func hasHEAD(ctx context.Context, dir string) (bool, error) {
	_, err := runGit(ctx, dir, streamLimit, "", "rev-parse", "--verify", "--quiet", "HEAD")
	if err == nil {
		return true, nil
	}
	if e, ok := err.(*Error); ok && e.Code == "git_failed" {
		return false, nil
	}
	return false, err
}

func (s *Service) requireClean(ctx context.Context, dir string) error {
	status, err := s.status(ctx, dir)
	if err != nil {
		return err
	}
	if len(status.Changes) != 0 {
		return failure("dirty_worktree", "Commit or unstage and save your local changes before switching branches or pulling.")
	}
	return nil
}

func (s *Service) branches(ctx context.Context, dir string) ([]Branch, error) {
	res, err := runGit(ctx, dir, streamLimit, "", "for-each-ref", "--format=%(refname)%00%(HEAD)%00%(upstream:short)%00", "refs/heads/", "refs/remotes/")
	if err != nil {
		return nil, err
	}
	branches := []Branch{}
	for _, line := range strings.Split(strings.TrimSuffix(res.output, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 4 || parts[3] != "" {
			return nil, failure("invalid_output", "Git returned incomplete branch information.")
		}
		b := Branch{Current: parts[1] == "*", Upstream: parts[2]}
		if strings.HasPrefix(parts[0], "refs/remotes/") {
			b.Remote = true
			b.Name = strings.TrimPrefix(parts[0], "refs/remotes/")
		} else {
			b.Name = strings.TrimPrefix(parts[0], "refs/heads/")
		}
		branches = append(branches, b)
	}
	return branches, nil
}

func (s *Service) history(ctx context.Context, dir string, limit int) ([]Commit, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return nil, failure("invalid_request", "History limit must be between 1 and 200.")
	}
	born, err := hasHEAD(ctx, dir)
	if err != nil {
		return nil, err
	}
	if !born {
		return []Commit{}, nil
	}
	res, err := runGit(ctx, dir, streamLimit, "", "log", "-z", "--no-show-signature", "--format=%H%x00%an%x00%ae%x00%at%x00%B", "--max-count="+strconv.Itoa(limit), "HEAD", "--")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(res.output, "\x00")
	if len(parts)%5 != 1 || parts[len(parts)-1] != "" {
		return nil, failure("invalid_output", "Git returned incomplete commit history.")
	}
	commits := make([]Commit, 0, (len(parts)-1)/5)
	for i := 0; i < len(parts)-1; i += 5 {
		timestamp, err := strconv.ParseInt(parts[i+3], 10, 64)
		if err != nil {
			return nil, failure("invalid_output", "Git returned an invalid commit timestamp.")
		}
		commits = append(commits, Commit{Hash: parts[i], Author: parts[i+1], AuthorEmail: parts[i+2], Timestamp: timestamp, Message: strings.TrimSuffix(parts[i+4], "\n")})
	}
	return commits, nil
}

func (s *Service) validateCloneRemote(ctx context.Context, remote string) error {
	res, err := runGit(ctx, s.root, streamLimit, "", "ls-remote", "--get-url", "--", remote)
	if err != nil {
		return err
	}
	resolved, err := s.remote(strings.TrimSpace(res.output), s.root)
	if err != nil {
		return err
	}
	return s.validateLocalRemote(ctx, resolved)
}

func (s *Service) validateLocalRemote(ctx context.Context, remote string) error {
	if !filepath.IsAbs(remote) {
		return nil
	}
	info, err := os.Stat(remote)
	if err != nil || !info.IsDir() {
		return failure("invalid_remote", "The workspace-local Git remote is unavailable.")
	}
	if _, err = os.Lstat(filepath.Join(remote, ".git")); err == nil {
		rel, err := filepath.Rel(s.root, remote)
		if err != nil {
			return failure("invalid_remote", "The local Git remote cannot be resolved.")
		}
		_, err = s.repository(ctx, filepath.ToSlash(rel))
		return err
	}
	if err = s.validateMetadata(ctx, remote); err != nil {
		return err
	}
	result, err := runGit(ctx, remote, streamLimit, "", "rev-parse", "--is-bare-repository")
	if err != nil {
		return err
	}
	if strings.TrimSpace(result.output) != "true" {
		return failure("invalid_remote", "The workspace-local remote must be a Git repository.")
	}
	return nil
}

func (s *Service) projectRemote(ctx context.Context, dir string) (string, error) {
	res, err := runGit(ctx, dir, streamLimit, "", "config", "--get", "remote.origin.url")
	if err != nil {
		if e, ok := err.(*Error); ok && e.Code == "git_failed" {
			return "", nil
		}
		return "", err
	}
	remote, err := s.remote(strings.TrimSpace(res.output), dir)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(remote) {
		rel, err := filepath.Rel(s.root, remote)
		if err != nil {
			return "", failure("invalid_remote", "The local Git remote is unavailable.")
		}
		remote = "./" + filepath.ToSlash(rel)
	}
	return publicRemote(remote), nil
}

func configValue(ctx context.Context, dir, key string) (string, error) {
	result, err := runGit(ctx, dir, streamLimit, "", "config", "--get", key)
	if e, ok := err.(*Error); ok && e.Code == "git_failed" {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.output), nil
}

func (s *Service) syncRemote(ctx context.Context, dir, operation string) error {
	status, err := s.status(ctx, dir)
	if err != nil {
		return err
	}
	if status.Detached {
		return failure("detached_head", "Select a local branch before pulling or pushing.")
	}
	branch := status.Branch
	if err = validBranch(ctx, dir, branch); err != nil {
		return err
	}
	remote, err := configValue(ctx, dir, "branch."+branch+".remote")
	if err != nil {
		return err
	}
	if remote == "" {
		remote = "origin"
	}
	fetchRemote := remote
	target := branch
	if operation == "push" {
		pushRemote, e := configValue(ctx, dir, "branch."+branch+".pushRemote")
		if e != nil {
			return e
		}
		if pushRemote == "" {
			pushRemote, e = configValue(ctx, dir, "remote.pushDefault")
			if e != nil {
				return e
			}
		}
		if pushRemote != "" {
			remote = pushRemote
		}
	}
	if remote == "." || remote == "" || strings.HasPrefix(remote, "-") || hasControl(remote) || strings.ContainsAny(remote, " /\\") {
		return failure("invalid_remote", "Choose a named HTTP, HTTPS, SSH or workspace-local Git remote.")
	}
	merge, err := configValue(ctx, dir, "branch."+branch+".merge")
	if err != nil {
		return err
	}
	if merge != "" && (operation == "pull" || remote == fetchRemote) {
		if !strings.HasPrefix(merge, "refs/heads/") {
			return failure("invalid_ref", "The upstream must name a branch.")
		}
		target = strings.TrimPrefix(merge, "refs/heads/")
	}
	if err = validBranch(ctx, dir, target); err != nil {
		return err
	}
	args := []string{"remote", "get-url", "--all"}
	if operation == "push" {
		args = append(args, "--push")
	}
	args = append(args, "--", remote)
	urls, err := runGit(ctx, dir, streamLimit, "", args...)
	if err != nil {
		return err
	}
	for _, url := range strings.Split(strings.TrimSpace(urls.output), "\n") {
		resolved, remoteErr := s.remote(url, dir)
		if remoteErr != nil {
			return remoteErr
		}
		if err = s.validateLocalRemote(ctx, resolved); err != nil {
			return err
		}
	}
	if operation == "pull" {
		// Use an explicit fetch refspec and empty refmap: repository configuration
		// must not turn a pull into an update of arbitrary local branch refs.
		tracking := "refs/remotes/" + remote + "/" + target
		if _, err = runGit(ctx, dir, streamLimit, "", "check-ref-format", tracking); err != nil {
			return failure("invalid_remote", "The remote tracking branch is invalid.")
		}
		_, err = runGit(ctx, dir, streamLimit, "", "fetch", "--no-recurse-submodules", "--no-tags", "--refmap=", "--", remote, "refs/heads/"+target+":"+tracking)
		if err != nil {
			return err
		}
		fetched, err := runGit(ctx, dir, streamLimit, "", "rev-parse", "--verify", "FETCH_HEAD^{commit}")
		if err != nil {
			return err
		}
		if err = s.protectRegistry(ctx, dir, strings.TrimSpace(fetched.output)); err != nil {
			return err
		}
		if err = s.requireClean(ctx, dir); err != nil {
			return err
		}
		_, err = runGit(ctx, dir, streamLimit, "", "-c", "merge.autoStash=false", "merge", "--ff-only", "--no-autostash", "--no-edit", "--no-overwrite-ignore", "--", strings.TrimSpace(fetched.output))
		return err
	} else {
		_, err = runGit(ctx, dir, streamLimit, "", "-c", "remote."+remote+".mirror=false", "push", "--porcelain", "--no-verify", "--", remote, "refs/heads/"+branch+":refs/heads/"+target)
	}
	return err
}
