// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin && cgo

package libproc

/*
#include <errno.h>
#include <stdlib.h>
#include <string.h>
#include <libproc.h>
#include <sys/proc_info.h>
#include <sys/resource.h>
#include <sys/sysctl.h>
#include <mach/mach_time.h>

// list_fds returns the number of open descriptors of pid, or -1 with errno.
// proc_pidinfo reports failure as 0 with errno set, so errno is cleared
// before each call. The size it reports without a buffer is padded, so the
// list is read for real.
static int list_fds(int pid) {
	errno = 0;
	int n = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, NULL, 0);
	if (n <= 0) return errno != 0 ? -1 : 0;
	void *buf = malloc(n);
	if (buf == NULL) { errno = ENOMEM; return -1; }
	errno = 0;
	int got = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, buf, n);
	int e = errno;
	free(buf);
	errno = e;
	if (got <= 0) return e != 0 ? -1 : 0;
	return got / (int)sizeof(struct proc_fdinfo);
}

static int procargs2(int pid, void *buf, size_t *len) {
	int mib[3] = {CTL_KERN, KERN_PROCARGS2, pid};
	return sysctl(mib, 3, buf, len, NULL, 0);
}

static int disk_io(int pid, uint64_t *rd, uint64_t *wr) {
	struct rusage_info_v2 ri;
	if (proc_pid_rusage(pid, RUSAGE_INFO_V2, (rusage_info_t *)&ri) != 0) return -1;
	*rd = ri.ri_diskio_bytesread;
	*wr = ri.ri_diskio_byteswritten;
	return 0;
}
*/
import "C"

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sync"
	"syscall"
	"unsafe"
)

var timebase = sync.OnceValue(func() (tb C.mach_timebase_info_data_t) {
	C.mach_timebase_info(&tb)
	if tb.denom == 0 {
		tb.numer, tb.denom = 1, 1
	}
	return tb
})

// cstr converts a fixed-size C char array that may lack a terminating NUL.
func cstr(p *C.char, size int) string {
	return C.GoStringN(p, C.int(C.strnlen(p, C.size_t(size))))
}

func errnoOr(err error) error {
	if err == nil {
		return syscall.ESRCH
	}
	return err
}

// PIDs lists every process (proc_listallpids).
func PIDs() ([]int, error) {
	for {
		n, err := C.proc_listpids(C.PROC_ALL_PIDS, 0, nil, 0)
		if n <= 0 {
			return nil, errnoOr(err)
		}
		// Room for processes started between the two calls.
		buf := make([]C.int, int(n)/C.sizeof_int+64)
		got, err := C.proc_listpids(C.PROC_ALL_PIDS, 0, unsafe.Pointer(&buf[0]), C.int(len(buf)*C.sizeof_int))
		if got <= 0 {
			return nil, errnoOr(err)
		}
		cnt := int(got) / C.sizeof_int
		if cnt == len(buf) {
			continue // possibly truncated: retry with a fresh size
		}
		pids := make([]int, 0, cnt)
		for _, p := range buf[:cnt] {
			if p >= 0 {
				pids = append(pids, int(p))
			}
		}
		return pids, nil
	}
}

func bsdInfo(info *Info, b *C.struct_proc_bsdinfo) {
	info.PID = int(b.pbi_pid)
	info.PPID = int(b.pbi_ppid)
	info.UID = int(b.pbi_uid)
	info.RUID = int(b.pbi_ruid)
	info.Nice = int(b.pbi_nice)
	info.State = StateLetter(uint32(b.pbi_status))
	info.Name = cstr(&b.pbi_name[0], len(b.pbi_name))
	if info.Name == "" {
		info.Name = cstr(&b.pbi_comm[0], len(b.pbi_comm))
	}
	info.StartUs = uint64(b.pbi_start_tvsec)*1e6 + uint64(b.pbi_start_tvusec)
}

// Get reads PROC_PIDTASKALLINFO, falling back to PROC_PIDTBSDINFO (Task
// false) for processes whose task is out of reach (other users', zombies),
// and then to sysctl kern.proc.pid, which unprivileged callers may read for
// any process (Name is then p_comm, at most 16 bytes).
func Get(pid int) (Info, error) {
	if pid < 0 {
		return Info{}, syscall.ESRCH
	}
	var info Info
	var all C.struct_proc_taskallinfo
	n, _ := C.proc_pidinfo(C.int(pid), C.PROC_PIDTASKALLINFO, 0, unsafe.Pointer(&all), C.sizeof_struct_proc_taskallinfo)
	if n == C.sizeof_struct_proc_taskallinfo {
		bsdInfo(&info, &all.pbsd)
		t := &all.ptinfo
		tb := timebase()
		info.RSS = uint64(t.pti_resident_size)
		info.VMS = uint64(t.pti_virtual_size)
		info.CPUNs = (uint64(t.pti_total_user) + uint64(t.pti_total_system)) * uint64(tb.numer) / uint64(tb.denom)
		info.Threads = int(t.pti_threadnum)
		info.CtxSwitches = uint64(t.pti_csw)
		info.Task = true
		return info, nil
	}
	var b C.struct_proc_bsdinfo
	n, _ = C.proc_pidinfo(C.int(pid), C.PROC_PIDTBSDINFO, 0, unsafe.Pointer(&b), C.sizeof_struct_proc_bsdinfo)
	if n != C.sizeof_struct_proc_bsdinfo {
		return kinfoGet(pid)
	}
	bsdInfo(&info, &b)
	return info, nil
}

// Args reads kern.procargs2 into buf (size it with ArgMax) and returns the
// argument vector and the number of environment strings that follow it.
func Args(pid int, buf []byte) ([]string, int, error) {
	if pid < 0 || len(buf) < 4 {
		return nil, 0, syscall.EINVAL
	}
	n := C.size_t(len(buf))
	if r, err := C.procargs2(C.int(pid), unsafe.Pointer(&buf[0]), &n); r != 0 {
		return nil, 0, errnoOr(err)
	}
	b := buf[:min(int(n), len(buf))]
	if len(b) < 4 {
		return nil, 0, errors.New("short procargs")
	}
	argc := int(binary.LittleEndian.Uint32(b))
	b = b[4:]
	// exec path, then NUL padding up to argv[0].
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[i:]
	} else {
		return nil, 0, errors.New("malformed procargs")
	}
	b = bytes.TrimLeft(b, "\x00")
	next := func() (string, bool) {
		if len(b) == 0 {
			return "", false
		}
		i := bytes.IndexByte(b, 0)
		if i < 0 {
			i = len(b)
		}
		s := string(b[:i])
		b = b[min(i+1, len(b)):]
		return s, true
	}
	args := make([]string, 0, min(argc, len(b)/2))
	for len(args) < argc {
		s, ok := next()
		if !ok {
			break
		}
		args = append(args, s)
	}
	env := 0
	for {
		s, ok := next()
		if !ok || s == "" {
			break
		}
		env++
	}
	return args, env, nil
}

// Path returns the executable path (proc_pidpath).
func Path(pid int) (string, error) {
	buf := make([]byte, C.PROC_PIDPATHINFO_MAXSIZE)
	n, err := C.proc_pidpath(C.int(pid), unsafe.Pointer(&buf[0]), C.uint32_t(len(buf)))
	if n <= 0 {
		return "", errnoOr(err)
	}
	return string(buf[:n]), nil
}

// Cwd returns the working directory (PROC_PIDVNODEPATHINFO).
func Cwd(pid int) (string, error) {
	var v C.struct_proc_vnodepathinfo
	n, err := C.proc_pidinfo(C.int(pid), C.PROC_PIDVNODEPATHINFO, 0, unsafe.Pointer(&v), C.sizeof_struct_proc_vnodepathinfo)
	if n != C.sizeof_struct_proc_vnodepathinfo {
		return "", errnoOr(err)
	}
	return cstr(&v.pvi_cdir.vip_path[0], len(v.pvi_cdir.vip_path)), nil
}

// FDCount returns the number of open file descriptors (PROC_PIDLISTFDS).
func FDCount(pid int) (int, error) {
	n, err := C.list_fds(C.int(pid))
	if n < 0 {
		return 0, errnoOr(err)
	}
	return int(n), nil
}

// DiskIO returns bytes read from and written to disk (proc_pid_rusage
// RUSAGE_INFO_V2).
func DiskIO(pid int) (read, write uint64, err error) {
	var rd, wr C.uint64_t
	if r, e := C.disk_io(C.int(pid), &rd, &wr); r != 0 {
		return 0, 0, errnoOr(e)
	}
	return uint64(rd), uint64(wr), nil
}
