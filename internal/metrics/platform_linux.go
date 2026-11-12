// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/shirou/gopsutil/v4/load"

	hostfs "github.com/kushalpandya/wargo/internal/host"
)

func physicalDisk(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "sr", "fd", "dm-", "md"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	_, err := os.Stat(hostfs.Path(filepath.Join("/sys/block", name)))
	return err == nil
}

var ifSkipPrefixes = []string{"veth", "br-", "docker", "virbr", "vnet", "cni", "flannel", "kube"}

func procCount(ctx context.Context) uint64 {
	if m, err := load.MiscWithContext(ctx); err == nil {
		return uint64(m.ProcsTotal)
	}
	return 0
}
