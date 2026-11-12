// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package procs

import (
	"context"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/libproc"
)

func (s *Service) scan() *List {
	now := time.Now()
	l := &List{UID: host.AdminUID(), Self: os.Getpid(), CPUs: runtime.NumCPU(), SampledAt: now.UnixMilli()}
	l.TotalMem, _ = unix.SysctlUint64("hw.memsize")
	pids, _ := libproc.PIDs()
	cur := make(map[int]cpuSample, len(pids))
	dt := now.Sub(s.prevAt).Seconds()
	l.Procs = make([]Proc, 0, len(pids))
	if s.scanArgs == nil {
		s.scanArgs = make([]byte, libproc.ArgMax())
	}
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		info, err := libproc.Get(pid)
		if err != nil {
			continue // exited
		}
		p := Proc{
			PID: pid, PPID: info.PPID, Name: info.Name, UID: info.UID, User: s.userName(info.UID), State: info.State,
			Threads: info.Threads, Nice: info.Nice, MemRSS: info.RSS,
			StartTime: int64(info.StartUs / 1000), startTicks: info.StartUs,
		}
		if l.TotalMem > 0 {
			p.MemPct = float64(p.MemRSS) / float64(l.TotalMem) * 100
		}
		cur[pid] = cpuSample{ticks: info.CPUNs, start: info.StartUs}
		if prev, ok := s.prev[pid]; ok && prev.start == info.StartUs && dt > 0 && info.CPUNs >= prev.ticks {
			p.CPU = float64(info.CPUNs-prev.ticks) / 1e9 / dt * 100
		}
		if args, _, err := libproc.Args(pid, s.scanArgs); err == nil && len(args) > 0 {
			p.Command = cleanCmdline([]byte(strings.Join(args, "\x00")), maxCmdline)
		}
		if p.Command == "" {
			p.Command = info.Name
		}
		l.Procs = append(l.Procs, p)
	}
	s.prev, s.prevAt = cur, now
	return l
}

// platformDetails fills the fields libproc can read. Anything it cannot
// read (other users' processes) stays empty, and Readable stays false.
func (s *Service) platformDetails(ctx context.Context, d *Details) {
	pid := d.PID
	if path, err := libproc.Path(pid); err == nil {
		d.Exe = path
		d.Readable = true
	}
	if v, err := libproc.Cwd(pid); err == nil {
		d.Cwd = v
	}
	if n, err := libproc.FDCount(pid); err == nil {
		d.FDCount = &n
	}
	if args, env, err := libproc.Args(pid, make([]byte, libproc.ArgMax())); err == nil {
		for _, a := range args {
			if a != "" && len(d.Args) < 256 {
				d.Args = append(d.Args, strings.ToValidUTF8(a, "?"))
			}
		}
		d.Cmdline = cleanCmdline([]byte(strings.Join(args, "\x00")), 0)
		d.EnvCount = &env
	}
	if info, err := libproc.Get(pid); err == nil {
		d.Mem.RSS, d.Mem.VMS = info.RSS, info.VMS
		d.Status.RealUID = info.RUID
		d.Status.VolCtxSwitches = info.CtxSwitches
	}
	if r, w, err := libproc.DiskIO(pid); err == nil {
		d.IO = &struct {
			Read  uint64 `json:"read"`
			Write uint64 `json:"write"`
		}{Read: r, Write: w}
	}
}

// verifyTarget checks that the process now holding p.PID is the one the
// user picked (same start time, so the pid wasn't reused).
func (s *Service) verifyTarget(p Proc) error {
	if st, err := libproc.StartTime(p.PID); err != nil || st != p.startTicks {
		return syscall.ESRCH
	}
	return nil
}
