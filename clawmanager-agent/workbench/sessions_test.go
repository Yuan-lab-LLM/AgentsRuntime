package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type testDuplex struct {
	*io.PipeReader
	w *io.PipeWriter
}

func (testDuplex) SetWriteDeadline(time.Time) error { return nil }
func (d testDuplex) Write(p []byte) (int, error)    { return d.w.Write(p) }
func TestSessionReplayBoundsIsolationAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewSessions(ctx, 7)
	defer s.Close()
	launch := func(ctx context.Context, o TerminalOptions) (Terminal, error) {
		r, w := io.Pipe()
		done := make(chan struct{})
		go func() { <-ctx.Done(); w.Close(); close(done) }()
		return Terminal{IO: testDuplex{r, w}, Resize: func(uint16, uint16) error { return nil }, Wait: func() error { <-done; return nil }}, nil
	}
	create := func(cwd string) Session {
		raw, _ := json.Marshal(TerminalOptions{Cwd: cwd, Cols: 80, Rows: 24})
		v, e := s.Execute(ctx, "terminal.create", raw, launch, nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		return v.(Session)
	}
	a, b := create("a"), create("b")
	if a.ID == b.ID || a.Cwd == b.Cwd || a.Generation != 7 {
		t.Fatalf("sessions: %+v %+v", a, b)
	}
	// Polling/disconnect has no lifecycle effect; independent input is replayable.
	write := func(id, text string) {
		raw, _ := json.Marshal(map[string]any{"session": id, "input": text})
		if _, e := s.Execute(ctx, "terminal.write", raw, launch, nil, nil); e != nil {
			t.Fatal(e)
		}
	}
	write(a.ID, "hello")
	write(b.ID, "other")
	read := func(id string, cursor uint64) ReadResult {
		raw, _ := json.Marshal(map[string]any{"session": id, "cursor": cursor})
		v, e := s.Execute(ctx, "terminal.read", raw, launch, nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		return v.(ReadResult)
	}
	deadline := time.Now().Add(time.Second)
	for (len(read(a.ID, 0).Data) < 5 || len(read(b.ID, 0).Data) < 5) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if string(read(a.ID, 0).Data) != "hello" || string(read(b.ID, 0).Data) != "other" {
		t.Fatal("output crossed sessions")
	}
	for i := 0; i < 20; i++ {
		write(a.ID, strings.Repeat("x", 64<<10))
	}
	r := read(a.ID, 0)
	if len(r.Data) > 1<<20 || !r.Truncated {
		t.Fatalf("unbounded replay %+v", r)
	}
	raw, _ := json.Marshal(map[string]any{"session": a.ID, "cursor": uint64(1 << 63)})
	if _, e := s.Execute(ctx, "terminal.read", raw, launch, nil, nil); e == nil {
		t.Fatal("future cursor accepted")
	}
	raw, _ = json.Marshal(map[string]any{"session": a.ID})
	if _, e := s.Execute(ctx, "terminal.close", raw, launch, nil, nil); e != nil {
		t.Fatal(e)
	}
	if !read(a.ID, r.Cursor).Exited {
		t.Fatal("not closed")
	}
	if _, e := s.Execute(ctx, "terminal.close", raw, launch, nil, nil); e != nil {
		t.Fatal("close not idempotent")
	}
	other := NewSessions(ctx, 8)
	defer other.Close()
	if _, e := other.Execute(ctx, "terminal.get", raw, launch, nil, nil); e == nil {
		t.Fatal("session crossed scope")
	}
}

func TestTerminalWriteHonorsRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewSessions(ctx, 1)
	defer s.Close()
	launch := func(ctx context.Context, _ TerminalOptions) (Terminal, error) {
		a, b := net.Pipe()
		done := make(chan struct{})
		go func() { <-ctx.Done(); b.Close(); close(done) }()
		return Terminal{IO: a, Wait: func() error { <-done; return nil }}, nil
	}
	value, err := s.Execute(ctx, "terminal.create", []byte(`{}`), launch, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	session := value.(Session)
	raw, _ := json.Marshal(map[string]string{"session": session.ID, "input": "blocked"})
	request, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := s.Execute(request, "terminal.write", raw, launch, nil, nil); done <- err }()
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("write did not return cancellation: %v", err)
			}
		case <-time.After(time.Second):
			cancel()
			t.Fatal("blocked write ignored request deadline")
		}
	}
	s.mu.Lock()
	exited := s.entries[session.ID].info.Exited
	s.mu.Unlock()
	if exited {
		t.Fatal("request cancellation closed the terminal")
	}
}
