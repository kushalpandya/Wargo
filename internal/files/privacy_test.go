// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/httpx"
)

func TestPrivacyProtected(t *testing.T) {
	h := "/Users/k"
	for p, want := range map[string]bool{
		"/Users/k/Desktop/a": true, "/Users/k/Downloads": true, "/Users/k/Library/Mobile Documents/x": true,
		"/Volumes/USB/f": true, "/Users/k/Code": false, "/Users/k/DesktopX": false, "/tmp": false,
	} {
		if privacyProtected(h, p) != want {
			t.Errorf("%s: want %v", p, want)
		}
	}
}

func TestPrivacyBlockedSkipsElevation(t *testing.T) {
	old := privacyBlocked
	privacyBlocked = func(error, string, string) bool { return true }
	oldRead := privacyReadBlocked
	privacyReadBlocked = func(error, string, string) bool { return true }
	defer func() { privacyBlocked, privacyReadBlocked = old, oldRead }()
	e := newEnv(t)
	ref := rootRef{Name: "Test", Path: e.dir}
	home, _ := os.UserHomeDir()
	cause := &os.PathError{Op: "open", Path: "x", Err: syscall.EPERM}
	err := e.svc.elevated(httptest.NewRequest("POST", "/x", nil), ref, cause, "remove", filepath.Join("Desktop", "a"), "", nil)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != "full_disk_access" {
		t.Fatalf("home %s: got %v", home, err)
	}
	if len(e.elev.calls) != 0 {
		t.Fatalf("helper was called: %v", e.elev.calls)
	}
	if _, err := e.svc.elevatedOut(httptest.NewRequest("POST", "/x", nil), ref, cause, "read", "a"); !errors.As(err, &he) || he.Code != "full_disk_access" {
		t.Fatalf("elevatedOut: %v", err)
	}
	if len(e.elev.calls) != 0 {
		t.Fatalf("helper was called: %v", e.elev.calls)
	}
}

func TestNoPrivacyDenialStillElevates(t *testing.T) {
	old := privacyBlocked
	privacyBlocked = func(error, string, string) bool { return false }
	defer func() { privacyBlocked = old }()
	e := newEnv(t)
	ref := rootRef{Name: "Test", Path: e.dir}
	_ = e.svc.elevated(httptest.NewRequest("POST", "/x", nil), ref, syscall.EACCES, "remove", "docs/a.txt", "", nil)
	if len(e.elev.calls) != 1 {
		t.Fatalf("want one helper call, got %v", e.elev.calls)
	}
}

func TestSymlinkInPathCarriesPath(t *testing.T) {
	err := withAbs(httpx.Errorf(400, "symlink_in_path", "x"), "/etc/hosts")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Data.(map[string]string)["abs"] != "/etc/hosts" {
		t.Fatalf("got %#v", err)
	}
	if other := httpx.Errorf(400, "other", "x"); withAbs(other, "/a") != error(other) {
		t.Fatal("other errors must pass through")
	}
}

func TestRootErrCarriesAbsForSymlink(t *testing.T) {
	err := rootRef{Path: "/etc"}.err(fsops.ErrSymlinkInPath, "hosts")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != "symlink_in_path" || he.Data.(map[string]string)["abs"] != "/etc/hosts" {
		t.Fatalf("got %#v", err)
	}
}

func TestPairErrReportsDestinationLink(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir()) // /var is a link on macOS
	must(t, err)
	must(t, os.MkdirAll(filepath.Join(dir, "real"), 0o755))
	must(t, os.Symlink("real", filepath.Join(dir, "lnk")))
	must(t, os.WriteFile(filepath.Join(dir, "a"), nil, 0o644))
	ref := rootRef{Path: dir}
	tr := &transferRoots{sref: ref, dref: ref} // same root
	raw := httpx.Errorf(400, "symlink_in_path", "Path contains a symbolic link")
	err = tr.pairErr(raw, pair{src: "a", dst: "lnk/a"})
	var he *httpx.Error
	if !errors.As(err, &he) {
		t.Fatalf("got %v", err)
	}
	if got := he.Data.(map[string]string)["abs"]; got != filepath.Join(dir, "lnk/a") {
		t.Fatalf("abs = %q, want destination", got)
	}
	// a link on the source side keeps the source path
	err = tr.pairErr(raw, pair{src: "lnk/a", dst: "b"})
	errors.As(err, &he)
	if got := he.Data.(map[string]string)["abs"]; got != filepath.Join(dir, "lnk/a") {
		t.Fatalf("abs = %q, want source", got)
	}
}
