// Package workspacegit provides Git operations for a single instance workspace.
// It must run inside the instance business sandbox, with that instance's user,
// environment and network policy. It is not a host-side Git execution service.
package workspacegit

type Request struct {
	Operation string   `json:"operation"`
	Path      string   `json:"path,omitempty"`
	Name      string   `json:"name,omitempty"`
	URL       string   `json:"url,omitempty"`
	Branch    string   `json:"branch,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	Staged    bool     `json:"staged,omitempty"`
	Message   string   `json:"message,omitempty"`
	Limit     int      `json:"limit,omitempty"`
}

type Response struct {
	Projects  []Project `json:"projects,omitempty"`
	Project   *Project  `json:"project,omitempty"`
	Status    *Status   `json:"status,omitempty"`
	Branches  []Branch  `json:"branches,omitempty"`
	History   []Commit  `json:"history,omitempty"`
	Diff      string    `json:"diff,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	Message   string    `json:"message,omitempty"`
}

type Project struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Remote string `json:"remote,omitempty"`
}

type Status struct {
	Branch   string   `json:"branch"`
	Detached bool     `json:"detached"`
	Ahead    int      `json:"ahead"`
	Behind   int      `json:"behind"`
	Changes  []Change `json:"changes"`
}

type Change struct {
	Path           string `json:"path"`
	PreviousPath   string `json:"previous_path,omitempty"`
	IndexStatus    string `json:"index_status"`
	WorktreeStatus string `json:"worktree_status"`
	Staged         bool   `json:"staged"`
	Untracked      bool   `json:"untracked"`
	Additions      int    `json:"additions"`
	Deletions      int    `json:"deletions"`
}

type Branch struct {
	Name     string `json:"name"`
	Current  bool   `json:"current"`
	Remote   bool   `json:"remote"`
	Upstream string `json:"upstream,omitempty"`
}

type Commit struct {
	Hash        string `json:"hash"`
	Author      string `json:"author"`
	AuthorEmail string `json:"author_email"`
	Timestamp   int64  `json:"timestamp"`
	Message     string `json:"message"`
}

// Error contains only bounded, public-safe text. Git stderr is never returned.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func failure(code, message string) error { return &Error{Code: code, Message: message} }
