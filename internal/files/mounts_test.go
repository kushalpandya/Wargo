// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/jobs"
)

const sampleMountinfo = `22 1 259:5 / / rw,noatime shared:1 - ext4 /dev/nvme0n1p5 rw
23 22 0:21 / /proc rw,nosuid,nodev,noexec,relatime shared:5 - proc proc rw
24 22 0:22 / /sys rw,nosuid,nodev,noexec,relatime shared:6 - sysfs sysfs rw
25 22 0:5 / /dev rw,nosuid shared:2 - devtmpfs devtmpfs rw,size=8000k
26 22 0:24 / /run rw,nosuid,nodev shared:12 - tmpfs tmpfs rw,mode=755
27 24 0:25 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw
28 24 0:26 / /sys/firmware/efi/efivars rw - efivarfs efivarfs rw
30 22 259:1 / /boot/efi rw,relatime shared:20 - vfat /dev/nvme0n1p1 rw,fmask=0022
31 22 8:2 / /mnt/Store rw,relatime shared:21 - fuseblk /dev/sda2 rw,user_id=0
32 22 8:2 /Media /srv/media rw,relatime shared:21 - fuseblk /dev/sda2 rw,user_id=0
33 22 8:17 / /run/media/kushal/USB\040Stick ro,nosuid shared:22 - exfat /dev/sdb1 ro
34 26 0:58 / /run/user/1000/doc rw - fuse.portal portal rw
35 22 0:60 / /var/lib/docker/overlay2/abc/merged rw - overlay overlay rw,lowerdir=x
36 22 7:0 / /snap/core/123 ro - squashfs /dev/loop0 ro
37 22 0:30 / /home rw shared:30 - btrfs /dev/sdc1 rw,subvol=/@home
38 22 0:30 /@data /data rw shared:31 - btrfs /dev/sdc1 rw,subvol=/@data
39 22 0:40 / /mnt/nas rw - nfs4 nas:/export rw,addr=10.0.0.2
40 22 0:41 / /mnt/share rw - cifs //nas/share rw
41 22 0:42 / /mnt/tank zfs rw - zfs tank/data rw
42 22 259:5 /home/kushal/x /home/kushal/bound rw - ext4 /dev/nvme0n1p5 rw
43 22 0:43 / /tmp rw - tmpfs tmpfs rw
`

func TestParseMountinfo(t *testing.T) {
	ms := parseMountinfo(strings.NewReader(sampleMountinfo + "garbage line\n"))
	if len(ms) != 21 {
		t.Fatalf("parsed %d mounts", len(ms))
	}
	usb := ms[10]
	if usb.Path != "/run/media/kushal/USB Stick" || usb.FSType != "exfat" || usb.Source != "/dev/sdb1" || !slices.Contains(usb.Opts, "ro") {
		t.Fatalf("usb mount: %+v", usb)
	}
	if ms[9].FSRoot != "/Media" || ms[9].Dev != "8:2" {
		t.Fatalf("bind mount: %+v", ms[9])
	}
	if unescapeMount(`a\040b\134c\`) != `a b\c\` {
		t.Fatal(unescapeMount(`a\040b\134c\`))
	}
	if unescapeLabel(`My\x20Disk`) != "My Disk" {
		t.Fatal("label unescape")
	}
}

func TestFilterMounts(t *testing.T) {
	got := []string{}
	for _, m := range filterMounts(parseMountinfo(strings.NewReader(sampleMountinfo))) {
		got = append(got, m.Path)
	}
	want := []string{"/", "/boot/efi", "/data", "/home", "/mnt/Store", "/mnt/nas", "/mnt/share", "/mnt/tank", "/run/media/kushal/USB Stick"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// A later mount on the same point hides the earlier one.
	ms := filterMounts(parseMountinfo(strings.NewReader("1 0 8:1 / /mnt/x rw - ext4 /dev/sda1 rw\n2 0 8:2 / /mnt/x rw - ext4 /dev/sdb1 rw\n")))
	if len(ms) != 1 || ms[0].Source != "/dev/sdb1" {
		t.Fatalf("shadowed: %+v", ms)
	}
}

func TestMountIDs(t *testing.T) {
	for _, p := range []string{"/", "/mnt/Store", "/run/media/kushal/USB Stick", "/mnt/ü/日本"} {
		id := mountID(p)
		if strings.ContainsAny(id, "/+=?#%: ") {
			t.Fatalf("id %q not URL-safe", id)
		}
		back, ok := parseMountID(id)
		if !ok || back != p {
			t.Fatalf("round trip %q → %q → %q", p, id, back)
		}
	}
	for _, bad := range []string{"0", "12", "m-", "m-***", "x-Lw", mountID("relative"), mountID("/a/../b"), mountID("/a/")} {
		if _, ok := parseMountID(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestDriveRoots(t *testing.T) {
	e := newEnv(t)
	drive := filepath.Join(t.TempDir(), "My Drive")
	must(t, os.MkdirAll(filepath.Join(drive, "music"), 0o755))
	must(t, os.WriteFile(filepath.Join(drive, "music/a.mp3"), []byte("x"), 0o644))
	withMounts(t, drive)

	// Disabled: mount ids are rejected and not listed.
	id := mountID(drive)
	expectErr(t, e.do("GET", "/api/files/list?root="+id, nil), 404, "unknown_root")

	e.svc.showMounts = true
	roots := decodeJSON[[]struct {
		ID, Kind, Name, Path, FSType string
		Total                        uint64
	}](t, e.do("GET", "/api/files/roots", nil))
	if len(roots) != 2 || roots[0].ID != "0" || roots[0].Kind != "place" || roots[1].ID != id || roots[1].Kind != "drive" || roots[1].Name != "My Drive" || roots[1].FSType != "ext4" || roots[1].Total == 0 {
		t.Fatalf("roots: %+v", roots)
	}
	w := e.do("GET", "/api/files/list?root="+id+"&path=music", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "a.mp3") {
		t.Fatal(w.Body.String())
	}
	// Confinement still applies inside a drive.
	if w := e.do("GET", "/api/files/list?root="+id+"&path=../..", nil); w.Code != 200 || decodeJSON[struct{ Path string }](t, w).Path != "." {
		t.Fatal("escaped the drive root:", w.Body.String())
	}
	// A well-formed id for a directory that isn't a mount is refused.
	expectErr(t, e.do("GET", "/api/files/list?root="+mountID(e.dir), nil), 404, "unknown_root")
	expectErr(t, e.do("GET", "/api/files/list?root="+mountID("/etc"), nil), 404, "unknown_root")
	// Numeric ids keep working.
	if w := e.do("GET", "/api/files/list?root=0", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

func TestCrossRootTransfer(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	must(t, os.MkdirAll(filepath.Join(a, "dir/sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(a, "dir/sub/f.txt"), []byte("hi"), 0o644))
	must(t, os.WriteFile(filepath.Join(a, "one.txt"), []byte("1"), 0o644))
	must(t, os.MkdirAll(b, 0o755))
	svc := NewWithOptions(Options{
		Roots:   []config.Root{{Name: "A", Path: a}, {Name: "B", Path: b}, {Name: "Sub", Path: filepath.Join(a, "dir/sub")}},
		DataDir: filepath.Join(base, "data"), Jobs: jobs.NewManager(events.NewHub()),
	})
	mux := http.NewServeMux()
	svc.Register(mux)
	e := &env{t: t, dir: a, svc: svc, mux: mux}
	ok := func(w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}

	ok(e.do("POST", "/api/files/copy", map[string]any{"root": "0", "destRoot": "1", "paths": []string{"dir", "one.txt"}, "dest": "."}))
	if s, _ := os.ReadFile(filepath.Join(b, "dir/sub/f.txt")); string(s) != "hi" {
		t.Fatal("cross copy missing")
	}
	expectErr(t, e.do("POST", "/api/files/copy", map[string]any{"root": "0", "destRoot": "1", "paths": []string{"one.txt"}, "dest": "."}), 409, "exists")
	ok(e.do("POST", "/api/files/move", map[string]any{"root": "0", "destRoot": "1", "paths": []string{"one.txt"}, "dest": "dir", "onConflict": "rename"}))
	if _, err := os.Stat(filepath.Join(a, "one.txt")); err == nil {
		t.Fatal("source still there after move")
	}
	if _, err := os.Stat(filepath.Join(b, "dir/one.txt")); err != nil {
		t.Fatal("moved file missing")
	}
	// Overlapping roots: copying a folder into a root that lives inside it.
	expectErr(t, e.do("POST", "/api/files/copy", map[string]any{"root": "0", "destRoot": "2", "paths": []string{"dir"}, "dest": "."}), 400, "into_self")
	expectErr(t, e.do("POST", "/api/files/move", map[string]any{"root": "0", "destRoot": "2", "paths": []string{"dir/sub"}, "dest": "."}), 400, "into_self")
	// Copying into the same real folder through another root duplicates.
	ok(e.do("POST", "/api/files/copy", map[string]any{"root": "2", "destRoot": "0", "paths": []string{"f.txt"}, "dest": "dir/sub"}))
	if _, err := os.Stat(filepath.Join(a, "dir/sub/f (2).txt")); err != nil {
		t.Fatal("expected duplicate through overlapping root")
	}

	// Large cross-root copies run as a job.
	old := inlineCopyBytes
	inlineCopyBytes = 1
	defer func() { inlineCopyBytes = old }()
	must(t, os.MkdirAll(filepath.Join(b, "big"), 0o755))
	w := e.do("POST", "/api/files/copy", map[string]any{"root": "0", "destRoot": "1", "paths": []string{"dir"}, "dest": "big"})
	res := decodeJSON[struct{ JobID string }](t, w)
	if res.JobID == "" {
		t.Fatal("expected job", w.Body.String())
	}
	s := waitJob(t, svc.jobs, res.JobID)
	if s.State != jobs.Succeeded || s.Result["destRoot"] != "1" || s.Result["root"] != "0" {
		t.Fatalf("job: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(b, "big/dir/sub/f.txt")); err != nil {
		t.Fatal("job copy missing")
	}
	expectErr(t, e.do("POST", "/api/files/copy", map[string]any{"root": "0", "destRoot": "9", "paths": []string{"dir"}, "dest": "."}), 404, "unknown_root")
}
