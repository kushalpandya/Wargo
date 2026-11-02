// SPDX-License-Identifier: AGPL-3.0-only

// Package host is the single place that knows whether Wargo runs natively
// on the host or inside a container ("container mode", enabled by setting
// WARGO_HOST_ROOT to the directory where the host's / is mounted, e.g.
// /host).
//
// In native mode every function here is the identity or a thin wrapper
// around the standard library, so behaviour is unchanged. In container mode
// host paths are mapped below the host root, the host's user database is
// parsed from its /etc files, commands run in the host's namespaces via
// nsenter, and file operations switch the calling thread's filesystem
// identity to the admin user (see AsUser).
package host

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// EnvRoot enables container mode.
const EnvRoot = "WARGO_HOST_ROOT"

var (
	mu   sync.RWMutex
	root = cleanRoot(os.Getenv(EnvRoot))
)

func cleanRoot(r string) string {
	r = strings.TrimSpace(r)
	if r == "" {
		return ""
	}
	r = filepath.Clean(r)
	if r == "/" || !filepath.IsAbs(r) {
		// "/" would make container mode pointless and a relative root is a
		// misconfiguration; both mean native.
		return ""
	}
	return r
}

// SetRoot switches the mode (tests and startup only). "" means native.
// It returns a function restoring the previous root.
func SetRoot(r string) (restore func()) {
	mu.Lock()
	prev := root
	root = cleanRoot(r)
	mu.Unlock()
	resetCaches()
	return func() {
		mu.Lock()
		root = prev
		mu.Unlock()
		resetCaches()
	}
}

// Container reports whether Wargo runs in container mode.
func Container() bool { return Root() != "" }

// Root returns the directory holding the host's root filesystem, or "" in
// native mode.
func Root() string {
	mu.RLock()
	defer mu.RUnlock()
	return root
}

// Path maps an absolute host path to the path Wargo must open. Relative
// paths are returned unchanged. It does not follow symlinks; use Resolve
// when the path may contain absolute symlinks.
func Path(p string) string {
	r := Root()
	if r == "" || !strings.HasPrefix(p, "/") {
		return p
	}
	return filepath.Join(r, filepath.Clean(p))
}

// Unpath converts a path below the host root back to the host's view.
// ok is false when p is outside the host root. Native: (p, true).
func Unpath(p string) (string, bool) {
	r := Root()
	if r == "" {
		return p, true
	}
	p = filepath.Clean(p)
	if p == r {
		return "/", true
	}
	if rest, ok := strings.CutPrefix(p, r+"/"); ok {
		return "/" + rest, true
	}
	return p, false
}

// maxLinks bounds symlink resolution like the kernel's ELOOP limit.
const maxLinks = 40

var errLoop = errors.New("too many levels of symbolic links")

// Resolve returns the path to open for the absolute host path p with every
// symlink resolved inside the host root: absolute link targets are taken
// relative to the host's /, and ".." never climbs above it. The final
// result is a path Wargo can open (host.Path form). Components that don't
// exist are kept as they are. Native mode returns p unchanged and lets the
// kernel follow links.
func Resolve(p string) (string, error) {
	r := Root()
	if r == "" || !strings.HasPrefix(p, "/") {
		return p, nil
	}
	hp, err := resolveIn(r, p)
	if err != nil {
		return "", err
	}
	return filepath.Join(r, hp), nil
}

// resolveIn resolves host path p against root r and returns the resolved
// host path.
func resolveIn(r, p string) (string, error) {
	pending := splitPath(p)
	cur := "/"
	links := 0
	for len(pending) > 0 {
		c := pending[0]
		pending = pending[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			cur = path.Dir(cur)
			continue
		}
		next := path.Join(cur, c)
		st, err := os.Lstat(filepath.Join(r, next))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Keep the rest literally (callers may be about to create it).
				rest := path.Join(append([]string{next}, pending...)...)
				return path.Clean(rest), nil
			}
			return "", err
		}
		if st.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		links++
		if links > maxLinks {
			return "", &fs.PathError{Op: "resolve", Path: p, Err: errLoop}
		}
		t, err := os.Readlink(filepath.Join(r, next))
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(t, "/") {
			cur = "/"
		}
		pending = append(splitPath(t), pending...)
	}
	return cur, nil
}

func splitPath(p string) []string { return strings.Split(p, "/") }

// ReadFile reads an absolute host path (symlinks resolved inside the host
// root in container mode).
func ReadFile(p string) ([]byte, error) {
	rp, err := Resolve(p)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(rp)
}

// Open opens an absolute host path for reading.
func Open(p string) (*os.File, error) {
	rp, err := Resolve(p)
	if err != nil {
		return nil, err
	}
	return os.Open(rp)
}

// Stat stats an absolute host path, following symlinks.
func Stat(p string) (fs.FileInfo, error) {
	rp, err := Resolve(p)
	if err != nil {
		return nil, err
	}
	return os.Stat(rp)
}

// EvalSymlinks resolves p (a host path) and returns the host path.
// Native: filepath.EvalSymlinks.
func EvalSymlinks(p string) (string, error) {
	r := Root()
	if r == "" {
		return filepath.EvalSymlinks(p)
	}
	hp, err := resolveIn(r, p)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(filepath.Join(r, hp)); err != nil {
		return "", err
	}
	return hp, nil
}

func resetCaches() {
	dbMu.Lock()
	dbCache = nil
	dbMu.Unlock()
	lookMu.Lock()
	lookCache = map[string]string{}
	lookMu.Unlock()
	adminMu.Lock()
	admin = nil
	adminMu.Unlock()
}
