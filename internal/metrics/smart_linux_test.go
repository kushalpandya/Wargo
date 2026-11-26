// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package metrics

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	hostfs "github.com/kushalpandya/wargo/internal/host"
)

// stubSmartctl replaces smartctl with fn and empties the cache.
func stubSmartctl(t *testing.T, fn func(dev string) ([]byte, error)) {
	t.Helper()
	oldRun, oldPath, oldWait := runSmartctl, smartctlPath, smartWait
	runSmartctl = func(_ context.Context, _, dev string) ([]byte, error) { return fn(dev) }
	smartctlPath = func() (string, error) { return "smartctl", nil }
	smartCache.mu.Lock()
	smartCache.m = map[string]*smartEntry{}
	smartCache.mu.Unlock()
	t.Cleanup(func() { runSmartctl, smartctlPath, smartWait = oldRun, oldPath, oldWait })
}

// counting returns a smartctl stub that answers with js and counts runs.
func counting(t *testing.T, js []byte) (func(string) ([]byte, error), *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	return func(string) ([]byte, error) { n.Add(1); return js, nil }, &n
}

func TestReadSmartNonZeroExitStillParsed(t *testing.T) {
	js := readFixture(t, "smartctl-nvme.json")
	stubSmartctl(t, func(string) ([]byte, error) { return js, &exec.ExitError{} })
	if h := readSmart("/dev/nvme0n1", nil); h.Status != HealthOK {
		t.Fatalf("health = %+v, want ok", h)
	}
}

func TestReadSmartStandbyKeepsLast(t *testing.T) {
	stubSmartctl(t, func(string) ([]byte, error) {
		return []byte(`{"smartctl":{"exit_status":2,"messages":[{"string":"Device is in STANDBY mode, exit(2)","severity":"information"}]}}`), nil
	})
	prev := &DriveHealth{Status: HealthWarn, Notes: []string{"8 reallocated sectors"}}
	if h := readSmart("/dev/sdb", prev); h != prev {
		t.Fatalf("health = %+v, want the previous result", h)
	}
	if h := readSmart("/dev/sdb", nil); h.Status != HealthUnknown {
		t.Fatalf("health = %+v, want unknown while asleep with no history", h)
	}
}

func TestReadSmartNotInstalled(t *testing.T) {
	stubSmartctl(t, nil)
	smartctlPath = func() (string, error) { return "", exec.ErrNotFound }
	h := readSmart("/dev/sda", nil)
	if h.Status != HealthUnknown || h.Reason != "Install smartmontools to see drive health" {
		t.Fatalf("health = %+v", h)
	}
}

func TestSmartHealthCachesAndRefreshesWhenStale(t *testing.T) {
	fn, calls := counting(t, readFixture(t, "smartctl-nvme.json"))
	stubSmartctl(t, fn)
	ctx := context.Background()
	d := Drive{Name: "nvme0n1", Serial: "S1"}
	smartHealth(ctx, d)
	smartHealth(ctx, d)
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (cached)", calls.Load())
	}
	// Make the entry stale and wait for the refresh it starts.
	smartCache.mu.Lock()
	e := smartCache.m[smartKey(d)]
	e.at = time.Now().Add(-smartTTL - time.Second)
	smartCache.mu.Unlock()
	if h := smartHealth(ctx, d); h == nil || h.Status != HealthOK {
		t.Fatalf("stale read = %+v, want the cached ok", h)
	}
	waitIdle(t, e)
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 after a refresh", calls.Load())
	}
}

// waitIdle waits until e has no read in flight.
func waitIdle(t *testing.T, e *smartEntry) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		smartCache.mu.Lock()
		p := e.pending
		smartCache.mu.Unlock()
		if !p {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("read still in flight")
		}
		runtime.Gosched()
	}
}

func TestSmartHealthConcurrentFirstReadsShareOneRun(t *testing.T) {
	release := make(chan struct{})
	js := readFixture(t, "smartctl-nvme.json")
	var calls atomic.Int32
	stubSmartctl(t, func(string) ([]byte, error) { calls.Add(1); <-release; return js, nil })
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { smartHealth(context.Background(), Drive{Name: "nvme0n1"}) })
	}
	time.AfterFunc(50*time.Millisecond, func() { close(release) })
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 shared run", calls.Load())
	}
}

func TestSmartHealthHungDriveDoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	js := readFixture(t, "smartctl-nvme.json")
	stubSmartctl(t, func(string) ([]byte, error) { <-release; return js, nil })
	smartWait = 20 * time.Millisecond
	// Let the background read finish before the stubs are restored.
	defer func() {
		close(release)
		smartCache.mu.Lock()
		e := smartCache.m[smartKey(Drive{Name: "sdb"})]
		smartCache.mu.Unlock()
		waitIdle(t, e)
	}()
	if h := smartHealth(context.Background(), Drive{Name: "sdb"}); h == nil || h.Reason != "Still reading drive health" {
		t.Fatalf("health = %+v, want still reading", h)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if h := smartHealth(ctx, Drive{Name: "sdb"}); h != nil {
		t.Fatalf("health = %+v, want nil for an ended request", h)
	}
}

func TestSmartHealthNewDiskReadsAgain(t *testing.T) {
	fn, calls := counting(t, readFixture(t, "smartctl-nvme.json"))
	stubSmartctl(t, fn)
	// SATA and USB disks have no serial in sysfs: model and size tell them apart.
	smartHealth(context.Background(), Drive{Name: "sdb", Model: "Old disk", Size: 1 << 40})
	smartHealth(context.Background(), Drive{Name: "sdb", Model: "New disk", Size: 2 << 40})
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want a fresh read for a new disk under the same name", calls.Load())
	}
}

func TestDiscardDevicesFollowsDeviceMapper(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"sys/class/block/dm-0/slaves/nvme0n1p3", "dev"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defer hostfs.SetRoot(root)()
	mi := filepath.Join(root, "mountinfo")
	if err := os.WriteFile(mi, []byte("26 1 254:0 / / rw,noatime,discard shared:1 - ext4 /dev/dm-0 rw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOST_PROC_MOUNTINFO", mi)
	if got := discardDevices(); !got["nvme0n1p3"] {
		t.Fatalf("discardDevices = %v, want the LUKS partition marked", got)
	}
}

func TestDiscardDevices(t *testing.T) {
	dir := t.TempDir()
	mi := filepath.Join(dir, "mountinfo")
	lines := "" +
		"26 1 259:2 / / rw,noatime shared:1 - btrfs /dev/nvme0n1p2 rw,ssd,discard=async,space_cache=v2\n" +
		"27 26 259:1 / /boot rw,relatime shared:2 - vfat /dev/nvme0n1p1 rw,fmask=0077\n" +
		"28 26 8:1 / /data rw,discard shared:3 - ext4 /dev/sda1 rw\n" +
		"29 26 0:5 / /dev rw - devtmpfs devtmpfs rw\n"
	if err := os.WriteFile(mi, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOST_PROC_MOUNTINFO", mi)
	got := discardDevices()
	if !got["nvme0n1p2"] || !got["sda1"] || got["nvme0n1p1"] || len(got) != 2 {
		t.Fatalf("discardDevices = %v", got)
	}
}

func TestAddHealthTrimAndSkipsVirtual(t *testing.T) {
	root := t.TempDir()
	for name, max := range map[string]string{"nvme0n1": "2199023255040", "sda": "0"} {
		q := filepath.Join(root, "sys/block", name, "queue")
		if err := os.MkdirAll(q, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(q, "discard_max_bytes"), []byte(max+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	defer hostfs.SetRoot(root)()
	t.Setenv("HOST_PROC_MOUNTINFO", filepath.Join(root, "none"))
	fn, _ := counting(t, readFixture(t, "smartctl-nvme.json"))
	stubSmartctl(t, fn)
	oldTimer := fstrimTimerActive
	fstrimTimerActive = func(context.Context) bool { return true }
	periodicTrim.at = time.Time{}
	t.Cleanup(func() { fstrimTimerActive = oldTimer; periodicTrim.at = time.Time{} })

	ds := []Drive{
		{Name: "nvme0n1", Transport: "nvme"},
		{Name: "sda", Transport: "sata"},
		{Name: "vda", Transport: "virtio"},
	}
	addHealth(context.Background(), ds)
	if ds[0].Health == nil || ds[0].Health.Status != HealthOK || ds[0].Trim != "on" {
		t.Fatalf("nvme = %+v", ds[0])
	}
	if ds[1].Trim != "" {
		t.Fatalf("sda without discard support has trim %q", ds[1].Trim)
	}
	if ds[2].Health != nil {
		t.Fatalf("virtio drive has health %+v", ds[2].Health)
	}
}
