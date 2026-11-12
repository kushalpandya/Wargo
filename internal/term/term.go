// SPDX-License-Identifier: AGPL-3.0-only

// Package term provides browser terminals: host login shells, root shells
// via `sudo -i` and container shells via `docker exec`, each in its own PTY,
// streamed over a WebSocket. Sessions survive browser reloads for a grace
// period and replay a bounded scrollback on reattach.
package term

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/httpx"
)

const (
	defaultMaxSessions = 8
	defaultIdle        = 10 * time.Minute
)

// Auditor records actions (satisfied by *audit.Log).
type Auditor interface {
	Record(r *http.Request, action, target string, err error, details any)
}

type Service struct {
	audit   Auditor
	origins []string

	max  int
	idle time.Duration

	acct  account
	tools tools
	env   []string // Wargo's environ, filtered per session

	dockerSocket string // the socket Wargo's own Docker client uses

	mu       sync.Mutex
	sessions map[string]*Session

	testArgv []string // tests: replaces the resolved argv
}

// termEnviron is the environment terminals inherit allow-listed variables
// from. In container mode the container's own environment is meaningless
// on the host, so nothing is inherited (defaults apply).
func termEnviron() []string {
	if host.Container() {
		return nil
	}
	return os.Environ()
}

// New builds the service. origins are WebSocket origin patterns.
//
// dockerSocket is the Docker socket Wargo itself uses; container shells are
// pinned to it so the docker CLI targets the same engine.
func New(a Auditor, origins []string, dockerSocket string) *Service {
	s := &Service{
		audit:    a,
		origins:  origins,
		max:      defaultMaxSessions,
		idle:     defaultIdle,
		acct:     currentAccount(os.Getenv),
		env:      termEnviron(),
		sessions: map[string]*Session{},

		dockerSocket: dockerSocket,
	}
	look := host.LookPath
	if resolvePath && !host.Container() {
		// macOS: launchd gives Wargo a bare PATH, so terminals and tool
		// lookup use the user's login PATH (Homebrew and friends).
		if p := resolveLoginPath(context.Background(), s.acct, runLogin); p != "" {
			s.env = withPath(s.env, p)
			look = func(name string) (string, error) { return lookIn(p, name) }
		}
	}
	s.tools = lookTools(look)
	return s
}

// extraEnv is the docker environment a session of this kind gets on top of
// the allow-listed base. Container shells (outside container mode) are pinned
// to Wargo's socket; DOCKER_CONTEXT is never passed, since it would override
// DOCKER_HOST. In container mode docker runs through nsenter on the host and
// the socket path here is the container's view, so it stays unpinned.
func (s *Service) extraEnv(kind string) []string {
	if kind != KindContainer || host.Container() || s.dockerSocket == "" {
		return nil
	}
	out := []string{"DOCKER_HOST=unix://" + s.dockerSocket}
	if v := os.Getenv("DOCKER_CONFIG"); v != "" {
		out = append(out, "DOCKER_CONFIG="+v)
	}
	return out
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/term", httpx.Handler(s.list))
	mux.Handle("POST /api/term", httpx.Handler(s.create))
	mux.Handle("DELETE /api/term/{id}", httpx.Handler(s.remove))
	mux.HandleFunc("GET /ws/term/{id}", s.serveWS)
}

func (s *Service) Capabilities(ctx context.Context) (string, any) {
	return "terminal", map[string]any{
		"available":   true,
		"shell":       s.acct.Shell,
		"tmux":        s.tools.Tmux != "",
		"sudo":        s.tools.Sudo != "",
		"docker":      s.tools.Docker != "",
		"maxSessions": s.max,
	}
}

// Run reaps idle and exited sessions, and kills everything on shutdown.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.killAll()
			return
		case <-t.C:
			s.reap(time.Now())
		}
	}
}

func (s *Service) reap(now time.Time) {
	var victims []*Session
	s.mu.Lock()
	for id, se := range s.sessions {
		se.mu.Lock()
		idle := se.cl == nil && now.Sub(se.lastSeen) > s.idle
		gone := se.exited && se.cl == nil && now.Sub(se.lastSeen) > exitedLinger
		se.mu.Unlock()
		if idle || gone {
			delete(s.sessions, id)
			victims = append(victims, se)
		}
	}
	s.mu.Unlock()
	for _, se := range victims {
		slog.Info("term: reaping session", "id", se.ID, "kind", se.Kind, "target", se.Target)
		go se.kill()
	}
}

func (s *Service) killAll() {
	s.mu.Lock()
	all := make([]*Session, 0, len(s.sessions))
	for id, se := range s.sessions {
		all = append(all, se)
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, se := range all {
		wg.Add(1)
		go func() { defer wg.Done(); se.kill() }()
	}
	wg.Wait()
}

func (s *Service) get(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) error {
	s.mu.Lock()
	out := make([]Info, 0, len(s.sessions))
	for _, se := range s.sessions {
		out = append(out, se.Info())
	}
	s.mu.Unlock()
	slices.SortFunc(out, func(a, b Info) int { return a.CreatedAt.Compare(b.CreatedAt) })
	httpx.OK(w, out)
	return nil
}

type createReq struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
	Tmux   bool   `json:"tmux"`
}

var errTooMany = httpx.Errorf(http.StatusTooManyRequests, "too_many_sessions", "Too many terminal sessions are open; close one first")

func (s *Service) create(w http.ResponseWriter, r *http.Request) error {
	var req createReq
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if req.Kind != KindContainer {
		req.Target = ""
	}
	if req.Kind != KindHost {
		req.Tmux = false
	}
	se, err := s.open(req)
	var details = map[string]any{"kind": req.Kind, "tmux": req.Tmux}
	if se != nil {
		details["id"] = se.ID
	}
	s.audit.Record(r, "term.open", auditTarget(req.Kind, req.Target), err, details)
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return he
		}
		return httpx.BadRequest(err.Error())
	}
	httpx.JSON(w, http.StatusCreated, map[string]string{"id": se.ID})
	return nil
}

func auditTarget(kind, target string) string {
	if target != "" {
		return kind + ":" + target
	}
	return kind
}

// open validates and spawns a session, enforcing the concurrency limit.
func (s *Service) open(req createReq) (*Session, error) {
	switch req.Kind {
	case KindHost, KindRoot, KindContainer:
	default:
		return nil, errBadKind
	}
	cols, rows := clampSize(req.Cols, req.Rows)

	s.mu.Lock()
	if s.liveLocked() >= s.max {
		s.mu.Unlock()
		return nil, errTooMany
	}
	tmuxN := 0
	if req.Tmux {
		tmuxN = s.nextTmuxLocked()
	}
	sp, err := buildSpec(req.Kind, req.Target, req.Tmux, tmuxN, s.acct, s.tools)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if s.testArgv != nil {
		sp.Argv = s.testArgv
	}
	se, err := startSession(req.Kind, req.Target, sp, buildEnv(s.env, s.acct, s.extraEnv(req.Kind)...), cols, rows)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	se.tmuxN = tmuxN
	s.sessions[se.ID] = se
	s.mu.Unlock()
	return se, nil
}

func (s *Service) liveLocked() int {
	n := 0
	for _, se := range s.sessions {
		se.mu.Lock()
		if !se.exited {
			n++
		}
		se.mu.Unlock()
	}
	return n
}

// nextTmuxLocked picks the lowest wargo-<n> name not used by a live
// session, so after a Wargo restart new tabs reattach to old tmux sessions.
func (s *Service) nextTmuxLocked() int {
	used := map[int]bool{}
	for _, se := range s.sessions {
		se.mu.Lock()
		if !se.exited && se.tmuxN > 0 {
			used[se.tmuxN] = true
		}
		se.mu.Unlock()
	}
	for n := 1; ; n++ {
		if !used[n] {
			return n
		}
	}
}

func (s *Service) remove(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	s.mu.Lock()
	se := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if se == nil {
		return httpx.ErrNotFound
	}
	se.mu.Lock()
	se.detachLocked(reasonKilled)
	se.mu.Unlock()
	se.kill()
	s.audit.Record(r, "term.close", auditTarget(se.Kind, se.Target), nil, map[string]any{"id": se.ID, "kind": se.Kind})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func ctrlJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// ---- WebSocket ----

// Close codes the frontend understands.
const (
	CloseExited   websocket.StatusCode = 4000 // process exited
	CloseTakeover websocket.StatusCode = 4001 // attached from another tab
	CloseSlow     websocket.StatusCode = 4002 // client too slow; reattach
	CloseKilled   websocket.StatusCode = 4003 // session closed
	CloseNotFound websocket.StatusCode = 4004 // unknown session
)

type ctrlMsg struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func (s *Service) serveWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.origins})
	if err != nil {
		return
	}
	defer c.CloseNow()
	se := s.get(r.PathValue("id"))
	if se == nil {
		c.Close(CloseNotFound, "no such session")
		return
	}
	c.SetReadLimit(256 << 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cl, replay, info := se.attach()
	defer se.detach(cl)

	write := func(typ websocket.MessageType, b []byte) error {
		wctx, wc := context.WithTimeout(ctx, 10*time.Second)
		defer wc()
		return c.Write(wctx, typ, b)
	}

	// Replay scrollback then the current title before live data.
	for len(replay) > 0 {
		n := min(len(replay), 64<<10)
		if write(websocket.MessageBinary, replay[:n]) != nil {
			return
		}
		replay = replay[n:]
	}
	if write(websocket.MessageText, ctrlJSON(map[string]any{"type": "title", "title": info.Title})) != nil {
		return
	}
	if info.Exited {
		_ = write(websocket.MessageText, ctrlJSON(map[string]any{"type": "exit", "code": *info.ExitCode}))
		c.Close(CloseExited, "exited")
		return
	}

	// Reader: keystrokes and control messages.
	go func() {
		defer cancel()
		for {
			typ, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				if se.write(b) != nil {
					return
				}
				continue
			}
			var m ctrlMsg
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			switch m.Type {
			case "resize":
				_ = se.resize(m.Cols, m.Rows)
			case "ping":
				select {
				case cl.out <- frame{text: true, data: []byte(`{"type":"pong"}`)}:
				default:
				}
			}
		}
	}()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-cl.out:
			typ := websocket.MessageBinary
			if f.text {
				typ = websocket.MessageText
			}
			if write(typ, f.data) != nil {
				return
			}
		case <-cl.gone:
			// Flush what was queued (e.g. final output and exit message).
		flush:
			for cl.reason != reasonSlow {
				select {
				case f := <-cl.out:
					typ := websocket.MessageBinary
					if f.text {
						typ = websocket.MessageText
					}
					if write(typ, f.data) != nil {
						return
					}
				default:
					break flush
				}
			}
			switch cl.reason {
			case reasonExited:
				c.Close(CloseExited, "exited")
			case reasonTakeover:
				c.Close(CloseTakeover, "attached elsewhere")
			case reasonSlow:
				c.Close(CloseSlow, "client too slow")
			case reasonKilled:
				c.Close(CloseKilled, "session closed")
			default:
				c.Close(websocket.StatusNormalClosure, "")
			}
			return
		case <-ping.C:
			pctx, pc := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			pc()
			if err != nil {
				return
			}
		}
	}
}
