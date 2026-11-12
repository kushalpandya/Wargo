// SPDX-License-Identifier: AGPL-3.0-only

package term

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRing(t *testing.T) {
	tests := []struct {
		name   string
		size   int
		writes []string
		want   string
	}{
		{"empty", 8, nil, ""},
		{"partial", 8, []string{"abc"}, "abc"},
		{"exact", 4, []string{"abcd"}, "abcd"},
		{"wrap", 4, []string{"abc", "def"}, "cdef"},
		{"many small", 5, []string{"a", "b", "c", "d", "e", "f", "g"}, "cdefg"},
		{"oversized write", 4, []string{"xy", "0123456789"}, "6789"},
		{"exact then more", 3, []string{"abc", "d"}, "bcd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRing(tc.size)
			for _, w := range tc.writes {
				r.Write([]byte(w))
			}
			if got := string(r.Bytes()); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if r.Len() != len(tc.want) {
				t.Fatalf("len %d want %d", r.Len(), len(tc.want))
			}
		})
	}
}

func TestTrimReplay(t *testing.T) {
	if got := string(trimReplay([]byte("ab\ncd"), false)); got != "ab\ncd" {
		t.Fatal(got)
	}
	if got := string(trimReplay([]byte("[1mab\ncd"), true)); got != "cd" {
		t.Fatal(got)
	}
}

func TestBuildEnv(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin:/bin", "LANG=en_GB.UTF-8", "LC_TIME=de_DE.UTF-8", "LC_ALL=C.UTF-8",
		"XDG_RUNTIME_DIR=/run/user/1000", "WARGO_SECRET=hunter2", "AWS_SECRET_ACCESS_KEY=x",
		"SSH_AUTH_SOCK=/tmp/agent", "INVOCATION_ID=abc", "HOME=/wrong", "TERM=dumb", "LC_BAD-NAME=x",
	}
	a := account{User: "kushal", Home: "/home/kushal", Shell: "/usr/bin/zsh"}
	env := buildEnv(environ, a)
	want := []string{
		"HOME=/home/kushal", "USER=kushal", "LOGNAME=kushal", "SHELL=/usr/bin/zsh", "PATH=/usr/bin:/bin",
		"TERM=xterm-256color", "COLORTERM=truecolor", "LANG=en_GB.UTF-8", "LC_ALL=C.UTF-8", "LC_TIME=de_DE.UTF-8",
		"XDG_RUNTIME_DIR=/run/user/1000",
	}
	if !slices.Equal(env, want) {
		t.Fatalf("env:\n got %q\nwant %q", env, want)
	}
	for _, kv := range env {
		if strings.Contains(kv, "SECRET") || strings.Contains(kv, "SSH_AUTH") || strings.Contains(kv, "INVOCATION") {
			t.Fatalf("leaked %s", kv)
		}
	}
	// Defaults when Wargo's env is empty (e.g. systemd).
	env = buildEnv(nil, a, "DOCKER_HOST=unix:///x")
	if !slices.Contains(env, "PATH="+defaultPath) || !slices.Contains(env, "LANG=C.UTF-8") || env[len(env)-1] != "DOCKER_HOST=unix:///x" {
		t.Fatalf("defaults: %q", env)
	}
}

func TestValidContainer(t *testing.T) {
	ok := []string{"jellyfin", "a", "my_app.1-2", "3f2a9c0d1e", strings.Repeat("a", 128)}
	bad := []string{"", "-rm", ".x", "a b", "a;b", "$(id)", "a/b", "--privileged", strings.Repeat("a", 129), "ünï"}
	for _, s := range ok {
		if !ValidContainer(s) {
			t.Errorf("want valid: %q", s)
		}
	}
	for _, s := range bad {
		if ValidContainer(s) {
			t.Errorf("want invalid: %q", s)
		}
	}
}

func TestBuildSpec(t *testing.T) {
	a := account{User: "u", Home: "/home/u", Shell: "/usr/bin/zsh"}
	all := tools{Tmux: "/usr/bin/tmux", Sudo: "/usr/bin/sudo", Docker: "/usr/bin/docker"}
	tests := []struct {
		name         string
		kind, target string
		tmux         bool
		tools        tools
		want         []string
		title        string
		err          bool
	}{
		{"host", KindHost, "", false, all, []string{"/usr/bin/zsh", "-l"}, "zsh", false},
		{"host tmux", KindHost, "", true, all, []string{"/usr/bin/tmux", "new-session", "-A", "-s", "wargo-3"}, "tmux wargo-3", false},
		{"host tmux missing", KindHost, "", true, tools{}, nil, "", true},
		{"root", KindRoot, "", false, all, []string{"/usr/bin/sudo", "-i"}, "root", false},
		{"root no sudo", KindRoot, "", false, tools{}, nil, "", true},
		{"container", KindContainer, "jellyfin", false, all,
			[]string{"/usr/bin/docker", "exec", "-it", "-e", "TERM=xterm-256color", "-e", "COLORTERM=truecolor", "jellyfin", "sh", "-c", containerShell},
			"jellyfin", false},
		{"container injection", KindContainer, "--privileged", false, all, nil, "", true},
		{"container shell meta", KindContainer, "x;id", false, all, nil, "", true},
		{"container no docker", KindContainer, "x", false, tools{}, nil, "", true},
		{"unknown", "ssh", "", false, all, nil, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sp, err := buildSpec(tc.kind, tc.target, tc.tmux, 3, a, tc.tools)
			if tc.err {
				if err == nil {
					t.Fatalf("want error, got %q", sp.Argv)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(sp.Argv, tc.want) || sp.Title != tc.title || sp.Dir != "/home/u" {
				t.Fatalf("got %q %q %q", sp.Argv, sp.Title, sp.Dir)
			}
		})
	}
}

func TestScanTitle(t *testing.T) {
	s := &Session{}
	if got, ok := s.scanTitle([]byte("hi\x1b]0;user@host: ~\x07$ ")); !ok || got != "user@host: ~" {
		t.Fatalf("%q %v", got, ok)
	}
	if got, ok := s.scanTitle([]byte("\x1b]2;vim\x1b\\")); !ok || got != "vim" {
		t.Fatalf("%q %v", got, ok)
	}
	// Split across chunks.
	if _, ok := s.scanTitle([]byte("x\x1b]0;spl")); ok {
		t.Fatal("unexpected")
	}
	if got, ok := s.scanTitle([]byte("it\x07")); !ok || got != "split" {
		t.Fatalf("%q %v", got, ok)
	}
	// Other OSC codes ignored.
	if _, ok := s.scanTitle([]byte("\x1b]7;file:///x\x07")); ok {
		t.Fatal("osc 7 parsed as title")
	}
}

type nopAudit struct{ actions []string }

func (n *nopAudit) Record(r *http.Request, action, target string, err error, details any) {
	n.actions = append(n.actions, action+" "+target)
}

// testService returns a service whose "host" kind runs the given shell
// script instead of the user's login shell.
func testService(t *testing.T, script string) (*Service, *nopAudit) {
	t.Helper()
	au := &nopAudit{}
	s := New(au, nil, "")
	s.acct = account{User: "test", Home: t.TempDir(), Shell: "/bin/sh"}
	s.tools = tools{}
	s.env = []string{"PATH=/usr/bin:/bin", "WARGO_SECRET=nope"}
	t.Cleanup(s.killAll)
	if script != "" {
		// "-l" is appended by buildSpec; sh accepts `-c script -l` as $0.
		s.acct.Shell = "/bin/sh"
		s.testArgv = []string{"/bin/sh", "-c", script}
	}
	return s, au
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestSessionLifecycle(t *testing.T) {
	s, _ := testService(t, `echo hi; echo "secret=[$WARGO_SECRET] term=$TERM"; printf '\033]0;mytitle\007'; sleep 30`)
	se, err := s.open(createReq{Kind: KindHost, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		se.mu.Lock()
		defer se.mu.Unlock()
		return bytes.Contains(se.ring.Bytes(), []byte("term="))
	})
	out := string(se.ring.Bytes())
	if !strings.Contains(out, "hi") || !strings.Contains(out, "secret=[]") || !strings.Contains(out, "term=xterm-256color") {
		t.Fatalf("output %q", out)
	}
	waitFor(t, 2*time.Second, func() bool { return se.Info().Title == "mytitle" })
	in := se.Info()
	if in.Cols != 100 || in.Rows != 30 || in.Exited || in.Attached {
		t.Fatalf("info %+v", in)
	}
	if err := se.resize(120, 40); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	se.kill()
	if time.Since(start) > killGrace {
		t.Fatal("SIGHUP did not stop the process")
	}
	in = se.Info()
	if !in.Exited || in.ExitCode == nil {
		t.Fatalf("not exited: %+v", in)
	}
}

func TestSessionExitAndLimit(t *testing.T) {
	s, _ := testService(t, `exit 3`)
	s.max = 2
	se, err := s.open(createReq{Kind: KindHost})
	if err != nil {
		t.Fatal(err)
	}
	<-se.done
	if in := se.Info(); in.ExitCode == nil || *in.ExitCode != 3 {
		t.Fatalf("%+v", in)
	}
	// Exited sessions don't count towards the limit.
	s.testArgv = []string{"/bin/sh", "-c", "sleep 30"}
	for range 2 {
		if _, err := s.open(createReq{Kind: KindHost}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.open(createReq{Kind: KindHost}); err != errTooMany {
		t.Fatalf("want limit error, got %v", err)
	}
	if _, err := s.open(createReq{Kind: "bogus"}); err == nil {
		t.Fatal("bogus kind accepted")
	}
}

func TestReap(t *testing.T) {
	s, _ := testService(t, `sleep 30`)
	se, err := s.open(createReq{Kind: KindHost})
	if err != nil {
		t.Fatal(err)
	}
	s.reap(time.Now())
	if s.get(se.ID) == nil {
		t.Fatal("reaped too early")
	}
	s.reap(time.Now().Add(s.idle + time.Second))
	if s.get(se.ID) != nil {
		t.Fatal("not reaped")
	}
	waitFor(t, 2*killGrace, func() bool { return se.Info().Exited })
}

func TestHTTPAndWebSocket(t *testing.T) {
	s, au := testService(t, `printf 'ready\n'; while read l; do echo "got:$l"; done`)
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := http.Post(srv.URL+"/api/term", "application/json", strings.NewReader(`{"kind":"host","cols":90,"rows":20}`))
	if err != nil {
		t.Fatal(err)
	}
	var created struct{ ID string }
	json.NewDecoder(res.Body).Decode(&created)
	res.Body.Close()
	if res.StatusCode != 201 || created.ID == "" {
		t.Fatalf("create: %d %+v", res.StatusCode, created)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/term/" + created.ID
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntil := func(c *websocket.Conn, needle string) string {
		var acc strings.Builder
		for !strings.Contains(acc.String(), needle) {
			typ, b, err := c.Read(ctx)
			if err != nil {
				t.Fatalf("read: %v (have %q)", err, acc.String())
			}
			if typ == websocket.MessageBinary {
				acc.Write(b)
			}
		}
		return acc.String()
	}
	readUntil(c, "ready")
	c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":132,"rows":43}`))
	c.Write(ctx, websocket.MessageBinary, []byte("hello\n"))
	readUntil(c, "got:hello")
	if in := s.get(created.ID).Info(); !in.Attached || in.Cols != 132 || in.Rows != 43 {
		t.Fatalf("info %+v", in)
	}

	// A second attach takes over and replays scrollback.
	c2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(c2, "got:hello")
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != CloseTakeover {
		t.Fatalf("want takeover close, got %v", err)
	}

	// List.
	res, _ = http.Get(srv.URL + "/api/term")
	var list []Info
	json.NewDecoder(res.Body).Decode(&list)
	res.Body.Close()
	if len(list) != 1 || list[0].ID != created.ID || !list[0].Attached {
		t.Fatalf("list %+v", list)
	}

	// Delete kills it and closes the socket.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/term/"+created.ID, nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 204 {
		t.Fatalf("delete: %v %v", err, res.StatusCode)
	}
	for {
		if _, _, err = c2.Read(ctx); err != nil {
			break
		}
	}
	if st := websocket.CloseStatus(err); st != CloseKilled && st != CloseExited {
		t.Fatalf("close status %v", err)
	}
	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/term/"+created.ID, nil)
	if res, _ = http.DefaultClient.Do(req); res.StatusCode != 404 {
		t.Fatalf("second delete: %d", res.StatusCode)
	}
	c3, _, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		_, _, err = c3.Read(ctx)
		if websocket.CloseStatus(err) != CloseNotFound {
			t.Fatalf("want not found, got %v", err)
		}
	}
	if !slices.Contains(au.actions, "term.open host") || !slices.Contains(au.actions, "term.close host") {
		t.Fatalf("audit %q", au.actions)
	}
}

func TestContainerEnvPinsDockerHost(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://elsewhere:2375")
	t.Setenv("DOCKER_CONTEXT", "other")
	t.Setenv("DOCKER_CONFIG", "")
	s := New(nil, nil, "/tmp/x.sock")
	if got := s.extraEnv(KindContainer); !slices.Equal(got, []string{"DOCKER_HOST=unix:///tmp/x.sock"}) {
		t.Fatal(got)
	}
	if s.extraEnv(KindHost) != nil || s.extraEnv(KindRoot) != nil {
		t.Fatal("host and root shells get no docker env")
	}
	t.Setenv("DOCKER_CONFIG", "/cfg")
	if got := s.extraEnv(KindContainer); !slices.Equal(got, []string{"DOCKER_HOST=unix:///tmp/x.sock", "DOCKER_CONFIG=/cfg"}) {
		t.Fatal(got)
	}
}
