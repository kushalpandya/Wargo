// SPDX-License-Identifier: AGPL-3.0-only

package procs

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// clkTck is USER_HZ; 100 on every mainstream Linux architecture.
const clkTck = 100

const maxCmdline = 512

// statFields is what we take from /proc/<pid>/stat.
type statFields struct {
	Name      string
	State     string
	PPID      int
	UTime     uint64 // clock ticks
	STime     uint64
	Nice      int
	Threads   int
	StartTime uint64 // ticks since boot
	VSize     uint64 // bytes
	RSSPages  int64
}

// parseStat parses the contents of /proc/<pid>/stat. The command name is in
// parentheses and may itself contain spaces and parentheses, so we split on
// the last ')'.
func parseStat(b []byte) (statFields, error) {
	var s statFields
	open := bytes.IndexByte(b, '(')
	closeIdx := bytes.LastIndexByte(b, ')')
	if open < 0 || closeIdx < open {
		return s, errors.New("malformed stat")
	}
	s.Name = string(b[open+1 : closeIdx])
	f := strings.Fields(string(b[closeIdx+1:]))
	// f[0] is field 3 (state).
	if len(f) < 22 {
		return s, errors.New("short stat")
	}
	s.State = f[0]
	s.PPID, _ = strconv.Atoi(f[1])
	s.UTime, _ = strconv.ParseUint(f[11], 10, 64)
	s.STime, _ = strconv.ParseUint(f[12], 10, 64)
	s.Nice, _ = strconv.Atoi(f[16])
	s.Threads, _ = strconv.Atoi(f[17])
	s.StartTime, _ = strconv.ParseUint(f[19], 10, 64)
	s.VSize, _ = strconv.ParseUint(f[20], 10, 64)
	s.RSSPages, _ = strconv.ParseInt(f[21], 10, 64)
	return s, nil
}

// cleanCmdline turns NUL-separated argv into a printable string.
func cleanCmdline(b []byte, limit int) string {
	b = bytes.TrimRight(b, "\x00")
	if limit > 0 && len(b) > limit {
		b = b[:limit]
	}
	out := make([]byte, len(b))
	for i, c := range b {
		switch {
		case c == 0:
			out[i] = ' '
		case c < 0x20 || c == 0x7f:
			out[i] = '?'
		default:
			out[i] = c
		}
	}
	return strings.ToValidUTF8(string(out), "?")
}

// readMeminfoTotal returns MemTotal in bytes.
func readMeminfoTotal(root string) uint64 {
	f, err := os.Open(filepath.Join(root, "meminfo"))
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "MemTotal:") {
			fs := strings.Fields(l)
			if len(fs) >= 2 {
				v, _ := strconv.ParseUint(fs[1], 10, 64)
				return v * 1024
			}
		}
	}
	return 0
}

// readBootTime returns btime (unix seconds) from /proc/stat.
func readBootTime(root string) int64 {
	f, err := os.Open(filepath.Join(root, "stat"))
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "btime ") {
			v, _ := strconv.ParseInt(strings.TrimSpace(l[6:]), 10, 64)
			return v
		}
	}
	return 0
}

// ownerUID returns the uid owning /proc/<pid> (the effective uid).
func ownerUID(dir string) (int, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		return 0, false
	}
	return int(st.Uid), true
}

// readInto reads up to len(buf) bytes of path into buf. /proc files are
// generated on read, so this avoids os.ReadFile's growing buffer and
// bounds the read (a cmdline or environ may be megabytes).
func readInto(path string, buf []byte) (int, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer syscall.Close(fd)
	n := 0
	for n < len(buf) {
		m, err := syscall.Read(fd, buf[n:])
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return n, err
		}
		if m <= 0 {
			break
		}
		n += m
	}
	return n, nil
}

// readDirNames lists a directory without sorting or stat-ing entries.
func readDirNames(dir string) []string {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	names, _ := f.Readdirnames(-1)
	return names
}

var containerIDRe = regexp.MustCompile(`(?:docker|libpod)[-/]([0-9a-f]{64})`)

// containerFromCgroup extracts a Docker container id from /proc/<pid>/cgroup.
func containerFromCgroup(b []byte) string {
	if m := containerIDRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// parseKV parses "Key: value" lines (status, io) into a map.
func parseKV(b []byte) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, ":")
		if ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// kb parses "1234 kB" into bytes.
func kb(s string) uint64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseUint(f[0], 10, 64)
	if len(f) > 1 && strings.EqualFold(f[1], "kB") {
		v *= 1024
	}
	return v
}
