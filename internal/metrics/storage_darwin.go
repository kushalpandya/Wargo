// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package metrics

import (
	"bytes"
	"context"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/host"
)

// filesystems lists the system volume and the volumes under /Volumes.
// Hidden system volumes (/System/Volumes/*, simulator images) are marked
// nobrowse and skipped.
func filesystems(ctx context.Context) []Filesystem {
	ms, err := host.Mounts()
	if err != nil {
		return nil
	}
	var out []Filesystem
	for _, m := range ms {
		if !host.UserVolume(m) || m.Total == 0 {
			continue
		}
		// Total minus free blocks: on APFS that is the whole container's
		// usage (all volumes), which is what fills the disk.
		used := m.Total - min(m.Free, m.Total)
		f := Filesystem{
			Device: m.Device, Mount: m.Path, FSType: m.FSType, Total: m.Total, Used: used, Free: m.Avail,
			// The sealed system snapshot is read-only; user data on / is
			// written through firmlinks into the Data volume.
			ReadOnly:  m.ReadOnly && m.Path != "/",
			Partition: strings.TrimPrefix(m.Device, "/dev/"),
		}
		if used+m.Avail > 0 {
			f.UsedPct = float64(used) / float64(used+m.Avail) * 100
		}
		if m.Files > 0 {
			f.InodesPct = float64(m.Files-min(m.FilesFree, m.Files)) / float64(m.Files) * 100
		}
		if m.Path != "/" {
			f.Label = path.Base(m.Path)
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b Filesystem) int { return strings.Compare(a.Mount, b.Mount) })
	return out
}

// diskutil output changes only when disks come and go, so it is cached
// for 5 minutes, or until the set of mounted devices changes.
var diskCache struct {
	mu     sync.Mutex
	at     time.Time
	key    string
	drives []Drive
	failed bool // the last read failed: retry after diskRetryTTL
}

const (
	diskCacheTTL = 5 * time.Minute
	diskRetryTTL = 15 * time.Second
	maxDisks     = 32
)

func drives(fs []Filesystem, temps []Temp) []Drive {
	devs := make([]string, 0, len(fs))
	for _, f := range fs {
		devs = append(devs, f.Device)
	}
	key := strings.Join(devs, "\x00")
	c := &diskCache
	c.mu.Lock()
	ttl := diskCacheTTL
	if c.failed {
		ttl = diskRetryTTL
	}
	if c.drives == nil || c.key != key || time.Since(c.at) > ttl {
		var ok bool
		c.drives, ok = readDiskutil()
		c.key, c.at, c.failed = key, time.Now(), !ok
	}
	base := c.drives
	c.mu.Unlock()

	byMount := map[string]Filesystem{}
	byPart := map[string]Filesystem{}
	for _, f := range fs {
		byMount[f.Mount] = f
		byPart[f.Partition] = f
	}
	nvmeTemp := 0.0
	for _, t := range temps {
		if t.Chip == "nvme" {
			nvmeTemp = t.Value
			break
		}
	}
	out := make([]Drive, 0, len(base))
	for _, b := range base {
		d := b
		if d.Transport == "nvme" {
			d.Temp = nvmeTemp
		}
		d.Partitions = make([]Partition, 0, len(b.Partitions))
		for _, p := range b.Partitions {
			// Keep the mounts the Filesystems list shows. The volumes of
			// one APFS container share its space, so the largest one
			// stands for the partition instead of summing them.
			var mounts []string
			var best Filesystem
			for _, m := range p.Mounts {
				if f, ok := byMount[m]; ok {
					mounts = append(mounts, m)
					if f.Total > best.Total {
						best = f
					}
				}
			}
			p.Mounts = mounts
			d.Used += best.Used
			d.Total += best.Total
			d.Partitions = append(d.Partitions, p)
		}
		if f, ok := byPart[d.Name]; ok { // filesystem directly on the disk
			d.Used += f.Used
			d.Total += f.Total
		}
		out = append(out, d)
	}
	return out
}

// readDiskutil runs diskutil for the physical disks. On error it returns an
// empty list (never null: the System page counts it) and ok false.
func readDiskutil() (ds []Drive, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	physical, err := diskutilPlist(ctx, "list", "-plist", "physical")
	if err != nil {
		return []Drive{}, false
	}
	list, _ := diskutilPlist(ctx, "list", "-plist")
	info := map[string]any{}
	for i, w := range plArray(plDict(physical)["WholeDisks"]) {
		name := plString(w)
		if i >= maxDisks || !physicalDisk(name) {
			continue
		}
		if v, err := diskutilPlist(ctx, "info", "-plist", name); err == nil {
			info[name] = v
		}
	}
	return parseDiskutil(list, physical, info), true
}

func diskutilPlist(ctx context.Context, args ...string) (any, error) {
	out, err := host.Command(ctx, append([]string{"/usr/sbin/diskutil"}, args...)...).Output()
	if err != nil {
		return nil, err
	}
	return decodePlist(bytes.NewReader(out))
}
