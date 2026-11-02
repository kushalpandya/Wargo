// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"bufio"
	"encoding/base64"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Mounted filesystems ("drives") are offered as roots next to the configured
// places. They are detected from /proc/self/mountinfo on demand, so drives
// that are plugged in or unmounted show up on the next roots request.

// mount is one line of /proc/self/mountinfo.
type mount struct {
	Dev    string // major:minor
	FSRoot string // path of the mount's root within its filesystem
	Path   string // mount point
	Opts   []string
	FSType string
	Source string
	Super  []string
}

// parseMountinfo parses the mountinfo format (proc(5)):
//
//	36 35 98:0 /mnt1 /mnt/parent rw,noatime master:1 - ext3 /dev/root rw,errors=continue
func parseMountinfo(r io.Reader) []mount {
	var out []mount
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		sep := slices.Index(f, "-")
		if sep < 6 || len(f) < sep+3 {
			continue
		}
		m := mount{
			Dev: f[2], FSRoot: unescapeMount(f[3]), Path: unescapeMount(f[4]),
			Opts: strings.Split(f[5], ","), FSType: f[sep+1], Source: unescapeMount(f[sep+2]),
		}
		if len(f) > sep+3 {
			m.Super = strings.Split(f[sep+3], ",")
		}
		out = append(out, m)
	}
	return out
}

// unescapeMount decodes the octal escapes (\040 etc.) used in mountinfo.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Filesystems that are never user data.
var pseudoFS = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "efivarfs": true, "overlay": true, "aufs": true, "squashfs": true,
	"proc": true, "sysfs": true, "cgroup": true, "cgroup2": true, "autofs": true, "devpts": true,
	"mqueue": true, "debugfs": true, "tracefs": true, "securityfs": true, "pstore": true, "bpf": true,
	"configfs": true, "fusectl": true, "hugetlbfs": true, "binfmt_misc": true, "nsfs": true,
	"ramfs": true, "rpc_pipefs": true, "nfsd": true, "zram": true, "selinuxfs": true, "erofs": true,
	"fuse.portal": true, "fuse.gvfsd-fuse": true, "fuse.lxcfs": true, "fuse.snapfuse": true,
	"fuse.xdg-document-portal": true, "fuse.doc": true,
}

// Network filesystems are included even though their source isn't a /dev node.
var networkFS = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true, "fuse.sshfs": true,
	"fuse.rclone": true, "9p": true, "virtiofs": true, "glusterfs": true, "ceph": true, "fuse.s3fs": true,
}

// Local filesystems whose source may not be a /dev path (zfs datasets, …).
var localFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "btrfs": true, "xfs": true, "zfs": true, "f2fs": true,
	"jfs": true, "reiserfs": true, "vfat": true, "msdos": true, "exfat": true, "ntfs": true,
	"ntfs3": true, "fuseblk": true, "hfs": true, "hfsplus": true, "iso9660": true, "udf": true,
	"bcachefs": true, "nilfs2": true, "fuse.mergerfs": true,
}

func skipMountPath(p string) bool {
	for _, pre := range []string{"/proc", "/sys", "/dev", "/var/lib/docker", "/var/lib/containers", "/var/lib/kubelet", "/snap", "/var/snap", "/var/lib/snapd"} {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	if (p == "/run" || strings.HasPrefix(p, "/run/")) && !strings.HasPrefix(p, "/run/media/") {
		return true
	}
	return false
}

func isNetwork(fstype string) bool { return networkFS[fstype] }

// filterMounts keeps real, user-facing filesystems and drops bind mounts of
// something that is already reachable through another listed mount.
func filterMounts(ms []mount) []mount {
	var cand []mount
	for _, m := range ms {
		if pseudoFS[m.FSType] || skipMountPath(m.Path) || !strings.HasPrefix(m.Path, "/") {
			continue
		}
		if !localFS[m.FSType] && !networkFS[m.FSType] && !strings.HasPrefix(m.Source, "/dev/") {
			continue
		}
		cand = append(cand, m)
	}
	// Later mounts on the same mount point hide earlier ones.
	last := map[string]int{}
	for i, m := range cand {
		last[m.Path] = i
	}
	var vis []mount
	for i, m := range cand {
		if last[m.Path] == i {
			vis = append(vis, m)
		}
	}
	// Shortest mount path first, so the canonical mount of a device wins.
	slices.SortStableFunc(vis, func(a, b mount) int {
		if d := len(a.Path) - len(b.Path); d != 0 {
			return d
		}
		return strings.Compare(a.Path, b.Path)
	})
	var out []mount
	for _, m := range vis {
		dup := false
		for _, k := range out {
			// Same device and the kept mount covers this one's subtree: a bind
			// mount (btrfs subvolumes like /@ and /@home don't nest, so both stay).
			if k.Dev == m.Dev && (k.FSRoot == m.FSRoot || inside(m.FSRoot, k.FSRoot)) && !isNetwork(m.FSType) {
				dup = true
				break
			}
			if isNetwork(m.FSType) && k.Source == m.Source && k.FSRoot == m.FSRoot {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b mount) int {
		if a.Path == "/" || b.Path == "/" {
			return boolCmp(b.Path == "/", a.Path == "/")
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out
}

func boolCmp(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// inside reports whether p equals dir or lies below it (absolute paths).
func inside(p, dir string) bool {
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// ---- ids ---------------------------------------------------------------------

const mountPrefix = "m-"

// mountID returns a stable, URL-safe id for a mount point.
func mountID(p string) string {
	return mountPrefix + base64.RawURLEncoding.EncodeToString([]byte(p))
}

// parseMountID decodes an id from mountID; ok is false for anything else.
func parseMountID(id string) (string, bool) {
	rest, ok := strings.CutPrefix(id, mountPrefix)
	if !ok || rest == "" {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return "", false
	}
	p := string(b)
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return "", false
	}
	return p, true
}

// ---- detection ---------------------------------------------------------------

// Drive is a mounted filesystem offered as a root.
type Drive struct {
	ID        string
	Label     string
	Path      string
	Device    string
	FSType    string
	ReadOnly  bool
	Removable bool
	Network   bool
}

// Paths read during detection (variables so tests can point them elsewhere).
var (
	mountinfoPath = "/proc/self/mountinfo"
	byLabelDir    = "/dev/disk/by-label"
	sysClassBlock = "/sys/class/block"
)

type mountCache struct {
	mu     sync.Mutex
	at     time.Time
	drives []Drive
}

const mountCacheTTL = 2 * time.Second

// drives returns the current mounts; fresh forces re-detection.
func (s *Service) drives(fresh bool) []Drive {
	if !s.showMounts {
		return nil
	}
	c := &s.mounts
	c.mu.Lock()
	defer c.mu.Unlock()
	if fresh || time.Since(c.at) > mountCacheTTL {
		c.drives = detectDrives()
		c.at = time.Now()
	}
	return c.drives
}

func detectDrives() []Drive {
	f, err := os.Open(mountinfoPath)
	if err != nil {
		return nil
	}
	ms := filterMounts(parseMountinfo(f))
	f.Close()
	labels := diskLabels()
	out := make([]Drive, 0, len(ms))
	for _, m := range ms {
		dev := m.Source
		if strings.HasPrefix(dev, "/dev/") {
			if r, err := filepath.EvalSymlinks(dev); err == nil {
				dev = r
			}
		}
		d := Drive{
			ID: mountID(m.Path), Path: m.Path, Device: dev, FSType: m.FSType,
			ReadOnly: slices.Contains(m.Opts, "ro") || slices.Contains(m.Super, "ro"),
			Network:  isNetwork(m.FSType),
		}
		d.Label = labels[dev]
		if d.Label == "" {
			d.Label = driveName(m.Path)
		}
		if strings.HasPrefix(dev, "/dev/") {
			d.Removable = removable(strings.TrimPrefix(dev, "/dev/"))
		}
		out = append(out, d)
	}
	return out
}

func driveName(p string) string {
	if p == "/" {
		return "System"
	}
	return path.Base(p)
}

func diskLabels() map[string]string {
	out := map[string]string{}
	ents, _ := os.ReadDir(byLabelDir)
	for _, e := range ents {
		if t, err := filepath.EvalSymlinks(filepath.Join(byLabelDir, e.Name())); err == nil {
			out[t] = unescapeLabel(e.Name())
		}
	}
	return out
}

// unescapeLabel decodes udev's \xNN escapes in /dev/disk/by-label names.
func unescapeLabel(s string) string {
	if !strings.Contains(s, `\x`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// removable reports whether the block device (partition or disk) sits on a
// removable disk or a USB transport.
func removable(name string) bool {
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		return false
	}
	base := filepath.Join(sysClassBlock, name)
	real, err := filepath.EvalSymlinks(base)
	if err != nil {
		return false
	}
	if strings.Contains(real, "/usb") {
		return true
	}
	disk := real
	if _, err := os.Stat(filepath.Join(real, "partition")); err == nil {
		disk = filepath.Dir(real)
	}
	b, err := os.ReadFile(filepath.Join(disk, "removable"))
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// statfs is syscall.Statfs giving up after a second (stale network mounts
// can block it indefinitely).
func statfs(p string) (syscall.Statfs_t, error) {
	type res struct {
		st  syscall.Statfs_t
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var st syscall.Statfs_t
		err := syscall.Statfs(p, &st)
		ch <- res{st, err}
	}()
	select {
	case r := <-ch:
		return r.st, r.err
	case <-time.After(time.Second):
		return syscall.Statfs_t{}, os.ErrDeadlineExceeded
	}
}
