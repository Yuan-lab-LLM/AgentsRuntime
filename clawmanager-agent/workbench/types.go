// Package workbench defines bounded, generation-scoped business operations.
package workbench

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

const GitCommand = "managed-workbench-git"
const MaxRequest = 1 << 20
const MaxOutput = 2 << 20

type Request struct {
	InstanceID int             `json:"instance_id"`
	UserID     int             `json:"user_id"`
	Generation int             `json:"generation"`
	GatewayID  string          `json:"gateway_id"`
	Operation  string          `json:"operation"`
	Data       json.RawMessage `json:"data"`
}
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string           { return e.Code + ": " + e.Message }
func Failure(code, message string) error { return &Error{code, message} }
func Invalid() error                     { return Failure("invalid_request", "Invalid workbench request.") }

type Session struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Cwd        string    `json:"cwd"`
	Generation int       `json:"generation"`
	CreatedAt  time.Time `json:"created_at"`
	Exited     bool      `json:"exited"`
	ExitCode   *int      `json:"exit_code,omitempty"`
}
type TerminalOptions struct {
	Cwd  string `json:"cwd"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}
type Terminal struct {
	IO     io.ReadWriteCloser
	Resize func(uint16, uint16) error
	Wait   func() error
}
type TerminalLauncher func(context.Context, TerminalOptions) (Terminal, error)
type ReadResult struct {
	Data      []byte `json:"data"`
	Cursor    uint64 `json:"cursor"`
	Truncated bool   `json:"truncated"`
	Exited    bool   `json:"exited"`
}
type Process struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Command    string    `json:"command"`
	Cwd        string    `json:"cwd"`
	State      string    `json:"state"`
	Source     string    `json:"source"`
	SessionID  string    `json:"session_id,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	CanStop    bool      `json:"can_stop"`
	CanRestart bool      `json:"can_restart"`
}
