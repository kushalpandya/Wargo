// SPDX-License-Identifier: AGPL-3.0-only

package brew

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Homebrew runs `sudo -A` when SUDO_ASKPASS is set, and sudo then runs that
// program to read the password. Wargo points it at a tiny script in a
// private 0700 directory that runs `wargo brew-askpass <socket>`; the
// socket, in the same directory, hands out the password approved for this
// update while the update runs, and only to processes descended from the
// update itself (sudo's askpass child), so other programs running as the
// same user can't ask for it. Nothing is written to disk but the script,
// and the directory is removed when the update ends.

// askpassMaxServes caps how often one update can read the password.
const askpassMaxServes = 64

const askpassPrefix = "wargo-askpass-"

type askpass struct {
	dir    string
	script string
	ln     *net.UnixListener

	mu   sync.Mutex
	pw   string
	root int // the update's process; 0 until it has started
}

// shellQuote quotes s for /bin/sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// startAskpass sets up the askpass script and starts serving pw (which may
// be empty: the script then fails with a clear message instead of hanging).
// Nothing is served until setRoot names the update's process.
func startAskpass(exe, pw string) (*askpass, error) {
	if exe == "" {
		return nil, fmt.Errorf("the Wargo binary's path is unknown")
	}
	dir, err := os.MkdirTemp("", askpassPrefix)
	if err != nil {
		return nil, err
	}
	a := &askpass{dir: dir, script: filepath.Join(dir, "askpass"), pw: pw}
	sock := filepath.Join(dir, "s")
	a.ln, err = net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	body := "#!/bin/sh\nexec " + shellQuote(exe) + " brew-askpass " + shellQuote(sock) + "\n"
	if err := os.WriteFile(a.script, []byte(body), 0o700); err != nil {
		a.Close()
		return nil, err
	}
	go a.serve()
	return a, nil
}

// setRoot names the process whose descendants may read the password.
func (a *askpass) setRoot(pid int) {
	a.mu.Lock()
	a.root = pid
	a.mu.Unlock()
}

func (a *askpass) serve() {
	for range askpassMaxServes {
		c, err := a.ln.AcceptUnix()
		if err != nil {
			return
		}
		a.mu.Lock()
		pw, root := a.pw, a.root
		a.mu.Unlock()
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if pw != "" && root > 0 && descends(c, root) {
			io.WriteString(c, pw+"\n")
		}
		c.Close()
	}
	a.ln.Close()
}

// descends reports whether the process at the other end of c is root or one
// of its descendants.
func descends(c *net.UnixConn, root int) bool {
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	pid := 0
	raw.Control(func(fd uintptr) { pid, err = peerPID(int(fd)) })
	if err != nil || pid <= 0 {
		return false
	}
	for range 32 {
		if pid == root {
			return true
		}
		if pid <= 1 {
			break
		}
		if pid, err = parentPID(pid); err != nil {
			break
		}
	}
	slog.Warn("brew: refused a password request from outside the update", "pid", pid)
	return false
}

// Close stops serving, forgets the password and removes the directory.
func (a *askpass) Close() {
	a.mu.Lock()
	a.pw = ""
	a.mu.Unlock()
	if a.ln != nil {
		a.ln.Close()
	}
	os.RemoveAll(a.dir)
}

// cleanAskpassDirs removes askpass directories this user left behind when
// Wargo stopped during an update.
func cleanAskpassDirs() {
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), askpassPrefix+"*"))
	for _, d := range dirs {
		st, err := os.Lstat(d)
		if err != nil || !st.IsDir() {
			continue
		}
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) == os.Getuid() {
			os.RemoveAll(d)
		}
	}
}

// AskpassMain is `wargo brew-askpass <socket>`, run by sudo as SUDO_ASKPASS
// (sudo passes its prompt as a second argument, which is ignored). It prints
// the password Wargo holds for the running update.
func AskpassMain(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: wargo brew-askpass <socket>")
		return 2
	}
	c, err := net.DialTimeout("unix", args[0], 5*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wargo: no administrator password was provided for this update")
		return 1
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, _ := io.ReadAll(io.LimitReader(c, 4096))
	if len(b) == 0 {
		fmt.Fprintln(os.Stderr, "wargo: no administrator password was provided for this update")
		return 1
	}
	os.Stdout.Write(b)
	return 0
}
