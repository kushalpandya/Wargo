// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package procs

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/host"
)

func (s *Service) scan() *List {
	now := time.Now()
	if s.bootTime == 0 {
		s.bootTime = readBootTime(s.root)
	}
	l := &List{UID: host.AdminUID(), Self: os.Getpid(), TotalMem: readMeminfoTotal(s.root), CPUs: runtime.NumCPU(), SampledAt: now.UnixMilli()}
	names := readDirNames(s.root)
	cur := make(map[int]cpuSample, len(names))
	dt := now.Sub(s.prevAt).Seconds()
	l.Procs = make([]Proc, 0, len(names))
	// One buffer for every small /proc read of this scan: 500+ processes
	// are scanned every second or two, so per-file allocations add up.
	buf := s.scanBuf[:]
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil || pid <= 0 {
			continue
		}
		dir := s.root + "/" + name
		n, err := readInto(dir+"/stat", buf)
		if err != nil || n == len(buf) {
			continue // exited (or a stat line we can't trust)
		}
		st, err := parseStat(buf[:n])
		if err != nil {
			continue
		}
		uid, _ := ownerUID(dir)
		p := Proc{
			PID: pid, PPID: st.PPID, Name: st.Name, UID: uid, User: s.userName(uid), State: st.State,
			Threads: st.Threads, Nice: st.Nice,
			MemRSS:     uint64(max(st.RSSPages, 0)) * s.pageSize,
			StartTime:  (s.bootTime*1000 + int64(st.StartTime)*1000/clkTck),
			startTicks: st.StartTime,
		}
		if l.TotalMem > 0 {
			p.MemPct = float64(p.MemRSS) / float64(l.TotalMem) * 100
		}
		ticks := st.UTime + st.STime
		cur[pid] = cpuSample{ticks: ticks, start: st.StartTime}
		if prev, ok := s.prev[pid]; ok && prev.start == st.StartTime && dt > 0 && ticks >= prev.ticks {
			p.CPU = float64(ticks-prev.ticks) / clkTck / dt * 100
		}
		// Only the first maxCmdline bytes are shown; don't read megabytes
		// of arguments.
		if n, err := readInto(dir+"/cmdline", buf[:maxCmdline]); err == nil {
			p.Command = cleanCmdline(buf[:n], maxCmdline)
		}
		if p.Command == "" && (st.PPID == 2 || pid == 2) {
			p.Kernel = true
		}
		l.Procs = append(l.Procs, p)
	}
	s.prev, s.prevAt = cur, now
	return l
}

func (s *Service) platformDetails(ctx context.Context, d *Details) {
	pid := d.PID
	dir := filepath.Join(s.root, strconv.Itoa(pid))
	cbuf := make([]byte, 64<<10)
	if n, err := readInto(filepath.Join(dir, "cmdline"), cbuf); err == nil {
		raw := cbuf[:n]
		d.Cmdline = cleanCmdline(raw, 0)
		for _, a := range strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00") {
			if a != "" && len(d.Args) < 256 {
				d.Args = append(d.Args, strings.ToValidUTF8(a, "?"))
			}
		}
	}
	if v, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
		d.Exe = v
		d.Readable = true
	}
	if v, err := os.Readlink(filepath.Join(dir, "cwd")); err == nil {
		d.Cwd = v
	}
	if b, err := os.ReadFile(filepath.Join(dir, "environ")); err == nil {
		n := 0
		for _, part := range strings.Split(string(b), "\x00") {
			if part != "" {
				n++
			}
		}
		d.EnvCount = &n
	}
	if f, err := os.Open(filepath.Join(dir, "fd")); err == nil {
		names, err := f.Readdirnames(-1)
		f.Close()
		if err == nil {
			n := len(names)
			d.FDCount = &n
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cgroup")); err == nil {
		d.Cgroup = strings.TrimSpace(string(b))
		if len(d.Cgroup) > 2048 {
			d.Cgroup = d.Cgroup[:2048]
		}
		if id := containerFromCgroup(b); id != "" {
			d.ContainerID = id
			if s.resolve != nil {
				rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				d.ContainerName = s.resolve(rctx, id)
				cancel()
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "statm")); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			vms, _ := strconv.ParseUint(f[0], 10, 64)
			rss, _ := strconv.ParseUint(f[1], 10, 64)
			sh, _ := strconv.ParseUint(f[2], 10, 64)
			d.Mem.VMS, d.Mem.RSS, d.Mem.Shared = vms*s.pageSize, rss*s.pageSize, sh*s.pageSize
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "status")); err == nil {
		kv := parseKV(b)
		d.Mem.Swap = kb(kv["VmSwap"])
		if f := strings.Fields(kv["Uid"]); len(f) > 0 {
			d.Status.RealUID, _ = strconv.Atoi(f[0])
		}
		d.Status.VolCtxSwitches, _ = strconv.ParseUint(kv["voluntary_ctxt_switches"], 10, 64)
		d.Status.InvCtxSwitches, _ = strconv.ParseUint(kv["nonvoluntary_ctxt_switches"], 10, 64)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "io")); err == nil {
		kv := parseKV(b)
		io := &struct {
			Read  uint64 `json:"read"`
			Write uint64 `json:"write"`
		}{}
		io.Read, _ = strconv.ParseUint(kv["read_bytes"], 10, 64)
		io.Write, _ = strconv.ParseUint(kv["write_bytes"], 10, 64)
		d.IO = io
	}
}

// verifyTarget checks that the process now holding p.PID is the one the
// user picked (same start time, so the pid wasn't reused) and, in
// container mode, that it belongs to the admin user. Wargo is root there
// and could signal anything; keep the native rule that other users'
// processes need approval (EPERM routes to elevation).
func (s *Service) verifyTarget(p Proc) error {
	dir := filepath.Join(s.root, strconv.Itoa(p.PID))
	var buf [1024]byte
	n, err := readInto(filepath.Join(dir, "stat"), buf[:])
	if err != nil {
		return syscall.ESRCH
	}
	st, err := parseStat(buf[:n])
	if err != nil || st.StartTime != p.startTicks {
		return syscall.ESRCH
	}
	if host.Container() {
		if uid, ok := ownerUID(dir); !ok || uid != host.AdminUID() {
			return syscall.EPERM
		}
	}
	return nil
}
