package workbench

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sort"
	"sync"
	"time"
)

const replaySize = 1 << 20

// Sessions belongs to exactly one gateway attempt; IDs have no authority alone.
type Sessions struct {
	mu          sync.Mutex
	ctx         context.Context
	generation  int
	entries     map[string]*session
	closing     bool
	teardownErr error
}
type session struct {
	info      Session
	options   TerminalOptions
	terminal  Terminal
	cancel    context.CancelFunc
	done      chan struct{}
	output    []byte
	cursor    uint64
	waitErr   error
	writeGate chan struct{}
}

func NewSessions(ctx context.Context, generation int) *Sessions {
	return &Sessions{ctx: ctx, generation: generation, entries: map[string]*session{}}
}
func (s *Sessions) prune() {
	var exited []*session
	for id, e := range s.entries {
		if e.info.Exited {
			if time.Since(e.info.CreatedAt) > time.Hour {
				delete(s.entries, id)
			} else {
				exited = append(exited, e)
			}
		}
	}
	sort.Slice(exited, func(i, j int) bool { return exited[i].info.CreatedAt.Before(exited[j].info.CreatedAt) })
	for len(exited) > 32 {
		delete(s.entries, exited[0].info.ID)
		exited = exited[1:]
	}
}
func (s *Sessions) create(options TerminalOptions, launch TerminalLauncher) (Session, error) {
	if options.Cols == 0 {
		options.Cols = 80
	}
	if options.Rows == 0 {
		options.Rows = 24
	}
	if options.Cols > 1000 || options.Rows > 1000 || len(options.Cwd) > 4096 {
		return Session{}, Invalid()
	}
	s.mu.Lock()
	s.prune()
	active := 0
	for _, e := range s.entries {
		if !e.info.Exited {
			active++
		}
	}
	if s.closing || s.ctx.Err() != nil {
		s.mu.Unlock()
		return Session{}, Failure("stale_generation", "Terminal generation has ended.")
	}
	if active >= 8 {
		s.mu.Unlock()
		return Session{}, Failure("session_limit", "At most eight terminals may be active.")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		s.mu.Unlock()
		return Session{}, err
	}
	id := hex.EncodeToString(random[:])
	ctx, cancel := context.WithCancel(s.ctx)
	e := &session{info: Session{ID: id, Name: "Terminal", Cwd: options.Cwd, Generation: s.generation, CreatedAt: time.Now().UTC()}, options: options, cancel: cancel, done: make(chan struct{}), output: make([]byte, replaySize), writeGate: make(chan struct{}, 1)}
	s.entries[id] = e
	s.mu.Unlock()
	terminal, err := launch(ctx, options)
	if err != nil {
		cancel()
		s.mu.Lock()
		delete(s.entries, id)
		close(e.done)
		s.mu.Unlock()
		return Session{}, err
	}
	s.mu.Lock()
	e.terminal = terminal
	info := e.info
	s.mu.Unlock()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 32<<10)
		for {
			n, err := terminal.IO.Read(buffer)
			if n > 0 {
				s.mu.Lock()
				for offset := 0; offset < n; {
					start := int(e.cursor % replaySize)
					count := min(n-offset, replaySize-start)
					copy(e.output[start:], buffer[offset:offset+count])
					e.cursor += uint64(count)
					offset += count
				}
				s.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		err := terminal.Wait()
		<-readDone
		terminal.IO.Close()
		cancel()
		s.mu.Lock()
		e.waitErr = err
		if unconfirmedTerminalError(err) {
			s.teardownErr = errors.Join(s.teardownErr, err)
		}
		e.info.Exited = true
		code := 0
		if err != nil {
			code = -1
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			}
		}
		e.info.ExitCode = &code
		close(e.done)
		s.prune()
		s.mu.Unlock()
	}()
	return info, nil
}
func (s *Sessions) Execute(ctx context.Context, op string, raw json.RawMessage, launch TerminalLauncher, list func(context.Context) ([]Process, error), stop func(context.Context, string) (Process, error)) (any, error) {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	if op == "terminal.create" {
		var options TerminalOptions
		if json.Unmarshal(raw, &options) != nil {
			return nil, Invalid()
		}
		return s.create(options, launch)
	}
	s.mu.Lock()
	s.prune()
	if op == "terminal.list" {
		result := []Session{}
		for _, e := range s.entries {
			result = append(result, e.info)
		}
		s.mu.Unlock()
		sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
		return map[string]any{"sessions": result}, nil
	}
	if op == "process.list" {
		result := []Process{}
		for _, e := range s.entries {
			result = append(result, terminalProcess(e.info))
		}
		s.mu.Unlock()
		if list != nil {
			owned, err := list(ctx)
			if err != nil {
				return nil, err
			}
			result = append(result, owned...)
		}
		return map[string]any{"processes": result}, nil
	}
	var data struct {
		Session string `json:"session"`
		Process string `json:"process"`
		Input   string `json:"input"`
		Cursor  uint64 `json:"cursor"`
		Cols    uint16 `json:"cols"`
		Rows    uint16 `json:"rows"`
	}
	if json.Unmarshal(raw, &data) != nil {
		s.mu.Unlock()
		return nil, Invalid()
	}
	processOp := op == "process.stop" || op == "process.restart"
	if processOp {
		data.Session = data.Process
	}
	e := s.entries[data.Session]
	if e == nil {
		s.mu.Unlock()
		if processOp {
			if op == "process.restart" {
				return nil, Failure("unsupported_operation", "This process has no trusted restart recipe.")
			}
			if stop != nil {
				return stop(ctx, data.Process)
			}
		}
		return nil, Failure("session_not_found", "Session is unavailable in this generation.")
	}
	info := e.info
	switch op {
	case "terminal.get":
		s.mu.Unlock()
		return info, nil
	case "terminal.read":
		if data.Cursor > e.cursor {
			s.mu.Unlock()
			return nil, Invalid()
		}
		start := uint64(0)
		if e.cursor > replaySize {
			start = e.cursor - replaySize
		}
		truncated := data.Cursor < start
		if data.Cursor > start {
			start = data.Cursor
		}
		result := ReadResult{Data: make([]byte, int(e.cursor-start)), Cursor: e.cursor, Truncated: truncated, Exited: info.Exited}
		for offset := 0; offset < len(result.Data); {
			at := int((start + uint64(offset)) % replaySize)
			n := min(len(result.Data)-offset, replaySize-at)
			copy(result.Data[offset:], e.output[at:at+n])
			offset += n
		}
		s.mu.Unlock()
		return result, nil
	case "terminal.close", "process.stop", "process.restart":
		e.cancel()
		s.mu.Unlock()
		select {
		case <-e.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		s.mu.Lock()
		info = e.info
		waitErr := e.waitErr
		s.mu.Unlock()
		if op == "process.restart" {
			if unconfirmedTerminalError(waitErr) {
				return nil, waitErr
			}
			created, err := s.create(e.options, launch)
			return terminalProcess(created), err
		}
		if processOp {
			return terminalProcess(info), nil
		}
		return info, nil
	case "terminal.write", "terminal.resize":
		terminal := e.terminal
		s.mu.Unlock()
		if info.Exited || terminal.IO == nil {
			return nil, Failure("session_exited", "Terminal has exited.")
		}
		select {
		case e.writeGate <- struct{}{}:
			defer func() { <-e.writeGate }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if op == "terminal.write" {
			if len(data.Input) > 64<<10 {
				return nil, Invalid()
			}
			return struct{}{}, writeTerminal(ctx, terminal.IO, []byte(data.Input))
		}
		if data.Cols < 1 || data.Rows < 1 || data.Cols > 1000 || data.Rows > 1000 {
			return nil, Invalid()
		}
		return struct{}{}, terminal.Resize(data.Cols, data.Rows)
	default:
		s.mu.Unlock()
		return nil, Failure("unsupported_operation", "Unsupported workbench operation.")
	}
}
func terminalProcess(info Session) Process {
	state := "running"
	if info.Exited {
		state = "exited"
	}
	return Process{ID: info.ID, Name: info.Name, Command: "/bin/sh -i", Cwd: info.Cwd, State: state, Source: "terminal", SessionID: info.ID, StartedAt: info.CreatedAt, CanStop: !info.Exited, CanRestart: true}
}
func (s *Sessions) Close() error {
	s.mu.Lock()
	s.closing = true
	all := make([]*session, 0, len(s.entries))
	for _, e := range s.entries {
		e.cancel()
		all = append(all, e)
	}
	s.mu.Unlock()
	s.mu.Lock()
	result := s.teardownErr
	s.mu.Unlock()
	for _, e := range all {
		<-e.done
		s.mu.Lock()
		err := e.waitErr
		s.mu.Unlock()
		var exit *exec.ExitError
		if err != nil && !errors.Is(err, context.Canceled) && !errors.As(err, &exit) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func unconfirmedTerminalError(err error) bool {
	var exit *exec.ExitError
	return err != nil && !errors.Is(err, context.Canceled) && !errors.As(err, &exit)
}

// A full PTY input queue must not turn an HTTP request into an idle-stop lock.
func writeTerminal(ctx context.Context, terminal io.Writer, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := terminal.(interface{ SetWriteDeadline(time.Time) error })
	if !ok {
		return Failure("terminal_io_unavailable", "Terminal write deadlines are unavailable.")
	}
	until := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(until) {
		until = d
	}
	if err := deadline.SetWriteDeadline(until); err != nil {
		return err
	}
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = deadline.SetWriteDeadline(time.Now()); close(cancelled) })
	_, err := terminal.Write(data)
	if !stop() {
		<-cancelled
	}
	_ = deadline.SetWriteDeadline(time.Time{})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if d, ok := ctx.Deadline(); err != nil && ok && !time.Now().Before(d) {
		return context.DeadlineExceeded
	}
	return err
}
