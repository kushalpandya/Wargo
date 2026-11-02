// SPDX-License-Identifier: AGPL-3.0-only

package term

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	scrollback   = 256 << 10 // bytes replayed on reattach
	readChunk    = 16 << 10
	clientQueue  = 64 // frames; with readChunk ≈ 1 MiB max pending per client
	killGrace    = 3 * time.Second
	exitedLinger = 2 * time.Minute // keep exited sessions (detached) this long
)

// frame is one outbound WebSocket message.
type frame struct {
	text bool
	data []byte
}

// client is the (single) browser attached to a session.
type client struct {
	out chan frame
	// gone is closed when the session detaches the client; reason tells
	// the handler which close status to send.
	gone   chan struct{}
	reason closeReason
}

type closeReason int

const (
	reasonNone closeReason = iota
	reasonExited
	reasonTakeover
	reasonSlow
	reasonKilled
)

// Session is one PTY-backed process.
type Session struct {
	ID      string
	Kind    string
	Target  string
	Created time.Time

	cmd  *exec.Cmd
	pty  *os.File
	done chan struct{} // closed when the process has exited and output drained

	mu         sync.Mutex
	title      string
	cols, rows int
	ring       *ring
	cl         *client
	lastSeen   time.Time // last detach (or creation)
	exited     bool
	exitCode   int
	killed     bool
	titleScan  []byte // carry-over for OSC title parsing across reads
	tmuxN      int
}

// Info is the JSON shape listed by GET /api/term.
type Info struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Target    string    `json:"target,omitempty"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	Attached  bool      `json:"attached"`
	Cols      int       `json:"cols"`
	Rows      int       `json:"rows"`
	Exited    bool      `json:"exited"`
	ExitCode  *int      `json:"exitCode,omitempty"`
	Tmux      bool      `json:"tmux,omitempty"`
}

func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := Info{
		ID: s.ID, Kind: s.Kind, Target: s.Target, Title: s.title, CreatedAt: s.Created,
		Attached: s.cl != nil, Cols: s.cols, Rows: s.rows, Exited: s.exited, Tmux: s.tmuxN > 0,
	}
	if s.exited {
		c := s.exitCode
		in.ExitCode = &c
	}
	return in
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// startSession launches sp in a PTY and begins pumping its output.
func startSession(kind, target string, sp spec, env []string, cols, rows int) (*Session, error) {
	cmd := sp.command(env)
	f, err := startPTY(cmd, cols, rows)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s := &Session{
		ID: newID(), Kind: kind, Target: target, Created: now,
		cmd: cmd, pty: f, done: make(chan struct{}),
		title: sp.Title, cols: cols, rows: rows, ring: newRing(scrollback), lastSeen: now,
	}
	go s.pump()
	return s, nil
}

// pump copies PTY output into the scrollback and the attached client.
func (s *Session) pump() {
	buf := make([]byte, readChunk)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.output(buf[:n])
		}
		if err != nil {
			break
		}
	}
	code := 0
	if err := s.cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			}
		} else {
			code = -1
		}
	}
	s.pty.Close()
	s.mu.Lock()
	s.exited, s.exitCode = true, code
	if s.cl != nil {
		s.sendLocked(frame{text: true, data: ctrlJSON(map[string]any{"type": "exit", "code": code})})
		s.detachLocked(reasonExited)
	}
	s.mu.Unlock()
	close(s.done)
}

func (s *Session) output(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ring.Write(p)
	if t, ok := s.scanTitle(p); ok && t != s.title {
		s.title = t
		s.sendLocked(frame{text: true, data: ctrlJSON(map[string]any{"type": "title", "title": t})})
	}
	s.sendLocked(frame{data: append([]byte(nil), p...)})
}

// sendLocked queues f for the attached client; a client that cannot keep
// up is disconnected (it can reattach and redraw from scrollback).
func (s *Session) sendLocked(f frame) {
	if s.cl == nil {
		return
	}
	select {
	case s.cl.out <- f:
	default:
		s.detachLocked(reasonSlow)
	}
}

func (s *Session) detachLocked(r closeReason) {
	if s.cl == nil {
		return
	}
	s.cl.reason = r
	close(s.cl.gone)
	s.cl = nil
	s.lastSeen = time.Now()
}

// attach makes a new client current (kicking any previous one) and returns
// the scrollback to replay first. Holding the lock while snapshotting
// guarantees no output is lost or duplicated between replay and live data.
func (s *Session) attach() (*client, []byte, Info) {
	s.mu.Lock()
	s.detachLocked(reasonTakeover)
	cl := &client{out: make(chan frame, clientQueue), gone: make(chan struct{})}
	replay := trimReplay(s.ring.Bytes(), s.ring.full)
	if !s.exited {
		s.cl = cl
	}
	s.mu.Unlock()
	return cl, replay, s.Info()
}

// trimReplay drops a partial first line when the ring has wrapped so the
// replay doesn't start in the middle of an escape sequence or rune.
func trimReplay(b []byte, wrapped bool) []byte {
	if !wrapped {
		return b
	}
	lim := min(len(b), 4096)
	if i := bytes.IndexByte(b[:lim], '\n'); i >= 0 {
		return b[i+1:]
	}
	return b
}

func (s *Session) detach(cl *client) {
	s.mu.Lock()
	if s.cl == cl {
		s.detachLocked(reasonNone)
	}
	s.mu.Unlock()
}

func (s *Session) write(p []byte) error {
	_, err := s.pty.Write(p)
	return err
}

func (s *Session) resize(cols, rows int) error {
	cols, rows = clampSize(cols, rows)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exited {
		return nil
	}
	s.cols, s.rows = cols, rows
	return setSize(s.pty, cols, rows)
}

func clampSize(cols, rows int) (int, int) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return min(cols, 1000), min(rows, 500)
}

// kill hangs up the process group, escalating to SIGKILL after killGrace.
// Blocks until the process is gone (or the grace period twice over).
func (s *Session) kill() {
	s.mu.Lock()
	if s.exited {
		s.mu.Unlock()
		return
	}
	s.killed = true
	s.mu.Unlock()
	pid := s.cmd.Process.Pid
	// Setsid made the child a process group leader: signal the whole group.
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	_ = s.cmd.Process.Signal(syscall.SIGHUP)
	select {
	case <-s.done:
		return
	case <-time.After(killGrace):
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = s.cmd.Process.Kill()
	// Closing the master hangs up anything still holding the slave.
	s.pty.Close()
	select {
	case <-s.done:
	case <-time.After(killGrace):
		slog.Warn("term: process did not exit after SIGKILL", "id", s.ID, "pid", pid)
	}
}

// scanTitle extracts the last OSC 0/2 window title in p (with carry-over
// of an unterminated sequence from the previous chunk).
func (s *Session) scanTitle(p []byte) (string, bool) {
	data := p
	if len(s.titleScan) > 0 {
		data = append(append([]byte(nil), s.titleScan...), p...)
		s.titleScan = nil
	}
	title, found := "", false
	for {
		i := bytes.Index(data, []byte("\x1b]"))
		if i < 0 {
			break
		}
		rest := data[i+2:]
		if len(rest) < 2 {
			s.titleScan = append([]byte(nil), data[i:]...)
			break
		}
		if (rest[0] != '0' && rest[0] != '2') || rest[1] != ';' {
			data = rest
			continue
		}
		body := rest[2:]
		end, skip := -1, 0
		if j := bytes.IndexByte(body, 0x07); j >= 0 {
			end, skip = j, 1
		}
		if j := bytes.Index(body, []byte("\x1b\\")); j >= 0 && (end < 0 || j < end) {
			end, skip = j, 2
		}
		if end < 0 {
			if len(body) < 512 {
				s.titleScan = append([]byte(nil), data[i:]...)
			}
			break
		}
		t := body[:end]
		if len(t) > 256 {
			t = t[:256]
		}
		title, found = string(bytes.ToValidUTF8(t, nil)), true
		data = body[end+skip:]
	}
	return title, found
}
