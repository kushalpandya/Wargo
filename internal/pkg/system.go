// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/host"
)

func kernelRelease() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return ""
	}
	b := make([]byte, 0, len(u.Release))
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

// kernelModulesMissing reports a pending reboot when the running kernel's
// modules directory is gone (the kernel package was upgraded).
func kernelModulesMissing() (bool, string) {
	return modulesMissing(kernelRelease(), []string{host.Path("/usr/lib/modules"), host.Path("/lib/modules")})
}

func modulesMissing(release string, dirs []string) (bool, string) {
	if release == "" {
		return false, ""
	}
	seen := false
	for _, d := range dirs {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		seen = true
		if _, err := os.Stat(filepath.Join(d, release)); err == nil {
			return false, ""
		}
	}
	if !seen {
		return false, ""
	}
	return true, fmt.Sprintf("The running kernel %s is no longer installed", release)
}

// LockStatus describes a held package-manager lock.
type LockStatus struct {
	Locked bool   `json:"locked"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// checkLocks inspects lock files without opening them (most are root-only).
// pacman/dnf/zypper locks are present-while-held files (dnf/zypper contain a
// PID); dpkg/apk locks always exist and are fcntl/flock locks, detected via
// /proc/locks by inode.
func checkLocks(paths []string) LockStatus {
	held := heldInodes()
	for _, p := range paths {
		st, err := host.Stat(p)
		if err != nil {
			continue
		}
		base := filepath.Base(p)
		switch {
		case strings.HasSuffix(base, ".pid"):
			if pidAlive(p) {
				return LockStatus{Locked: true, Path: p, Reason: "another package manager process is running"}
			}
		case base == "db.lck":
			return LockStatus{Locked: true, Path: p, Reason: "the pacman database is locked (another transaction is running, or a stale lock remains)"}
		default:
			if sys, ok := st.Sys().(*syscall.Stat_t); ok && held[sys.Ino] {
				return LockStatus{Locked: true, Path: p, Reason: "another package manager is running"}
			}
		}
	}
	return LockStatus{}
}

func pidAlive(path string) bool {
	b, err := host.ReadFile(path)
	if err != nil {
		// Unreadable pid file: assume held to be safe.
		return true
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false
	}
	_, err = os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

// heldInodes lists inodes with active locks from /proc/locks.
func heldInodes() map[uint64]bool {
	out := map[uint64]bool{}
	f, err := os.Open("/proc/locks")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// 1: POSIX  ADVISORY  WRITE 1234 08:02:131090 0 EOF
		fl := strings.Fields(sc.Text())
		for _, x := range fl {
			parts := strings.Split(x, ":")
			if len(parts) == 3 {
				if ino, err := strconv.ParseUint(parts[2], 10, 64); err == nil {
					out[ino] = true
				}
				break
			}
		}
	}
	return out
}

// DiskInfo is free space on a filesystem relevant to upgrades.
type DiskInfo struct {
	Path string `json:"path"`
	Free uint64 `json:"free"`
	Low  bool   `json:"low"`
}

const lowSpace = 2 << 30 // warn below 2 GiB
const minSpace = 256 << 20

func diskSpace() []DiskInfo {
	var out []DiskInfo
	seen := map[uint64]bool{}
	for _, p := range []string{"/", "/var/cache"} {
		var s syscall.Statfs_t
		if err := syscall.Statfs(host.Path(p), &s); err != nil {
			continue
		}
		var st syscall.Stat_t
		if syscall.Stat(host.Path(p), &st) == nil {
			if seen[st.Dev] {
				continue
			}
			seen[st.Dev] = true
		}
		free := s.Bavail * uint64(s.Bsize)
		out = append(out, DiskInfo{Path: p, Free: free, Low: free < lowSpace})
	}
	return out
}

// ConfigFile is a package-manager-created config file awaiting review.
type ConfigFile struct {
	Path    string    `json:"path"`
	ModTime time.Time `json:"modTime"`
	New     bool      `json:"new"`
}

// findConfigFiles walks /etc for the given suffixes, ignoring permission
// errors. Files modified at or after since are flagged new.
func findConfigFiles(root string, suffixes []string, since time.Time) []ConfigFile {
	var out []ConfigFile
	if len(suffixes) == 0 {
		return nil
	}
	// root is a host path; report host paths.
	if host.Container() {
		defer func() {
			for i := range out {
				out[i].Path, _ = host.Unpath(out[i].Path)
			}
		}()
		root = host.Path(root)
	}
	deadline := time.Now().Add(5 * time.Second)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if time.Now().After(deadline) || len(out) >= 200 {
			return fs.SkipAll
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		name := d.Name()
		for _, s := range suffixes {
			if strings.HasSuffix(name, s) {
				info, err := d.Info()
				if err != nil {
					return nil
				}
				out = append(out, ConfigFile{Path: p, ModTime: info.ModTime(), New: !since.IsZero() && !info.ModTime().Before(since)})
				break
			}
		}
		return nil
	})
	sort.Slice(out, func(a, b int) bool {
		if out[a].New != out[b].New {
			return out[a].New
		}
		return out[a].Path < out[b].Path
	})
	return out
}
