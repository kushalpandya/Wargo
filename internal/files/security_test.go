// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"archive/zip"
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// within fails the test if fn doesn't return in time (a FIFO open blocks
// forever without O_NONBLOCK).
func within(t *testing.T, d time.Duration, fn func() *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	ch := make(chan *httptest.ResponseRecorder, 1)
	go func() { ch <- fn() }()
	select {
	case w := <-ch:
		return w
	case <-time.After(d):
		t.Fatal("request blocked")
		return nil
	}
}

func TestFIFONeverBlocks(t *testing.T) {
	e := newEnv(t)
	if err := syscall.Mkfifo(filepath.Join(e.dir, "docs/pipe"), 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	for _, c := range []struct{ method, url string }{
		{"GET", "/api/files/list?path=docs/pipe"},
		{"GET", "/api/files/content?path=docs/pipe"},
		{"GET", "/api/files/download?path=docs/pipe"},
	} {
		w := within(t, 3*time.Second, func() *httptest.ResponseRecorder { return e.do(c.method, c.url, nil) })
		if w.Code < 400 {
			t.Fatalf("%s: status %d", c.url, w.Code)
		}
	}
	// A zip of the folder skips the FIFO.
	w := within(t, 3*time.Second, func() *httptest.ResponseRecorder {
		return e.do("GET", "/api/files/download-zip?path=docs", nil)
	})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len())); err != nil {
		t.Fatal(err)
	}
	// Extracting a FIFO is refused rather than hanging.
	w = within(t, 3*time.Second, func() *httptest.ResponseRecorder {
		return e.do("POST", "/api/files/extract", map[string]any{"path": "docs/pipe"})
	})
	if w.Code < 400 {
		t.Fatal(w.Code)
	}
}

func TestZipDuplicatePaths(t *testing.T) {
	e := newEnv(t)
	w := e.do("GET", "/api/files/download-zip?path=docs/a.txt&path=docs/a.txt&path=/docs/a.txt", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("entries = %d", len(zr.File))
	}
}

func TestControlCharNames(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"a\nb", "a\rb", "x\x1b[31m", "del\x7f", "c1\u0085"} {
		expectErr(t, e.do("POST", "/api/files/mkdir", map[string]any{"path": "docs/" + n}), 400, "")
		expectErr(t, e.do("POST", "/api/files/rename", map[string]any{"path": "docs/a.txt", "name": n}), 400, "")
		expectErr(t, e.do("POST", "/api/files/upload/init", map[string]any{"dir": "docs", "name": n, "size": 1}), 400, "")
	}
	// Existing files with odd names can still be edited.
	must(t, os.WriteFile(filepath.Join(e.dir, "docs/odd\nname"), []byte("x"), 0o644))
	st, _ := os.Stat(filepath.Join(e.dir, "docs/odd\nname"))
	w := e.do("PUT", "/api/files/content", map[string]any{"path": "docs/odd\nname", "content": "y", "mtime": st.ModTime()})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestTrashLongName(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("é", 127) // 254 bytes
	must(t, os.WriteFile(filepath.Join(e.dir, long), []byte("x"), 0o644))
	w := within(t, 5*time.Second, func() *httptest.ResponseRecorder {
		return e.do("POST", "/api/files/delete", map[string]any{"paths": []string{long}, "trash": true})
	})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	ents, _ := os.ReadDir(e.svc.trashDir)
	if len(ents) != 1 || len(ents[0].Name()) > 255 || !utf8.ValidString(ents[0].Name()) {
		t.Fatalf("trash = %v", ents)
	}
	// Editing a file with a 255-byte name works (the temp name is shortened).
	long2 := strings.Repeat("b", 255)
	must(t, os.WriteFile(filepath.Join(e.dir, long2), []byte("x"), 0o644))
	st, _ := os.Stat(filepath.Join(e.dir, long2))
	if w := e.do("PUT", "/api/files/content", map[string]any{"path": long2, "content": "y", "mtime": st.ModTime()}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestSideName(t *testing.T) {
	for _, base := range []string{"a", strings.Repeat("x", 300), strings.Repeat("ü", 200)} {
		n := sideName(".", base, ".wargo-0123456789ab")
		if len(n) > 255 || !utf8.ValidString(n) || !strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".wargo-0123456789ab") {
			t.Fatalf("sideName(%d bytes) = %q", len(base), n)
		}
	}
}

func TestDispositionInjection(t *testing.T) {
	for _, n := range []string{"a\r\nSet-Cookie: x=1", `q"uote.txt`, "ünï.txt"} {
		v := disposition("attachment", n)
		if strings.ContainsAny(v, "\r\n") || !strings.HasPrefix(v, "attachment") {
			t.Fatalf("%q -> %q", n, v)
		}
	}
}

func TestUploadLimit(t *testing.T) {
	e := newEnv(t)
	old := maxUploads
	maxUploads = 2
	t.Cleanup(func() { maxUploads = old })
	for i := 0; i < 2; i++ {
		if w := e.do("POST", "/api/files/upload/init", map[string]any{"dir": ".", "name": "u" + itoa(i), "size": 1}); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	expectErr(t, e.do("POST", "/api/files/upload/init", map[string]any{"dir": ".", "name": "u9", "size": 1}), 429, "too_many_uploads")
}

func TestUploadNoReplaceRace(t *testing.T) {
	e := newEnv(t)
	w := e.do("POST", "/api/files/upload/init", map[string]any{"dir": ".", "name": "late.txt", "size": 3})
	in := decodeJSON[initRes](t, w)
	e.do("PUT", "/api/files/upload/"+in.UploadID+"?offset=0", []byte("new"))
	// Someone creates the file after init: completing must not replace it.
	must(t, os.WriteFile(filepath.Join(e.dir, "late.txt"), []byte("old"), 0o644))
	expectErr(t, e.do("POST", "/api/files/upload/"+in.UploadID+"/complete", nil), 409, "exists")
	if b, _ := os.ReadFile(filepath.Join(e.dir, "late.txt")); string(b) != "old" {
		t.Fatal("existing file was replaced")
	}
}
