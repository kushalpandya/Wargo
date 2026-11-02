// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SetupEnv points gopsutil at the host's pseudo filesystems (container
// mode). Call before any metrics are collected.
func SetupEnv() {
	r := Root()
	if r == "" {
		return
	}
	for k, v := range map[string]string{
		"HOST_PROC": "/proc", "HOST_SYS": "/sys", "HOST_ETC": "/etc", "HOST_VAR": "/var",
		"HOST_RUN": "/run", "HOST_DEV": "/dev", "HOST_ROOT": "/",
	} {
		os.Setenv(k, filepath.Join(r, v))
	}
	// Mounts must be the host's (PID 1's view), not the container's.
	os.Setenv("HOST_PROC_MOUNTINFO", filepath.Join(r, "proc/1/mountinfo"))
}

// CheckError explains a failed container-mode self-check.
type CheckError struct{ Problems []string }

func (e *CheckError) Error() string {
	return "container mode self-check failed:\n  - " + strings.Join(e.Problems, "\n  - ") +
		"\nRun the container with: privileged: true, pid: host, network_mode: host, uts: host and the volume /:" + Root() +
		" (see docs/DOCKER.md)."
}

// Check verifies the container-mode prerequisites and returns a
// descriptive error when something is missing. Native mode: nil.
func Check(ctx context.Context) error {
	r := Root()
	if r == "" {
		return nil
	}
	var probs []string
	if os.Geteuid() != 0 {
		probs = append(probs, fmt.Sprintf("Wargo must run as root inside the container (uid is %d)", os.Geteuid()))
	}
	if st, err := os.Stat(r); err != nil || !st.IsDir() {
		probs = append(probs, fmt.Sprintf("%s=%s is not a directory: mount the host's / there", EnvRoot, r))
		return &CheckError{probs}
	}
	if _, err := os.Stat(filepath.Join(r, "etc/passwd")); err != nil {
		probs = append(probs, fmt.Sprintf("%s/etc/passwd is missing: is the host's / mounted at %s?", r, r))
	}
	if _, err := os.Stat(filepath.Join(r, "proc/1")); err != nil {
		probs = append(probs, fmt.Sprintf("%s/proc is not the host's /proc (bind-mount / recursively)", r))
	} else if err := sharesHostPID(r); err != nil {
		probs = append(probs, err.Error())
	}
	if len(probs) == 0 {
		if err := nsenterWorks(ctx); err != nil {
			probs = append(probs, err.Error())
		}
	}
	if len(probs) > 0 {
		return &CheckError{probs}
	}
	return nil
}

// sharesHostPID verifies that Wargo runs in the host's PID namespace
// (pid: host): the host's PID 1 (seen through the host's /proc mount) must
// be in our own PID namespace.
func sharesHostPID(r string) error {
	self, err1 := os.Readlink("/proc/self/ns/pid")
	init, err2 := os.Readlink(filepath.Join(r, "proc/1/ns/pid"))
	if err1 == nil && err2 == nil {
		if self != init {
			return errors.New("not in the host PID namespace: set pid: host")
		}
		return nil
	}
	// Fall back to a heuristic when the ns links aren't readable: in a
	// private PID namespace our parent (tini) or we ourselves are PID 1.
	if os.Getpid() == 1 || os.Getppid() == 1 && isContainerInit(1) {
		return errors.New("not in the host PID namespace: set pid: host")
	}
	return nil
}

func isContainerInit(pid int) bool {
	b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	switch strings.TrimSpace(string(b)) {
	case "tini", "docker-init", "dumb-init", "wargo", "catatonit":
		return true
	}
	return false
}

func nsenterWorks(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := Command(ctx, "/bin/sh", "-c", "exit 0")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("cannot enter the host namespaces with nsenter (%s): set privileged: true", msg)
	}
	return nil
}

var containerIDRe = regexp.MustCompile(`/(?:docker/)?containers/([0-9a-f]{64})/`)

// SelfContainerID returns Wargo's own Docker container id (container mode
// only) from the bind mounts Docker sets up for /etc/hostname and friends.
func SelfContainerID() string {
	if !Container() {
		return ""
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	return containerIDFromMountinfo(f)
}

func containerIDFromMountinfo(r interface{ Read([]byte) (int, error) }) string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		l := sc.Text()
		if !strings.Contains(l, "/etc/hostname") && !strings.Contains(l, "/etc/hosts") && !strings.Contains(l, "/etc/resolv.conf") {
			continue
		}
		if m := containerIDRe.FindStringSubmatch(l); m != nil {
			return m[1]
		}
	}
	return ""
}
