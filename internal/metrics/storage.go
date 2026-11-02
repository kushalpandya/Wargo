// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

type Filesystem struct {
	Device     string  `json:"device"`
	Mount      string  `json:"mount"`
	FSType     string  `json:"fstype"`
	Total      uint64  `json:"total"`
	Used       uint64  `json:"used"`
	Free       uint64  `json:"free"`
	UsedPct    float64 `json:"usedPct"`
	InodesPct  float64 `json:"inodesPct"`
	ReadOnly   bool    `json:"readOnly"`
	Label      string  `json:"label,omitempty"`
	Partition  string  `json:"partition,omitempty"` // kernel name, e.g. nvme0n1p5
}

type Partition struct {
	Name   string `json:"name"`
	Size   uint64 `json:"size"`
	Mounts []string `json:"mounts,omitempty"`
}

type Drive struct {
	Name       string      `json:"name"`
	Model      string      `json:"model"`
	Serial     string      `json:"serial,omitempty"`
	Size       uint64      `json:"size"`
	Rotational bool        `json:"rotational"`
	Removable  bool        `json:"removable"`
	Transport  string      `json:"transport"` // nvme | sata | usb | virtio | mmc
	Temp       float64     `json:"temp,omitempty"`
	Partitions []Partition `json:"partitions"`
	Used       uint64      `json:"used"`  // sum of mounted filesystems on this drive
	Total      uint64      `json:"total"` // sum of mounted filesystem sizes
}

type Storage struct {
	Drives      []Drive      `json:"drives"`
	Filesystems []Filesystem `json:"filesystems"`
}

var skipFS = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "efivarfs": true, "overlay": true, "squashfs": true, "proc": true,
	"sysfs": true, "cgroup": true, "cgroup2": true, "autofs": true, "devpts": true, "mqueue": true,
	"debugfs": true, "tracefs": true, "securityfs": true, "pstore": true, "bpf": true, "configfs": true,
	"fusectl": true, "hugetlbfs": true, "binfmt_misc": true, "nsfs": true, "fuse.portal": true,
	"fuse.gvfsd-fuse": true, "ramfs": true, "rpc_pipefs": true, "nfsd": true, "zram": true,
}

func labels() map[string]string {
	out := map[string]string{}
	ents, _ := os.ReadDir("/dev/disk/by-label")
	for _, e := range ents {
		if t, err := filepath.EvalSymlinks(filepath.Join("/dev/disk/by-label", e.Name())); err == nil {
			out[t] = strings.ReplaceAll(e.Name(), `\x20`, " ")
		}
	}
	return out
}

func filesystems(ctx context.Context) []Filesystem {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil
	}
	lbl := labels()
	seen := map[string]bool{}
	var out []Filesystem
	for _, p := range parts {
		if skipFS[p.Fstype] || strings.HasPrefix(p.Mountpoint, "/var/lib/docker") ||
			strings.HasPrefix(p.Mountpoint, "/snap/") || strings.HasPrefix(p.Mountpoint, "/run/") && !strings.HasPrefix(p.Mountpoint, "/run/media") {
			continue
		}
		dev := p.Device
		if r, err := filepath.EvalSymlinks(dev); err == nil {
			dev = r
		}
		// Bind mounts repeat the same device; keep the shortest mount path.
		if seen[dev] && strings.HasPrefix(dev, "/dev/") {
			continue
		}
		u, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		seen[dev] = true
		out = append(out, Filesystem{
			Device: dev, Mount: p.Mountpoint, FSType: p.Fstype, Total: u.Total, Used: u.Used, Free: u.Free,
			UsedPct: u.UsedPercent, InodesPct: u.InodesUsedPercent, ReadOnly: slices.Contains(p.Opts, "ro"),
			Label: lbl[dev], Partition: strings.TrimPrefix(dev, "/dev/"),
		})
	}
	slices.SortFunc(out, func(a, b Filesystem) int { return strings.Compare(a.Mount, b.Mount) })
	return out
}

func drives(fs []Filesystem, temps []Temp) []Drive {
	ents, _ := os.ReadDir("/sys/block")
	mounts := map[string][]string{}
	usage := map[string]Filesystem{}
	for _, f := range fs {
		mounts[f.Partition] = append(mounts[f.Partition], f.Mount)
		usage[f.Partition] = f
	}
	var out []Drive
	nvmeTemp := 0.0
	for _, t := range temps {
		if t.Chip == "nvme" {
			nvmeTemp = t.Value
			break
		}
	}
	for _, e := range ents {
		name := e.Name()
		if !physicalDisk(name) {
			continue
		}
		base := filepath.Join("/sys/block", name)
		sz, _ := readInt(filepath.Join(base, "size"))
		if sz == 0 {
			continue
		}
		d := Drive{Name: name, Size: uint64(sz) * 512}
		d.Model = strings.TrimSpace(readStr(filepath.Join(base, "device", "model")))
		if d.Model == "" {
			d.Model = strings.TrimSpace(readStr(filepath.Join(base, "device", "name")))
		}
		d.Serial = readStr(filepath.Join(base, "device", "serial"))
		rot, _ := readInt(filepath.Join(base, "queue", "rotational"))
		d.Rotational = rot == 1
		rem, _ := readInt(filepath.Join(base, "removable"))
		d.Removable = rem == 1
		d.Transport = transport(name, base)
		if d.Transport == "nvme" {
			d.Temp = nvmeTemp
		}
		subs, _ := os.ReadDir(base)
		for _, s := range subs {
			if !strings.HasPrefix(s.Name(), name) {
				continue
			}
			psz, _ := readInt(filepath.Join(base, s.Name(), "size"))
			d.Partitions = append(d.Partitions, Partition{Name: s.Name(), Size: uint64(psz) * 512, Mounts: mounts[s.Name()]})
			if f, ok := usage[s.Name()]; ok {
				d.Used += f.Used
				d.Total += f.Total
			}
		}
		if f, ok := usage[name]; ok { // filesystem directly on the disk
			d.Used += f.Used
			d.Total += f.Total
		}
		slices.SortFunc(d.Partitions, func(a, b Partition) int {
			if natLess(a.Name, b.Name) {
				return -1
			}
			return 1
		})
		out = append(out, d)
	}
	return out
}

func transport(name, base string) string {
	switch {
	case strings.HasPrefix(name, "nvme"):
		return "nvme"
	case strings.HasPrefix(name, "mmcblk"):
		return "mmc"
	case strings.HasPrefix(name, "vd"):
		return "virtio"
	}
	if p, err := filepath.EvalSymlinks(base); err == nil && strings.Contains(p, "/usb") {
		return "usb"
	}
	return "sata"
}

// Storage returns drives and mounted filesystems, cached for 15 s.
func (c *Collector) Storage(ctx context.Context) Storage {
	c.mu.RLock()
	fresh := time.Since(c.fsAt) < 15*time.Second && c.fs != nil
	fs := c.fs
	var temps []Temp
	if c.latest != nil {
		temps = c.latest.Temps
	}
	c.mu.RUnlock()
	if !fresh {
		fs = filesystems(ctx)
		c.mu.Lock()
		c.fs, c.fsAt = fs, time.Now()
		c.mu.Unlock()
	}
	return Storage{Drives: drives(fs, temps), Filesystems: fs}
}
