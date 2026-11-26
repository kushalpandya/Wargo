// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package metrics

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	hostfs "github.com/kushalpandya/wargo/internal/host"
)

// SMART data changes slowly and reading it touches the drive, so each
// drive is read at most every smartTTL (smartTTLRotational for hard disks,
// whose spin-down timer some drives restart on SMART commands). Reads run
// in the background: a request waits at most smartWait for a first read,
// and after that is served from the cache while a stale entry refreshes,
// so a hung drive never holds up the Storage request.
const (
	smartTTL           = 10 * time.Minute
	smartTTLRotational = 30 * time.Minute
	smartTimeout       = 10 * time.Second
)

var smartWait = smartTimeout + time.Second

// smartSlots bounds how many smartctl processes run at once.
var smartSlots = make(chan struct{}, 4)

// smartEntry is one drive's cached health. ready is closed once the first
// read finishes, so concurrent first requests share one smartctl run.
type smartEntry struct {
	at      time.Time
	h       *DriveHealth
	pending bool
	ready   chan struct{}
}

// smartCache is keyed by what identifies the physical disk, so a different
// disk that takes over a name (USB sdb unplugged, another plugged in) is
// read afresh. SATA and USB disks have no serial in sysfs; their wwid,
// model and size stand in.
var smartCache = struct {
	mu sync.Mutex
	m  map[string]*smartEntry
}{m: map[string]*smartEntry{}}

func smartKey(d Drive) string {
	wwid := readStr(hostfs.Path(filepath.Join("/sys/block", d.Name, "device", "wwid")))
	return strings.Join([]string{d.Name, d.Serial, wwid, d.Model, strconv.FormatUint(d.Size, 10)}, "\x00")
}

// smartctlPath finds smartctl on the host. Native PATHs often lack sbin,
// where distributions put it.
var smartctlPath = func() (string, error) {
	p, err := hostfs.LookPath("smartctl")
	if err == nil || hostfs.Container() {
		return p, err
	}
	for _, d := range filepath.SplitList(hostfs.DefaultPath) {
		if c := filepath.Join(d, "smartctl"); executableFile(c) {
			return c, nil
		}
	}
	return "", err
}

func executableFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0
}

// runSmartctl runs smartctl on the host (as root in container mode).
// -n standby leaves a sleeping drive asleep instead of spinning it up.
var runSmartctl = func(ctx context.Context, bin, dev string) ([]byte, error) {
	return hostfs.Command(ctx, bin, "-j", "-a", "-n", "standby", dev).Output()
}

// readSmart reads one drive. prev is the last result, kept while the
// drive sleeps.
func readSmart(dev string, prev *DriveHealth) *DriveHealth {
	bin, err := smartctlPath()
	if err != nil {
		return unknownHealth("Install smartmontools to see drive health")
	}
	smartSlots <- struct{}{}
	defer func() { <-smartSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), smartTimeout)
	defer cancel()
	out, err := runSmartctl(ctx, bin, dev)
	if len(out) == 0 {
		if ctx.Err() != nil {
			return unknownHealth("The drive did not answer in time")
		}
		return unknownHealth("Could not read drive health")
	}
	f, standby, err := parseSmartctl(out)
	switch {
	case err != nil:
		return unknownHealth("Could not read drive health")
	case standby && !f.Known:
		if prev != nil && prev.Status != HealthUnknown {
			return prev
		}
		return unknownHealth("The drive is asleep; its health shows once it wakes")
	}
	h := verdict(f)
	return &h
}

// refresh reads the drive in the background and stores the result.
func (e *smartEntry) refresh(dev string, prev *DriveHealth) {
	c := &smartCache
	go func() {
		var h *DriveHealth
		defer func() {
			c.mu.Lock()
			e.h, e.at, e.pending = h, time.Now(), false
			c.mu.Unlock()
			select {
			case <-e.ready:
			default:
				close(e.ready)
			}
		}()
		h = readSmart(dev, prev)
	}()
}

// smartHealth returns the cached health of a drive, starting a read when
// it was never read or is stale. It returns nil when ctx ends first, and
// "still reading" when a first read takes longer than smartWait.
func smartHealth(ctx context.Context, d Drive) *DriveHealth {
	c := &smartCache
	ttl := smartTTL
	if d.Rotational {
		ttl = smartTTLRotational
	}
	key := smartKey(d)
	c.mu.Lock()
	e := c.m[key]
	if e == nil {
		e = &smartEntry{ready: make(chan struct{}), pending: true}
		c.m[key] = e
		e.refresh("/dev/"+d.Name, nil)
	} else if !e.pending && time.Since(e.at) > ttl {
		e.pending = true
		e.refresh("/dev/"+d.Name, e.h)
	}
	c.mu.Unlock()

	t := time.NewTimer(smartWait)
	defer t.Stop()
	select {
	case <-e.ready:
	case <-ctx.Done():
		return nil
	case <-t.C:
		return unknownHealth("Still reading drive health")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return e.h
}

// smartHealthAll reads the drives in parallel, so a first load waits for
// the slowest drive rather than the sum of them.
func smartHealthAll(ctx context.Context, ds []Drive) map[string]*DriveHealth {
	out := make(map[string]*DriveHealth, len(ds))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, d := range ds {
		wg.Go(func() {
			h := smartHealth(ctx, d)
			mu.Lock()
			out[d.Name] = h
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// periodicTrim reports whether the host runs fstrim on a timer, cached
// with the SMART data.
var periodicTrim = struct {
	mu sync.Mutex
	at time.Time
	on bool
}{}

var fstrimTimerActive = func(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	out, _ := hostfs.Command(ctx, "systemctl", "is-active", "fstrim.timer").Output()
	return strings.TrimSpace(string(out)) == "active"
}

// fstrimTimer does not hold the lock while systemctl runs; two requests
// racing on a stale value both ask, which is harmless.
func fstrimTimer(ctx context.Context) bool {
	p := &periodicTrim
	p.mu.Lock()
	fresh, on := !p.at.IsZero() && time.Since(p.at) <= smartTTL, p.on
	p.mu.Unlock()
	if fresh {
		return on
	}
	on = fstrimTimerActive(ctx)
	p.mu.Lock()
	p.on, p.at = on, time.Now()
	p.mu.Unlock()
	return on
}

// mountinfoPath is the mount table gopsutil reads (the host's in
// container mode).
func mountinfoPath() string {
	if p := os.Getenv("HOST_PROC_MOUNTINFO"); p != "" {
		return p
	}
	return "/proc/self/mountinfo"
}

// discardDevices returns the kernel names (nvme0n1p2) of devices mounted
// with online discard. Both the per-mount and the superblock options are
// checked: btrfs reports discard=async as a superblock option.
func discardDevices() map[string]bool {
	out := map[string]bool{}
	f, err := os.Open(mountinfoPath())
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		pre, post, ok := strings.Cut(sc.Text(), " - ")
		if !ok {
			continue
		}
		a, b := strings.Fields(pre), strings.Fields(post)
		if len(a) < 6 || len(b) < 3 || !strings.HasPrefix(b[1], "/dev/") {
			continue
		}
		if !discardOpt(strings.Split(a[5], ",")) && !discardOpt(strings.Split(b[2], ",")) {
			continue
		}
		dev := b[1]
		if r, err := hostfs.EvalSymlinks(dev); err == nil {
			dev = r
		}
		markWithSlaves(out, strings.TrimPrefix(dev, "/dev/"), 0)
	}
	return out
}

// markWithSlaves marks a device and, for device-mapper and md devices
// (LUKS, LVM, RAID), the partitions or disks underneath them, which is
// where the drive's TRIM state is looked up.
func markWithSlaves(out map[string]bool, name string, depth int) {
	out[name] = true
	if depth > 8 {
		return
	}
	ents, _ := os.ReadDir(hostfs.Path(filepath.Join("/sys/class/block", name, "slaves")))
	for _, e := range ents {
		markWithSlaves(out, e.Name(), depth+1)
	}
}

// hasHealth reports whether a drive's transport can carry SMART data.
func hasHealth(transport string) bool { return transport != "virtio" && transport != "mmc" }

// addHealth fills Health and Trim on drives.
func addHealth(ctx context.Context, ds []Drive) {
	var want []Drive
	for _, d := range ds {
		if hasHealth(d.Transport) {
			want = append(want, d)
		}
	}
	hs := smartHealthAll(ctx, want)
	var discard map[string]bool
	timer := false
	for i := range ds {
		d := &ds[i]
		d.Health = hs[d.Name]
		if d.Rotational {
			continue
		}
		dm, _ := readInt(hostfs.Path(filepath.Join("/sys/block", d.Name, "queue", "discard_max_bytes")))
		if dm <= 0 {
			continue
		}
		if discard == nil {
			discard, timer = discardDevices(), fstrimTimer(ctx)
		}
		fsDiscard := discard[d.Name]
		for _, p := range d.Partitions {
			fsDiscard = fsDiscard || discard[p.Name]
		}
		d.Trim = trimLabel(fsDiscard || timer)
	}
}
