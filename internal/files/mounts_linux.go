// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package files

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kushalpandya/wargo/internal/host"
)

// Paths read during detection (variables so tests can point them elsewhere).
var (
	mountinfoPath = defaultMountinfo
	byLabelDir    = "/dev/disk/by-label"
	sysClassBlock = "/sys/class/block"
)

// mountinfoFile is the mount table to read: our own natively, the host
// PID 1's in container mode (the container's mounts are irrelevant).
func mountinfoFile() string {
	if host.Container() && mountinfoPath == defaultMountinfo {
		return host.Path("/proc/1/mountinfo")
	}
	return mountinfoPath
}

// hostPathVar maps a detection path to the host unless a test replaced it.
func hostPathVar(p string) string {
	if p == "/dev/disk/by-label" || p == "/sys/class/block" {
		return host.Path(p)
	}
	return p
}

const defaultMountinfo = "/proc/self/mountinfo"

func detectDrives() []Drive {
	f, err := os.Open(mountinfoFile())
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
			if r, err := host.EvalSymlinks(dev); err == nil {
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

func diskLabels() map[string]string {
	out := map[string]string{}
	dir := hostPathVar(byLabelDir)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if t, err := filepath.EvalSymlinks(filepath.Join(dir, e.Name())); err == nil {
			// udev links are relative, so the result is below the host
			// root in container mode; report the host's device path.
			t, _ = host.Unpath(t)
			out[t] = unescapeLabel(e.Name())
		}
	}
	return out
}

// removable reports whether the block device (partition or disk) sits on a
// removable disk or a USB transport.
func removable(name string) bool {
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		return false
	}
	base := filepath.Join(hostPathVar(sysClassBlock), name)
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

// sealedRoot is the macOS sealed system volume check; Linux has none.
func sealedRoot(string) bool { return false }
