// SPDX-License-Identifier: AGPL-3.0-only

// Package helper is the privileged half of Wargo. It runs as root only when
// the user approves a specific action (via sudo or wargo-privd) and accepts
// a small, strictly validated set of operations. It never invokes a shell.
package helper

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/kushalpandya/wargo/internal/fsops"
)

var signals = map[string]syscall.Signal{
	"TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "HUP": syscall.SIGHUP, "INT": syscall.SIGINT,
	"STOP": syscall.SIGSTOP, "CONT": syscall.SIGCONT, "USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2,
}

// Signal resolves a signal name.
func Signal(name string) (syscall.Signal, bool) {
	s, ok := signals[name]
	return s, ok
}

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "wargo helper: "+format+"\n", a...)
	return 1
}

// Main runs a helper operation and returns the exit code.
func Main(args []string) int {
	if os.Geteuid() != 0 {
		return fail("must run as root (via sudo)")
	}
	if len(args) == 0 {
		return fail("missing operation")
	}
	var err error
	switch args[0] {
	case "kill":
		err = kill(args[1:])
	case "fs":
		err = fsOp(args[1:])
	case "power":
		err = power(args[1:])
	case "compose-update":
		err = composeUpdate(args[1:])
	case "privd-trust":
		err = privdTrust(args[1:])
	case "pkg-run":
		return pkgRun(args[1:])
	default:
		return fail("unknown operation %q", args[0])
	}
	if err != nil {
		return fail("%v", err)
	}
	return 0
}

func kill(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: kill <SIGNAL> <pid>")
	}
	sig, ok := Signal(args[0])
	if !ok {
		return fmt.Errorf("unsupported signal %q", args[0])
	}
	pid, err := strconv.Atoi(args[1])
	if err != nil || pid <= 1 {
		return fmt.Errorf("invalid pid %q", args[1])
	}
	return syscall.Kill(pid, sig)
}

// fs <op> <root> <path> [arg]
func fsOp(args []string) error {
	if len(args) < 3 {
		return errors.New("usage: fs <op> <root> <path> [arg]")
	}
	op, rootPath, p := args[0], args[1], args[2]
	if !filepath.IsAbs(rootPath) {
		return errors.New("root must be absolute")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	arg := ""
	if len(args) > 3 {
		arg = args[3]
	}
	p = fsops.Clean(p)
	switch op {
	case "mkdir":
		return root.Mkdir(p, 0o755)
	case "remove":
		return fsops.Remove(root, p)
	case "move":
		return fsops.Move(root, p, arg)
	case "copy":
		return fsops.Copy(root, p, arg, nil)
	case "chmod":
		m, err := fsops.ParseMode(arg)
		if err != nil {
			return err
		}
		return root.Chmod(p, m)
	case "chown":
		uid, gid, err := fsops.ParseOwner(arg)
		if err != nil {
			return err
		}
		return root.Lchown(p, uid, gid)
	case "write":
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 32<<20))
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if st, err := root.Stat(p); err == nil {
			mode = st.Mode().Perm()
		}
		return root.WriteFile(p, data, mode)
	case "read":
		f, err := root.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(os.Stdout, io.LimitReader(f, 32<<20))
		return err
	}
	return fmt.Errorf("unknown fs op %q", op)
}

func power(args []string) error {
	if len(args) != 1 || (args[0] != "reboot" && args[0] != "poweroff") {
		return errors.New("usage: power reboot|poweroff")
	}
	return exec.Command("systemctl", args[0]).Run()
}

// compose-update <working_dir> <project> <config_file>
func composeUpdate(args []string) error {
	if len(args) != 3 {
		return errors.New("usage: compose-update <dir> <project> <file>")
	}
	dir, project, file := args[0], args[1], args[2]
	if !filepath.IsAbs(dir) || !filepath.IsAbs(file) {
		return errors.New("paths must be absolute")
	}
	for _, step := range [][]string{
		{"compose", "-p", project, "-f", file, "pull"},
		{"compose", "-p", project, "-f", file, "up", "-d", "--remove-orphans"},
	} {
		cmd := exec.Command("docker", step...)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker %v: %w", step, err)
		}
	}
	return nil
}

// PkgExitMarker is printed after a package-manager run so the caller can
// read the exit status from the log even after systemd collects the unit.
const PkgExitMarker = "[wargo] exit status "

// PackageManagers may be launched by pkg-run.
var PackageManagers = []string{"pacman", "pamac", "apt-get", "apt", "dnf", "yum", "zypper", "apk", "flatpak"}

var systemBinDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/usr/local/bin"}

// ResolvePackageManager returns the absolute path of an allowed package
// manager binary, or an error.
func ResolvePackageManager(name string) (string, error) {
	if !filepath.IsAbs(name) && strings.Contains(name, "/") {
		return "", fmt.Errorf("%q must be a bare name or absolute path", name)
	}
	base := filepath.Base(name)
	allowed := false
	for _, pm := range PackageManagers {
		if pm == base {
			allowed = true
		}
	}
	if !allowed {
		return "", fmt.Errorf("%q is not a supported package manager", name)
	}
	p := name
	if !filepath.IsAbs(p) {
		var err error
		if p, err = exec.LookPath(base); err != nil {
			return "", err
		}
	}
	p = filepath.Clean(p)
	for _, d := range systemBinDirs {
		if filepath.Dir(p) == d {
			return p, nil
		}
	}
	return "", fmt.Errorf("%q is not in a system directory", p)
}

// pkg-run -- <package manager> [args...]: runs the package manager and
// prints PkgExitMarker with its exit status.
func pkgRun(args []string) int {
	if len(args) < 2 || args[0] != "--" {
		return fail("usage: pkg-run -- <package-manager> [args...]")
	}
	bin, err := ResolvePackageManager(args[1])
	if err != nil {
		return fail("%v", err)
	}
	cmd := exec.Command(bin, args[2:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			fmt.Fprintln(os.Stderr, "wargo helper:", err)
			code = 127
		}
	}
	fmt.Printf("\n%s%d\n", PkgExitMarker, code)
	return code
}

// PrivdCredentialsPath is where wargo-privd keeps trusted passkeys.
const PrivdCredentialsPath = "/var/lib/wargo-privd/credentials.json"

// privd-trust: stdin is the JSON credential store for wargo-privd.
func privdTrust(args []string) error {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return err
	}
	if len(data) == 0 || data[0] != '{' {
		return errors.New("expected JSON on stdin")
	}
	if err := os.MkdirAll(filepath.Dir(PrivdCredentialsPath), 0o700); err != nil {
		return err
	}
	tmp := PrivdCredentialsPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, PrivdCredentialsPath)
}
