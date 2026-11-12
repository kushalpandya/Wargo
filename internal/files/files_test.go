// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/jobs"
)

type fakeElev struct{ calls [][]string }

func (f *fakeElev) Run(r *http.Request, action, target string, argv []string, stdin io.Reader) ([]byte, error) {
	f.calls = append(f.calls, argv)
	if r.Header.Get("X-Wargo-Elevation") == "" {
		return nil, &httpErr{}
	}
	return nil, nil
}
func (f *fakeElev) Helper(args ...string) []string {
	return append([]string{"wargo", "helper"}, args...)
}

type httpErr struct{}

func (*httpErr) Error() string { return "elevation_required" }

type env struct {
	t    *testing.T
	dir  string // root dir
	svc  *Service
	mux  *http.ServeMux
	elev *fakeElev
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "root")
	must(t, os.MkdirAll(filepath.Join(dir, "docs/sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "docs/a.txt"), []byte("hello"), 0o640))
	must(t, os.WriteFile(filepath.Join(dir, "docs/sub/b.txt"), []byte("world"), 0o644))
	must(t, os.WriteFile(filepath.Join(base, "secret.txt"), []byte("outside"), 0o644))
	must(t, os.Symlink("/etc", filepath.Join(dir, "etc-link")))
	must(t, os.Symlink("../secret.txt", filepath.Join(dir, "rel-escape")))
	must(t, os.Symlink("docs/a.txt", filepath.Join(dir, "inside-link")))
	el := &fakeElev{}
	svc := NewWithOptions(Options{
		Roots:   []config.Root{{Name: "Test", Path: dir}},
		DataDir: filepath.Join(base, "data"), TrashDir: filepath.Join(base, "trash"),
		MaxEditSize: 1 << 20, Jobs: jobs.NewManager(events.NewHub()), Elevate: el,
	})
	mux := http.NewServeMux()
	svc.Register(mux)
	return &env{t: t, dir: dir, svc: svc, mux: mux, elev: el}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) do(method, url string, body any, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req := httptest.NewRequest(method, url, rd)
	if _, ok := body.([]byte); !ok && body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

func decodeJSON[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

type apiErr struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func expectErr(t *testing.T, w *httptest.ResponseRecorder, status int, code string) apiErr {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d (%s)", w.Code, status, w.Body.String())
	}
	e := decodeJSON[apiErr](t, w)
	if code != "" && e.Code != code {
		t.Fatalf("code = %q, want %q (%s)", e.Code, code, e.Message)
	}
	return e
}

func waitJob(t *testing.T, m *jobs.Manager, id string) jobs.Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := m.Get(id).Snapshot()
		if s.State != jobs.Running {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return jobs.Snapshot{}
}

// ---- confinement -----------------------------------------------------------

func TestConfinement(t *testing.T) {
	e := newEnv(t)
	// ".." and absolute paths are normalised into the root.
	for _, p := range []string{"../../etc/passwd", "/etc/passwd", "docs/../../../etc/passwd", `..\..\etc\passwd`} {
		w := e.do("GET", "/api/files/download?path="+p, nil)
		expectErr(t, w, 404, "not_found")
	}
	// Symlinks that leave the root are refused.
	for _, p := range []string{"etc-link/passwd", "etc-link", "rel-escape"} {
		w := e.do("GET", "/api/files/download?path="+p, nil)
		expectErr(t, w, 403, "outside_root")
		w = e.do("GET", "/api/files/content?path="+p, nil)
		expectErr(t, w, 403, "outside_root")
	}
	w := e.do("GET", "/api/files/list?path=etc-link", nil)
	expectErr(t, w, 403, "outside_root")
	// Writes through an escaping link fail too.
	w = e.do("POST", "/api/files/mkdir", map[string]string{"path": "etc-link/wargo-test"})
	expectErr(t, w, 403, "outside_root")
	if _, err := os.Stat("/etc/wargo-test"); err == nil {
		t.Fatal("created outside root")
	}
	w = e.do("POST", "/api/files/copy", map[string]any{"paths": []string{"docs/a.txt"}, "dest": "etc-link"})
	expectErr(t, w, 403, "outside_root")

	// Listing reports link targets but marks escaping ones.
	w = e.do("GET", "/api/files/list?path=.", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	res := decodeJSON[struct{ Entries []Entry }](t, w)
	got := map[string]Entry{}
	for _, en := range res.Entries {
		got[en.Name] = en
	}
	if got["etc-link"].TargetType != "outside" || got["etc-link"].Target != "/etc" {
		t.Errorf("etc-link = %+v", got["etc-link"])
	}
	if got["rel-escape"].TargetType != "outside" {
		t.Errorf("rel-escape = %+v", got["rel-escape"])
	}
	if got["inside-link"].TargetType != "file" || got["inside-link"].Size != 5 {
		t.Errorf("inside-link = %+v", got["inside-link"])
	}
	if res.Entries[0].Name != "docs" {
		t.Errorf("dirs should sort first, got %s", res.Entries[0].Name)
	}
	// Inside links work.
	w = e.do("GET", "/api/files/download?path=inside-link", nil)
	if w.Code != 200 || w.Body.String() != "hello" {
		t.Fatalf("inside link download: %d %s", w.Code, w.Body.String())
	}
}

func TestUnknownRoot(t *testing.T) {
	e := newEnv(t)
	expectErr(t, e.do("GET", "/api/files/list?root=nope", nil), 404, "unknown_root")
	expectErr(t, e.do("GET", "/api/files/list?root=7", nil), 404, "unknown_root")
	if w := e.do("GET", "/api/files/list?root=Test&path=docs", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

// ---- download --------------------------------------------------------------

func TestDownloadHeaders(t *testing.T) {
	e := newEnv(t)
	must(t, os.WriteFile(filepath.Join(e.dir, "x.svg"), []byte(`<svg onload="alert(1)"/>`), 0o644))
	must(t, os.WriteFile(filepath.Join(e.dir, "p.html"), []byte(`<script>alert(1)</script>`), 0o644))
	for _, name := range []string{"x.svg", "p.html"} {
		w := e.do("GET", "/api/files/download?inline=1&path="+name, nil)
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("%s inline content-type = %q", name, ct)
		}
		if csp := w.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox") {
			t.Errorf("%s csp = %q", name, csp)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Error("missing nosniff")
		}
	}
	w := e.do("GET", "/api/files/download?path=p.html", nil)
	if !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("disposition = %q", w.Header().Get("Content-Disposition"))
	}
	// Range support.
	w = e.do("GET", "/api/files/download?path=docs/a.txt", nil, "Range", "bytes=1-3")
	if w.Code != 206 || w.Body.String() != "ell" {
		t.Fatalf("range: %d %q", w.Code, w.Body.String())
	}
	// Folder → zip.
	w = e.do("GET", "/api/files/download?path=docs", nil)
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	must(t, err)
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["docs/a.txt"] || !names["docs/sub/b.txt"] {
		t.Fatalf("zip names = %v", names)
	}
	// Multiple paths.
	w = e.do("GET", "/api/files/download-zip?path=docs/a.txt&path=docs/sub", nil)
	zr, err = zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	must(t, err)
	if len(zr.File) < 2 || zr.File[0].Name != "a.txt" {
		t.Fatalf("multi zip = %v", zr.File)
	}
}

// ---- uploads ---------------------------------------------------------------

type initRes struct {
	UploadID  string `json:"uploadId"`
	ChunkSize int    `json:"chunkSize"`
	Received  int64  `json:"received"`
	Resumed   bool   `json:"resumed"`
}

func TestUploadResume(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("0123456789"), 1000) // 10 kB
	initReq := map[string]any{"dir": "docs", "name": "up.bin", "size": len(data)}
	w := e.do("POST", "/api/files/upload/init", initReq)
	in := decodeJSON[initRes](t, w)
	if in.UploadID == "" || in.Received != 0 || in.ChunkSize != chunkSize {
		t.Fatalf("init = %+v", in)
	}
	base := "/api/files/upload/" + in.UploadID
	// First chunk.
	if w := e.do("PUT", base+"?offset=0", data[:4000]); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Wrong offset → 409 with the current offset.
	w = e.do("PUT", base+"?offset=1000", data[1000:2000])
	er := expectErr(t, w, 409, "offset_mismatch")
	if !strings.Contains(string(er.Data), `"offset":4000`) {
		t.Fatalf("data = %s", er.Data)
	}
	// Checksum mismatch leaves the offset unchanged.
	w = e.do("PUT", base+"?offset=4000", data[4000:5000], "X-Chunk-Sha256", strings.Repeat("0", 64))
	expectErr(t, w, 422, "checksum_mismatch")
	// Completing early fails.
	expectErr(t, e.do("POST", base+"/complete", nil), 409, "offset_mismatch")
	// "Reload": init again with the upload id resumes the same upload.
	w = e.do("POST", "/api/files/upload/init", map[string]any{"dir": "docs", "name": "up.bin", "size": len(data), "uploadId": in.UploadID})
	in2 := decodeJSON[initRes](t, w)
	if in2.UploadID != in.UploadID || in2.Received != 4000 || !in2.Resumed {
		t.Fatalf("resume = %+v", in2)
	}
	// Also survives a service restart (sidecar on disk).
	e.svc.uploads = newUploads(e.svc.uploads.dir)
	sum := sha256.Sum256(data[4000:])
	w = e.do("PUT", base+"?offset=4000", data[4000:], "X-Chunk-Sha256", hex.EncodeToString(sum[:]))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Exceeding the declared size is refused.
	expectErr(t, e.do("PUT", base+"?offset="+itoa(len(data)), []byte("x")), 400, "")
	if w := e.do("POST", base+"/complete", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(e.dir, "docs/up.bin"))
	must(t, err)
	if !bytes.Equal(got, data) {
		t.Fatal("content mismatch")
	}
	if _, err := os.Stat(e.svc.uploads.part(in.UploadID)); !os.IsNotExist(err) {
		t.Fatal("part file left behind")
	}
	// Existing target without overwrite.
	expectErr(t, e.do("POST", "/api/files/upload/init", initReq), 409, "exists")
	initReq["overwrite"] = true
	if w := e.do("POST", "/api/files/upload/init", initReq); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

func TestUploadInitResumesOnlyWithID(t *testing.T) {
	e := newEnv(t)
	data := bytes.Repeat([]byte("abcdefghij"), 1000)
	req := map[string]any{"dir": "docs", "name": "r.bin", "size": len(data)}
	a := decodeJSON[initRes](t, e.do("POST", "/api/files/upload/init", req))
	if w := e.do("PUT", "/api/files/upload/"+a.UploadID+"?offset=0", data[:5000]); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	b := decodeJSON[initRes](t, e.do("POST", "/api/files/upload/init", req))
	if b.UploadID == a.UploadID || b.Received != 0 || b.Resumed {
		t.Fatalf("no id must start a new upload: %+v", b)
	}
	c := decodeJSON[initRes](t, e.do("POST", "/api/files/upload/init", map[string]any{"dir": "docs", "name": "r.bin", "size": len(data), "uploadId": a.UploadID}))
	if c.UploadID != a.UploadID || c.Received != 5000 || !c.Resumed {
		t.Fatalf("resume with id = %+v", c)
	}
	// An id for a different file is not honoured.
	d := decodeJSON[initRes](t, e.do("POST", "/api/files/upload/init", map[string]any{"dir": "docs", "name": "other.bin", "size": len(data), "uploadId": a.UploadID}))
	if d.UploadID == a.UploadID || d.Received != 0 {
		t.Fatalf("mismatched id = %+v", d)
	}
	// The new upload completes even though the old one is still unfinished.
	if w := e.do("PUT", "/api/files/upload/"+b.UploadID+"?offset=0", data); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := e.do("POST", "/api/files/upload/"+b.UploadID+"/complete", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

func TestUploadNestedAndCancel(t *testing.T) {
	e := newEnv(t)
	w := e.do("POST", "/api/files/upload/init", map[string]any{"dir": ".", "name": "new/deep/f.txt", "size": 3})
	in := decodeJSON[initRes](t, w)
	e.do("PUT", "/api/files/upload/"+in.UploadID+"?offset=0", []byte("abc"))
	if w := e.do("POST", "/api/files/upload/"+in.UploadID+"/complete", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if b, _ := os.ReadFile(filepath.Join(e.dir, "new/deep/f.txt")); string(b) != "abc" {
		t.Fatal("nested upload failed")
	}
	w = e.do("POST", "/api/files/upload/init", map[string]any{"dir": ".", "name": "c.txt", "size": 3})
	in = decodeJSON[initRes](t, w)
	if w := e.do("DELETE", "/api/files/upload/"+in.UploadID, nil); w.Code != 204 {
		t.Fatal(w.Code)
	}
	expectErr(t, e.do("PUT", "/api/files/upload/"+in.UploadID+"?offset=0", []byte("abc")), 404, "upload_not_found")
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestUploadNameEscape(t *testing.T) {
	e := newEnv(t)
	// ".." in the name is rejected outright.
	expectErr(t, e.do("POST", "/api/files/upload/init", map[string]any{"dir": ".", "name": "../../x.txt", "size": 1}), 400, "")
	expectErr(t, e.do("POST", "/api/files/upload/init", map[string]any{"dir": ".", "name": "a/../../x.txt", "size": 1}), 400, "")
	// ".." in the folder is folded into the root.
	w := e.do("POST", "/api/files/upload/init", map[string]any{"dir": "../..", "name": "x.txt", "size": 1})
	in := decodeJSON[initRes](t, w)
	e.do("PUT", "/api/files/upload/"+in.UploadID+"?offset=0", []byte("x"))
	e.do("POST", "/api/files/upload/"+in.UploadID+"/complete", nil)
	if _, err := os.Stat(filepath.Join(e.dir, "x.txt")); err != nil {
		t.Fatal("expected x.txt to land inside the root")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.dir), "x.txt")); err == nil {
		t.Fatal("upload escaped the root")
	}
}

// ---- extraction --------------------------------------------------------------

func TestExtractZipSlip(t *testing.T) {
	e := newEnv(t)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range []string{"../evil.txt", "../../evil2.txt", "/abs.txt", "ok/a.txt", `..\win.txt`, "ok/../../dots.txt"} {
		f, _ := zw.Create(n)
		f.Write([]byte("x"))
	}
	hdr := &zip.FileHeader{Name: "link"}
	hdr.SetMode(fs.ModeSymlink | 0o777)
	f, _ := zw.CreateHeader(hdr)
	f.Write([]byte("/etc"))
	zw.Close()
	must(t, os.WriteFile(filepath.Join(e.dir, "docs/bad.zip"), buf.Bytes(), 0o644))

	w := e.do("POST", "/api/files/extract", map[string]string{"path": "docs/bad.zip"})
	res := decodeJSON[struct{ JobID, Dest string }](t, w)
	if res.Dest != "docs/bad" {
		t.Fatalf("dest = %q", res.Dest)
	}
	snap := waitJob(t, e.svc.jobs, res.JobID)
	if snap.State != jobs.Succeeded {
		t.Fatalf("job = %+v", snap)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "docs/bad/ok/a.txt")); err != nil {
		t.Fatal("safe entry missing")
	}
	base := filepath.Dir(e.dir)
	for _, p := range []string{"evil.txt", "evil2.txt", "abs.txt", "win.txt", "dots.txt"} {
		for _, d := range []string{base, e.dir, filepath.Join(e.dir, "docs"), filepath.Join(e.dir, "docs/bad")} {
			if _, err := os.Lstat(filepath.Join(d, p)); err == nil {
				t.Errorf("%s written to %s", p, d)
			}
		}
	}
	if _, err := os.Lstat(filepath.Join(e.dir, "docs/bad/link")); err == nil {
		t.Error("symlink was extracted")
	}
	if snap.Result["skipped"].(int) != 6 {
		t.Errorf("skipped = %v", snap.Result["skipped"])
	}
}

func TestExtractTarAndLimits(t *testing.T) {
	e := newEnv(t)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name string, body string) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	add("../slip.txt", "x")
	add("good.txt", "good")
	tw.WriteHeader(&tar.Header{Name: "l", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	add("big.txt", strings.Repeat("y", 5000))
	tw.Close()
	must(t, os.WriteFile(filepath.Join(e.dir, "a.tar"), buf.Bytes(), 0o644))

	w := e.do("POST", "/api/files/extract", map[string]string{"path": "a.tar", "dest": "out"})
	res := decodeJSON[struct{ JobID string }](t, w)
	if s := waitJob(t, e.svc.jobs, res.JobID); s.State != jobs.Succeeded {
		t.Fatalf("job = %+v", s)
	}
	if b, _ := os.ReadFile(filepath.Join(e.dir, "out/good.txt")); string(b) != "good" {
		t.Fatal("good.txt missing")
	}
	if _, err := os.Lstat(filepath.Join(e.dir, "slip.txt")); err == nil {
		t.Fatal("tar slip")
	}
	// Size cap.
	old := maxExtractBytes
	maxExtractBytes = 1000
	defer func() { maxExtractBytes = old }()
	w = e.do("POST", "/api/files/extract", map[string]string{"path": "a.tar", "dest": "out2"})
	res = decodeJSON[struct{ JobID string }](t, w)
	if s := waitJob(t, e.svc.jobs, res.JobID); s.State != jobs.Failed || !strings.Contains(s.Error, "limit") {
		t.Fatalf("expected size-limit failure, got %+v", s)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "out2/big.txt")); err == nil {
		t.Fatal("oversized file left behind")
	}
	// Non-archives are refused.
	expectErr(t, e.do("POST", "/api/files/extract", map[string]string{"path": "docs/a.txt"}), 415, "unsupported_archive")
}

func TestArchiveRoundTrip(t *testing.T) {
	e := newEnv(t)
	for _, format := range []string{"zip", "tar.gz"} {
		w := e.do("POST", "/api/files/archive", map[string]any{"paths": []string{"docs"}, "format": format})
		res := decodeJSON[struct{ JobID, Path string }](t, w)
		if s := waitJob(t, e.svc.jobs, res.JobID); s.State != jobs.Succeeded {
			t.Fatalf("%s archive: %+v", format, s)
		}
		dest := "x-" + strings.ReplaceAll(format, ".", "")
		w = e.do("POST", "/api/files/extract", map[string]string{"path": res.Path, "dest": dest})
		res2 := decodeJSON[struct{ JobID string }](t, w)
		if s := waitJob(t, e.svc.jobs, res2.JobID); s.State != jobs.Succeeded {
			t.Fatalf("%s extract: %+v", format, s)
		}
		if b, _ := os.ReadFile(filepath.Join(e.dir, dest, "docs/sub/b.txt")); string(b) != "world" {
			t.Fatalf("%s round trip failed", format)
		}
	}
	// Archive inside the folder being archived is refused.
	expectErr(t, e.do("POST", "/api/files/archive", map[string]any{"paths": []string{"docs"}, "dest": "docs/x.zip"}), 400, "")
}

// A file that appears at the archive's name while it is being written is
// kept: the finished archive never replaces it.
func TestArchiveCreateDoesNotReplace(t *testing.T) {
	e := newEnv(t)
	old := archiveRename
	archiveRename = func(sr *os.Root, src string, dr *os.Root, dst string) error {
		must(t, os.WriteFile(filepath.Join(e.dir, dst), []byte("mine"), 0o644))
		return old(sr, src, dr, dst)
	}
	defer func() { archiveRename = old }()
	w := e.do("POST", "/api/files/archive", map[string]any{"paths": []string{"docs"}, "format": "zip"})
	res := decodeJSON[struct{ JobID, Path string }](t, w)
	s := waitJob(t, e.svc.jobs, res.JobID)
	if s.State != jobs.Failed || !strings.Contains(s.Error, "exists") {
		t.Fatalf("job = %+v", s)
	}
	if b, _ := os.ReadFile(filepath.Join(e.dir, res.Path)); string(b) != "mine" {
		t.Fatalf("%s replaced: %q", res.Path, b)
	}
	ents, _ := os.ReadDir(e.dir)
	for _, d := range ents {
		if strings.HasSuffix(d.Name(), ".partial") {
			t.Errorf("temporary archive left behind: %s", d.Name())
		}
	}
}

// Extraction stops before it would leave less than extractReserve free,
// and removes the folder it created.
func TestTarExtractStopsBeforeReserve(t *testing.T) {
	e := newEnv(t)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "big.bin", Mode: 0o644, Size: 1 << 20, Typeflag: tar.TypeReg})
	tw.Write(make([]byte, 1<<20))
	tw.Close()
	must(t, os.WriteFile(filepath.Join(e.dir, "a.tar"), buf.Bytes(), 0o644))
	old := freeSpaceIn
	freeSpaceIn = func(*os.Root, string) (uint64, uint64, bool) { return maxExtractReserve + 10, 1 << 40, true }
	defer func() { freeSpaceIn = old }()

	w := e.do("POST", "/api/files/extract", map[string]string{"path": "a.tar", "dest": "out"})
	res := decodeJSON[struct{ JobID string }](t, w)
	if s := waitJob(t, e.svc.jobs, res.JobID); s.State != jobs.Failed || !strings.Contains(s.Error, "free space") {
		t.Fatalf("job = %+v", s)
	}
	if _, err := os.Lstat(filepath.Join(e.dir, "out")); err == nil {
		t.Fatal("destination folder left behind")
	}
	// A folder that existed before the job is kept.
	must(t, os.Mkdir(filepath.Join(e.dir, "mine"), 0o755))
	w = e.do("POST", "/api/files/extract", map[string]string{"path": "a.tar", "dest": "mine"})
	res = decodeJSON[struct{ JobID string }](t, w)
	if s := waitJob(t, e.svc.jobs, res.JobID); s.State != jobs.Failed {
		t.Fatalf("job = %+v", s)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "mine")); err != nil {
		t.Fatal("existing destination folder removed")
	}
}

func TestExtractReserveScalesWithDrive(t *testing.T) {
	if got := extractReserve(1 << 40); got != maxExtractReserve {
		t.Errorf("1 TiB drive: reserve %d, want 1 GiB", got)
	}
	if got := extractReserve(4 << 30); got != (4<<30)/20 {
		t.Errorf("4 GiB drive: reserve %d, want 5%%", got)
	}
}

// On a small drive the reserve is 5% of its size, not 1 GiB, and folder
// entries (no data) are never refused for space.
func TestTarExtractSmallDriveReserve(t *testing.T) {
	e := newEnv(t)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "d/", Mode: 0o755, Typeflag: tar.TypeDir})
	tw.WriteHeader(&tar.Header{Name: "d/f.bin", Mode: 0o644, Size: 1 << 20, Typeflag: tar.TypeReg})
	tw.Write(make([]byte, 1<<20))
	tw.Close()
	must(t, os.WriteFile(filepath.Join(e.dir, "a.tar"), buf.Bytes(), 0o644))
	old := freeSpaceIn
	defer func() { freeSpaceIn = old }()

	// A 2 GiB drive with 200 MiB free: the reserve is about 102 MiB.
	freeSpaceIn = func(*os.Root, string) (uint64, uint64, bool) { return 200 << 20, 2 << 30, true }
	w := e.do("POST", "/api/files/extract", map[string]string{"path": "a.tar", "dest": "out"})
	res := decodeJSON[struct{ JobID string }](t, w)
	if s := waitJob(t, e.svc.jobs, res.JobID); s.State == jobs.Failed {
		t.Fatalf("job = %+v", s)
	}

	// Only folders, with less than the reserve free: still extracts.
	buf.Reset()
	tw = tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "only/", Mode: 0o755, Typeflag: tar.TypeDir})
	tw.Close()
	must(t, os.WriteFile(filepath.Join(e.dir, "dirs.tar"), buf.Bytes(), 0o644))
	freeSpaceIn = func(*os.Root, string) (uint64, uint64, bool) { return 1 << 20, 1 << 40, true }
	w = e.do("POST", "/api/files/extract", map[string]string{"path": "dirs.tar", "dest": "out2"})
	res = decodeJSON[struct{ JobID string }](t, w)
	if s := waitJob(t, e.svc.jobs, res.JobID); s.State == jobs.Failed {
		t.Fatalf("folders only: job = %+v", s)
	}
	if fi, err := os.Stat(filepath.Join(e.dir, "out2", "only")); err != nil || !fi.IsDir() {
		t.Fatalf("folder not extracted: %v", err)
	}
}

// A background move (the cross-device path) removes only what it copied
// and lists the special files it left behind.
func TestTransferMoveKeepsSpecialFiles(t *testing.T) {
	e := newEnv(t)
	if err := syscall.Mkfifo(filepath.Join(e.dir, "docs/sub/p"), 0o644); err != nil {
		t.Skip(err)
	}
	root, err := os.OpenRoot(e.dir)
	must(t, err)
	defer root.Close()
	job := e.svc.jobs.Start("files.move", "Move docs", func() {})
	res, err := transfer(t.Context(), root, root, job, []pair{{"docs", "moved"}}, true)
	must(t, err)
	if res["skipped"] != 1 {
		t.Errorf("skipped = %v", res["skipped"])
	}
	if b, _ := os.ReadFile(filepath.Join(e.dir, "moved/sub/b.txt")); string(b) != "world" {
		t.Error("moved/sub/b.txt missing")
	}
	if _, err := os.Lstat(filepath.Join(e.dir, "docs/sub/p")); err != nil {
		t.Error("FIFO removed from the source")
	}
	for _, p := range []string{"docs/a.txt", "docs/sub/b.txt"} {
		if _, err := os.Lstat(filepath.Join(e.dir, p)); err == nil {
			t.Errorf("%s not removed", p)
		}
	}
	if !strings.Contains(strings.Join(job.Lines(), "\n"), "Not moved (special file): docs/sub/p") {
		t.Errorf("log = %v", job.Lines())
	}
}

// ---- editor ----------------------------------------------------------------

func TestContentConflict(t *testing.T) {
	e := newEnv(t)
	w := e.do("GET", "/api/files/content?path=docs/a.txt", nil)
	c := decodeJSON[struct {
		Content  string
		MTime    time.Time
		Encoding string
	}](t, w)
	if c.Content != "hello" || c.Encoding != "utf-8" {
		t.Fatalf("content = %+v", c)
	}
	// Someone else edits the file.
	p := filepath.Join(e.dir, "docs/a.txt")
	future := c.MTime.Add(2 * time.Second)
	must(t, os.Chtimes(p, future, future))
	w = e.do("PUT", "/api/files/content", map[string]any{"path": "docs/a.txt", "content": "mine", "mtime": c.MTime})
	expectErr(t, w, 409, "conflict")
	if b, _ := os.ReadFile(p); string(b) != "hello" {
		t.Fatal("conflicting write went through")
	}
	w = e.do("PUT", "/api/files/content", map[string]any{"path": "docs/a.txt", "content": "mine", "mtime": future})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	st, _ := os.Stat(p)
	if b, _ := os.ReadFile(p); string(b) != "mine" || st.Mode().Perm() != 0o640 {
		t.Fatalf("after save: %q %v", b, st.Mode())
	}
	// No temp files left.
	ents, _ := os.ReadDir(filepath.Join(e.dir, "docs"))
	for _, en := range ents {
		if strings.Contains(en.Name(), ".wargo-") {
			t.Fatal("temp file left:", en.Name())
		}
	}
	// Binary refused.
	must(t, os.WriteFile(filepath.Join(e.dir, "bin"), []byte{1, 0, 2}, 0o644))
	expectErr(t, e.do("GET", "/api/files/content?path=bin", nil), 415, "binary")
	// Create.
	w = e.do("PUT", "/api/files/content", map[string]any{"path": "docs/new.txt", "content": "n", "create": true})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	expectErr(t, e.do("PUT", "/api/files/content", map[string]any{"path": "docs/new.txt", "content": "n", "create": true}), 409, "exists")
	// Saving through a symlink keeps the link.
	g := decodeJSON[struct{ MTime time.Time }](t, e.do("GET", "/api/files/content?path=inside-link", nil))
	if w := e.do("PUT", "/api/files/content", map[string]any{"path": "inside-link", "content": "via link", "mtime": g.MTime}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if st, _ := os.Lstat(filepath.Join(e.dir, "inside-link")); st.Mode()&fs.ModeSymlink == 0 {
		t.Fatal("symlink replaced by a file")
	}
	// Latin-1 round trip.
	must(t, os.WriteFile(filepath.Join(e.dir, "l1.txt"), []byte{'c', 'a', 'f', 0xe9}, 0o644))
	l := decodeJSON[struct {
		Content, Encoding string
		MTime             time.Time
	}](t, e.do("GET", "/api/files/content?path=l1.txt", nil))
	if l.Encoding != "latin1" || l.Content != "café" {
		t.Fatalf("latin1 = %+v", l)
	}
	e.do("PUT", "/api/files/content", map[string]any{"path": "l1.txt", "content": "café!", "mtime": l.MTime, "encoding": "latin1"})
	if b, _ := os.ReadFile(filepath.Join(e.dir, "l1.txt")); !bytes.Equal(b, []byte{'c', 'a', 'f', 0xe9, '!'}) {
		t.Fatalf("latin1 save = %v", b)
	}
}

// ---- mutations ---------------------------------------------------------------

func TestCopyMoveIntoSelf(t *testing.T) {
	e := newEnv(t)
	for _, op := range []string{"copy", "move"} {
		for _, dest := range []string{"docs", "docs/sub"} {
			w := e.do("POST", "/api/files/"+op, map[string]any{"paths": []string{"docs"}, "dest": dest})
			if op == "move" && dest == "docs" {
				expectErr(t, w, 400, "into_self")
				continue
			}
			expectErr(t, w, 400, "into_self")
		}
	}
	// Duplicate in place gets a unique name.
	w := e.do("POST", "/api/files/copy", map[string]any{"paths": []string{"docs/a.txt"}, "dest": "docs"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(e.dir, "docs/a (2).txt")); err != nil {
		t.Fatal("duplicate missing")
	}
	// Conflict reporting.
	must(t, os.WriteFile(filepath.Join(e.dir, "a.txt"), []byte("other"), 0o644))
	expectErr(t, e.do("POST", "/api/files/move", map[string]any{"paths": []string{"docs/a.txt"}, "dest": "."}), 409, "exists")
	w = e.do("POST", "/api/files/move", map[string]any{"paths": []string{"docs/a.txt"}, "dest": ".", "onConflict": "rename"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(e.dir, "a (2).txt")); err != nil {
		t.Fatal("keep-both move failed")
	}
	// Large copy becomes a job.
	old := inlineCopyBytes
	inlineCopyBytes = 1
	defer func() { inlineCopyBytes = old }()
	w = e.do("POST", "/api/files/copy", map[string]any{"paths": []string{"docs"}, "dest": "docs/sub/.."})
	expectErr(t, w, 400, "into_self")
	must(t, os.Mkdir(filepath.Join(e.dir, "target"), 0o755))
	w = e.do("POST", "/api/files/copy", map[string]any{"paths": []string{"docs"}, "dest": "target"})
	res := decodeJSON[struct{ JobID string }](t, w)
	if res.JobID == "" {
		t.Fatal("expected a job", w.Body.String())
	}
	if s := waitJob(t, e.svc.jobs, res.JobID); s.State != jobs.Succeeded {
		t.Fatalf("copy job: %+v", s)
	}
	if b, _ := os.ReadFile(filepath.Join(e.dir, "target/docs/sub/b.txt")); string(b) != "world" {
		t.Fatal("copy job result missing")
	}
}

func TestRenameMkdirDeleteTrash(t *testing.T) {
	e := newEnv(t)
	expectErr(t, e.do("POST", "/api/files/rename", map[string]string{"path": "docs/a.txt", "name": "../x"}), 400, "")
	expectErr(t, e.do("POST", "/api/files/rename", map[string]string{"path": "docs/a.txt", "name": ".."}), 400, "")
	if w := e.do("POST", "/api/files/rename", map[string]string{"path": "docs/a.txt", "name": "c.txt"}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := e.do("POST", "/api/files/mkdir", map[string]string{"path": "docs/newdir"}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	expectErr(t, e.do("POST", "/api/files/mkdir", map[string]string{"path": "docs/newdir"}), 409, "exists")
	expectErr(t, e.do("POST", "/api/files/delete", map[string]any{"paths": []string{"."}}), 400, "")
	expectErr(t, e.do("POST", "/api/files/delete", map[string]any{"paths": []string{"../.."}}), 400, "")

	w := e.do("POST", "/api/files/delete", map[string]any{"paths": []string{"docs/c.txt"}, "trash": true})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	ents, _ := os.ReadDir(e.svc.trashDir)
	if len(ents) != 1 || !strings.HasSuffix(ents[0].Name(), "-c.txt") {
		t.Fatalf("trash = %v", ents)
	}
	// Deleting the escaping symlink removes only the link.
	if w := e.do("POST", "/api/files/delete", map[string]any{"paths": []string{"etc-link"}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat("/etc/passwd"); err != nil {
		t.Fatal("!!")
	}
	if w := e.do("POST", "/api/files/delete", map[string]any{"paths": []string{"docs"}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(e.dir, "docs")); !os.IsNotExist(err) {
		t.Fatal("not deleted")
	}
}

func TestChmodAndElevation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	e := newEnv(t)
	if w := e.do("POST", "/api/files/chmod", map[string]any{"paths": []string{"docs"}, "mode": "750", "dirMode": "750", "recursive": true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	st, _ := os.Stat(filepath.Join(e.dir, "docs/sub/b.txt"))
	if st.Mode().Perm() != 0o750 {
		t.Fatalf("mode = %v", st.Mode())
	}
	expectErr(t, e.do("POST", "/api/files/chmod", map[string]any{"paths": []string{"docs"}, "mode": "999"}), 400, "")
	// Symlinks are never followed: not as the item, not as a folder on the way.
	must(t, os.Symlink("docs", filepath.Join(e.dir, "docs-link")))
	for _, p := range []string{"inside-link", "docs-link/a.txt"} {
		expectErr(t, e.do("POST", "/api/files/chmod", map[string]any{"paths": []string{p}, "mode": "600"}), 400, "symlink_in_path")
	}
	if st, _ := os.Stat(filepath.Join(e.dir, "docs/a.txt")); st.Mode().Perm() != 0o750 {
		t.Fatalf("chmod followed a symlink: %v", st.Mode())
	}

	// Permission errors on single-path ops go through the helper.
	locked := filepath.Join(e.dir, "locked")
	must(t, os.Mkdir(locked, 0o500))
	defer os.Chmod(locked, 0o755)
	w := e.do("POST", "/api/files/mkdir", map[string]string{"path": "locked/x"})
	if w.Code != 500 && w.Code != 403 { // fake elevator error w/o credential
		t.Fatalf("status %d", w.Code)
	}
	if len(e.elev.calls) != 1 {
		t.Fatalf("elevator calls = %v", e.elev.calls)
	}
	want := []string{"wargo", "helper", "fs", "mkdir", e.dir, "locked/x"}
	if strings.Join(e.elev.calls[0], " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v", e.elev.calls[0])
	}
	if w := e.do("POST", "/api/files/mkdir", map[string]string{"path": "locked/x"}, "X-Wargo-Elevation", "x"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Multi-path ops don't elevate.
	e.elev.calls = nil
	must(t, os.WriteFile(filepath.Join(e.dir, "f1"), nil, 0o644))
	os.Chmod(locked, 0o755)
	must(t, os.WriteFile(filepath.Join(locked, "f2"), nil, 0o644))
	os.Chmod(locked, 0o500)
	w = e.do("POST", "/api/files/delete", map[string]any{"paths": []string{"f1", "locked/f2"}})
	expectErr(t, w, 403, "permission_denied")
	if len(e.elev.calls) != 0 {
		t.Fatal("multi-path op elevated")
	}
}

// ---- pure helpers ------------------------------------------------------------

func TestLsMode(t *testing.T) {
	cases := []struct {
		m    fs.FileMode
		want string
		oct  string
	}{
		{0o644, "-rw-r--r--", "0644"},
		{fs.ModeDir | 0o755, "drwxr-xr-x", "0755"},
		{fs.ModeSymlink | 0o777, "lrwxrwxrwx", "0777"},
		{fs.ModeSetuid | 0o755, "-rwsr-xr-x", "4755"},
		{fs.ModeDir | fs.ModeSticky | 0o777, "drwxrwxrwt", "1777"},
		{fs.ModeSetgid | 0o640, "-rw-r-S---", "2640"},
	}
	for _, c := range cases {
		if got := lsMode(c.m); got != c.want {
			t.Errorf("lsMode(%v) = %s, want %s", c.m, got, c.want)
		}
		if got := octal(c.m); got != c.oct {
			t.Errorf("octal(%v) = %s, want %s", c.m, got, c.oct)
		}
	}
}

func TestSafeRel(t *testing.T) {
	cases := map[string]string{
		"a/b.txt": "a/b.txt", "./a": "a", "a//b": "a/b", "../x": "", "/etc/passwd": "", "a/../../x": "",
		`..\x`: "", "C:/x": "", "": "", ".": "", "a/..": "",
	}
	for in, want := range cases {
		got, ok := safeRel(in)
		if (want == "") == ok || got != want {
			t.Errorf("safeRel(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
}

func TestUniqueName(t *testing.T) {
	e := newEnv(t)
	root, _ := os.OpenRoot(e.dir)
	defer root.Close()
	if n := uniqueName(root, "docs", "a.txt"); n != "a (2).txt" {
		t.Error(n)
	}
	if n := uniqueName(root, "docs", "zzz"); n != "zzz" {
		t.Error(n)
	}
	if s, x := splitExt("x.tar.gz"); s != "x" || x != ".tar.gz" {
		t.Error(s, x)
	}
}

func TestSaveKeepsModeAfterChown(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := root.OpenFile("a.sh", os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := root.Chmod("a.sh", 0o775|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	st, err := root.Lstat("a.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(root, "a.sh", []byte("new"), st); err != nil {
		t.Fatal(err)
	}
	after, err := root.Lstat("a.sh")
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode() != st.Mode() {
		t.Fatalf("mode = %v, want %v", after.Mode(), st.Mode())
	}
}

type fakeInfo struct {
	fs.FileInfo
	mode fs.FileMode
	sys  *syscall.Stat_t
}

func (f fakeInfo) Mode() fs.FileMode { return f.mode }
func (f fakeInfo) Sys() any          { return f.sys }

func TestForeignSetgidFileNeedsElevation(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	foreign := fakeInfo{mode: 0o775 | fs.ModeSetgid, sys: &syscall.Stat_t{Uid: uint32(os.Getuid()) + 1}}
	done, err := foreignWrite(root, "x", []byte("y"), foreign, false)
	if !done || !isPerm(err) {
		t.Fatalf("done=%v err=%v, want a permission error", done, err)
	}
	own := fakeInfo{mode: 0o775 | fs.ModeSetgid, sys: &syscall.Stat_t{Uid: uint32(os.Getuid())}}
	if done, err := foreignWrite(root, "x", []byte("y"), own, false); done || err != nil {
		t.Fatalf("own file: done=%v err=%v", done, err)
	}
}

func TestWriteInPlaceThroughSymlinkedFolder(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	os.Mkdir(dir+"/real", 0o755)
	os.WriteFile(dir+"/real/f", []byte("old"), 0o644)
	os.Symlink("real", dir+"/link")
	if err := writeInPlaceNoFollow(root, "link/f", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dir + "/real/f"); string(b) != "new" {
		t.Fatalf("got %q", b)
	}
	os.Symlink("f", dir+"/real/l")
	if err := writeInPlaceNoFollow(root, "link/l", []byte("z")); err == nil {
		t.Fatal("final symlink followed")
	}
}
