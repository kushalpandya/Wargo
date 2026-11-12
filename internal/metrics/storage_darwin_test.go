// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package metrics

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/events"
)

func TestStorageDarwin(t *testing.T) {
	st := NewCollector(events.NewHub(), time.Second, time.Minute).Storage(context.Background())
	if !slices.ContainsFunc(st.Filesystems, func(f Filesystem) bool { return f.Mount == "/" && f.Total > 0 }) {
		t.Fatal(st)
	}
	for _, f := range st.Filesystems {
		if strings.HasPrefix(f.Mount, "/System/Volumes/") {
			t.Fatal(f.Mount)
		}
	}
	if len(st.Drives) == 0 || st.Drives[0].Used == 0 {
		t.Fatalf("%+v", st.Drives)
	}
	// The root filesystem shows under the drive's APFS partition.
	if !slices.ContainsFunc(st.Drives[0].Partitions, func(p Partition) bool { return slices.Contains(p.Mounts, "/") }) {
		t.Fatalf("%+v", st.Drives[0])
	}
	for _, f := range st.Filesystems {
		if f.Mount == "/" && (f.ReadOnly || f.Used > f.Total || f.UsedPct <= 0) {
			t.Fatalf("%+v", f)
		}
	}
}
