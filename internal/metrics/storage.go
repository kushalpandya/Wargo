// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"context"
	"time"
)

type Filesystem struct {
	Device    string  `json:"device"`
	Mount     string  `json:"mount"`
	FSType    string  `json:"fstype"`
	Total     uint64  `json:"total"`
	Used      uint64  `json:"used"`
	Free      uint64  `json:"free"`
	UsedPct   float64 `json:"usedPct"`
	InodesPct float64 `json:"inodesPct"`
	ReadOnly  bool    `json:"readOnly"`
	Label     string  `json:"label,omitempty"`
	Partition string  `json:"partition,omitempty"` // kernel name, e.g. nvme0n1p5
}

type Partition struct {
	Name   string   `json:"name"`
	Size   uint64   `json:"size"`
	Label  string   `json:"label,omitempty"` // filesystem label, mounted or not
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
