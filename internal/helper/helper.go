// SPDX-License-Identifier: AGPL-3.0-only

// Package helper is the privileged half of Wargo. It runs as root only when
// the user approves a specific action (via sudo or wargo-privd natively, or
// in-process after the approval was verified in container mode) and accepts
// a small, strictly validated set of operations. It never invokes a shell.
package helper

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/host"
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

// Main runs a helper operation (`wargo helper ...` under sudo or privd)
// and returns the exit code.
func Main(args []string) int {
	if os.Geteuid() != 0 {
		return fail("must run as root (via sudo)")
	}
	uid, err := stdinFileOwner(args, os.Getenv("SUDO_UID"), os.Getuid())
	if err != nil {
		return fail("%v", err)
	}
	stdin := defaultStdin(os.Getenv("SUDO_UID"))
	in, rest, err := parseStdinFile(args, uid)
	if err != nil {
		return fail("%v", err)
	}
	if in != nil {
		defer in.Close()
		stdin = in
	}
	code, err := Exec(rest, stdin, os.Stdout, os.Stderr)
	if err != nil {
		return fail("%v", err)
	}
	return code
}

// defaultStdin is the payload reader when no --stdin-file is given. Under
// sudo, stdin carried the password (and under NOPASSWD that line is still
// unread), so the operation gets an empty reader instead. wargo-privd runs
// the helper without SUDO_UID and pipes the payload on stdin.
func defaultStdin(sudoUID string) io.Reader {
	if sudoUID != "" {
		return strings.NewReader("")
	}
	return os.Stdin
}

// stdinFileOwner picks the uid that must own a --stdin-file payload. The
// option is only meant for the sudo path, so it is refused when SUDO_UID is
// unset (wargo-privd runs helper as root with no SUDO_UID and must never be
// pointed at arbitrary root-owned files).
func stdinFileOwner(args []string, sudoUID string, realUID int) (int, error) {
	if sudoUID == "" {
		if len(args) > 0 && args[0] == "--stdin-file" {
			return 0, errors.New("--stdin-file is only valid under sudo")
		}
		return realUID, nil
	}
	n, err := strconv.Atoi(sudoUID)
	if err != nil || n < 0 {
		return 0, errors.New("invalid SUDO_UID")
	}
	return n, nil
}

// maxStdinFile bounds a spooled payload (see elevate.runSudo).
const maxStdinFile = 32 << 20

// parseStdinFile handles the optional leading `--stdin-file <path>`. The
// unprivileged server spools a payload there so it never shares sudo's
// stdin with the password. The file must be a regular file (symlinks are
// refused) owned by uid and at most 32 MiB. The helper never removes it:
// removing by name as root would act on whatever the path names by then,
// and runSudo removes its own spool file. With no option, stdin is nil and
// args are returned as is.
func parseStdinFile(args []string, uid int) (io.ReadCloser, []string, error) {
	if len(args) == 0 || args[0] != "--stdin-file" {
		return nil, args, nil
	}
	if len(args) < 3 {
		return nil, nil, errors.New("--stdin-file needs a path and an operation")
	}
	path, rest := args[1], args[2:]
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open stdin file: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.Mode().IsRegular():
		err = errors.New("stdin file is not a regular file")
	case !ok || int(st.Uid) != uid:
		err = errors.New("stdin file has the wrong owner")
	case fi.Size() > maxStdinFile:
		err = errors.New("stdin file is too large")
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, rest, nil
}

// Exec runs one helper operation in the calling process, which must
// already be root (the sub-command, or Wargo itself in container mode once
// the elevation credential was verified). File operations run with the
// caller's thread identity, so container-mode callers must not be inside
// host.AsUser. It returns the exit code (non-zero only for pkg-run) and an
// error for invalid or failed operations.
func Exec(args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 {
		return 1, errors.New("missing operation")
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	var err error
	switch args[0] {
	case "kill":
		err = kill(args[1:])
	case "fs":
		err = fsOp(args[1:], stdin, stdout)
	case "power":
		err = power(args[1:])
	case "compose-update":
		err = composeUpdate(args[1:], stdout, stderr)
	case "privd-trust":
		if host.Container() {
			return 1, errors.New("wargo-privd is not used in container mode")
		}
		err = privdTrust(args[1:], stdin)
	case "pkg-run":
		return pkgRun(args[1:], stdout, stderr)
	default:
		return 1, fmt.Errorf("unknown operation %q", args[0])
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// kill <SIGNAL> <pid> [starttime]: starttime is the expected process
// start (/proc/<pid>/stat field 22, clock ticks since boot); when given,
// the signal is only delivered to that exact process (pid-reuse safe).
func kill(args []string) error {
	if len(args) != 2 && len(args) != 3 {
		return errors.New("usage: kill <SIGNAL> <pid> [starttime]")
	}
	sig, ok := Signal(args[0])
	if !ok {
		return fmt.Errorf("unsupported signal %q", args[0])
	}
	pid, err := ParsePID(args[1])
	if err != nil {
		return err
	}
	var start uint64
	if len(args) == 3 {
		if start, err = strconv.ParseUint(args[2], 10, 64); err != nil {
			return fmt.Errorf("invalid start time %q", args[2])
		}
	}
	return signalPID(pid, sig, start)
}

// maxPID is the kernel's PID_MAX_LIMIT (4194304).
const maxPID = 1 << 22

// ParsePID accepts a single process id: plain decimal digits, 2..maxPID.
// Anything else is refused — in particular 0, 1, negative values (process
// groups, or -1 = every process) and values like 4294967295 that
// strconv.Atoi accepts but the kernel's 32-bit pid_t truncates to -1.
func ParsePID(s string) (int, error) {
	if s == "" || len(s) > 7 || strings.TrimLeft(s, "0123456789") != "" || s[0] == '0' {
		return 0, fmt.Errorf("invalid pid %q", s)
	}
	pid, err := strconv.Atoi(s)
	if err != nil || pid <= 1 || pid > maxPID {
		return 0, fmt.Errorf("invalid pid %q", s)
	}
	return pid, nil
}

// fs <op> <root> <path> [arg]
func fsOp(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) < 3 {
		return errors.New("usage: fs <op> <root> <path> [arg]")
	}
	op, rootPath, p := args[0], args[1], args[2]
	if !filepath.IsAbs(rootPath) {
		return errors.New("root must be absolute")
	}
	// Roots are host paths; in container mode they live below the host root
	// (symlinks resolved inside it).
	openPath, err := host.Resolve(rootPath)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(openPath)
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
	case "mkdir", "remove", "move", "copy", "chmod", "chown", "write", "read":
	default:
		return fmt.Errorf("unknown fs op %q", op)
	}
	// os.Root follows relative symlinks that stay inside it, and on the
	// System drive ("/") a link planted by another local user would point
	// root at any file. Every op runs on the pinned, symlink-free folder
	// holding p and touches only its last component.
	parent, base, err := fsops.PinParent(root, p)
	if err != nil {
		return err
	}
	defer parent.Close()
	switch op {
	case "mkdir":
		return parent.Mkdir(base, 0o755)
	case "remove":
		return parent.RemoveAll(base)
	case "move", "copy":
		return transfer(op, root, parent, p, base, arg)
	case "chmod":
		m, err := fsops.ParseMode(arg)
		if err != nil {
			return err
		}
		return fsops.ChmodNoFollow(parent, base, m)
	case "chown":
		uid, gid, err := fsops.ParseOwner(arg)
		if err != nil {
			return err
		}
		return parent.Lchown(base, uid, gid)
	case "write":
		data, err := io.ReadAll(io.LimitReader(stdin, 32<<20))
		if err != nil {
			return err
		}
		return writeReplace(parent, base, data)
	default: // "read"
		return readRegular(parent, base, stdout)
	}
}

// transfer moves or copies src (base inside the pinned srcParent) to dst,
// whose folder is pinned the same way.
func transfer(op string, root, srcParent *os.Root, src, srcBase, dst string) error {
	dst = fsops.Clean(dst)
	if fsops.Inside(dst, src) {
		return fsops.ErrIntoSelf
	}
	dstParent, dstBase, err := fsops.PinParent(root, dst)
	if err != nil {
		return err
	}
	defer dstParent.Close()
	if op == "move" {
		return fsops.MoveBetween(srcParent, srcBase, dstParent, dstBase)
	}
	_, err = fsops.CopyBetween(context.Background(), srcParent, srcBase, dstParent, dstBase, nil)
	return err
}

// readRegular copies the regular file base (a single name inside dir) to
// w. The open never blocks (a FIFO swapped in would otherwise hang the
// root helper), never follows a symlink, and what was opened must be a
// regular file (not a device, FIFO or socket).
func readRegular(dir *os.Root, base string, w io.Writer) error {
	f, err := fsops.OpenNoFollow(dir, base, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", base)
	}
	_, err = io.Copy(w, io.LimitReader(f, 32<<20))
	return err
}

// writeReplace atomically replaces p with data, as root. It never writes
// through the existing directory entry: a symlink or hard link planted at
// p (e.g. to /etc/passwd) would otherwise make root overwrite its target.
// Instead data goes to a fresh O_EXCL temp file in the same directory,
// which gets the original's mode and owner, is fsynced and renamed over
// p (rename replaces the entry, never follows it). A final-component
// symlink is refused outright.
func writeReplace(root *os.Root, p string, data []byte) error {
	mode := os.FileMode(0o644)
	uid, gid := -1, -1
	if st, err := root.Lstat(p); err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; edit its target instead", p)
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		mode = st.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(sys.Uid), int(sys.Gid)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir, base := filepath.Split(p)
	var tmp string
	var f *os.File
	for i := 0; ; i++ {
		var rnd [8]byte
		rand.Read(rnd[:])
		tmp = filepath.Join(dir, fmt.Sprintf(".%s.wargo-%x", base, rnd))
		var err error
		f, err = root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) || i >= 5 {
			return err
		}
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			root.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if uid >= 0 {
		if err := f.Chown(uid, gid); err != nil {
			return err
		}
	}
	// After chown (which clears setuid/setgid bits).
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmp, p); err != nil {
		return err
	}
	ok = true
	return nil
}

func power(args []string) error {
	if len(args) != 1 || (args[0] != "reboot" && args[0] != "poweroff") {
		return errors.New("usage: power reboot|poweroff")
	}
	return host.Command(nil, "systemctl", args[0]).Run()
}

// compose-update <working_dir> <project> <config_file>
// compose-update <dir> <project> <file> [file...]: compose projects may
// be split across several files (base + overrides); all are passed on.
func composeUpdate(args []string, stdout, stderr io.Writer) error {
	if len(args) < 3 || len(args) > 3+15 {
		return errors.New("usage: compose-update <dir> <project> <file> [file...]")
	}
	dir, project, files := args[0], args[1], args[2:]
	if !filepath.IsAbs(dir) {
		return errors.New("paths must be absolute")
	}
	var fileArgs []string
	for _, f := range files {
		if !filepath.IsAbs(f) || filepath.Clean(f) != f {
			return errors.New("paths must be absolute")
		}
		fileArgs = append(fileArgs, "-f", f)
	}
	// The project is passed as the value of -p: it must not look like an
	// option (or carry anything else docker would parse).
	if !composeProjectRe.MatchString(project) {
		return fmt.Errorf("invalid compose project name %q", project)
	}
	base := append([]string{"compose", "-p", project}, fileArgs...)
	for _, step := range [][]string{
		append(append([]string{}, base...), "pull"),
		append(append([]string{}, base...), "up", "-d", "--remove-orphans"),
	} {
		// Native: plain exec in dir. Container: the host's docker CLI, as
		// root, in the host's namespaces.
		cmd := host.CommandOpts(nil, host.Opts{Dir: dir}, append([]string{"docker"}, step...)...)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker %v: %w", step, err)
		}
	}
	return nil
}

var composeProjectRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// PkgExitMarker is printed after a package-manager run so the caller can
// read the exit status from the log even after systemd collects the unit.
const PkgExitMarker = "[wargo] exit status "

// PackageManagers may be launched by pkg-run.
var PackageManagers = []string{"pacman", "pamac", "apt-get", "apt", "dnf", "yum", "zypper", "apk", "flatpak"}

var systemBinDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/usr/local/bin"}

// ResolvePackageManager returns the absolute path of an allowed package
// manager binary, or an error.
func ResolvePackageManager(name string) (string, error) {
	lit, _, err := resolvePackageManager(name)
	return lit, err
}

// resolvePackageManager also returns the fully resolved host path, which
// is what root must execute so a later swap of a link cannot redirect it.
func resolvePackageManager(name string) (literal, resolved string, err error) {
	if !filepath.IsAbs(name) && strings.Contains(name, "/") {
		return "", "", fmt.Errorf("%q must be a bare name or absolute path", name)
	}
	base := filepath.Base(name)
	allowed := false
	for _, pm := range PackageManagers {
		if pm == base {
			allowed = true
		}
	}
	if !allowed {
		return "", "", fmt.Errorf("%q is not a supported package manager", name)
	}
	p := name
	if !filepath.IsAbs(p) {
		if p, err = host.LookPath(base); err != nil {
			return "", "", err
		}
	}
	p = filepath.Clean(p)
	for _, d := range systemBinDirs {
		if filepath.Dir(p) == d {
			r, err := checkRootTrusted(p)
			if err != nil {
				return "", "", err
			}
			return p, r, nil
		}
	}
	return "", "", fmt.Errorf("%q is not in a system directory", p)
}

// requireRootOwner enforces ownership checks on package manager binaries;
// true when running as root. rootUID is the uid that counts as root. Tests
// override both.
var (
	requireRootOwner = os.Geteuid() == 0
	rootUID          = uint32(0)
)

// checkRootTrusted refuses a binary, or any directory above it (up to the
// host root), that is not root-owned or is group/world-writable, since
// root is about to run it. p is a host path: in container mode it is
// looked at below the host root, never in the container's own filesystem.
// Both the literal path (every component, so a symlink must sit in a
// trusted directory) and the fully resolved path are checked. It returns
// the resolved host path, which is what must be executed.
func checkRootTrusted(p string) (string, error) {
	resolved := p
	if host.Container() {
		rp, err := host.Resolve(p)
		if err != nil {
			return "", err
		}
		hp, ok := host.Unpath(rp)
		if !ok {
			return "", fmt.Errorf("%s resolves outside the host", p)
		}
		resolved = hp
	} else {
		var err error
		if resolved, err = filepath.EvalSymlinks(p); err != nil {
			return "", err
		}
	}
	if !requireRootOwner {
		return resolved, nil
	}
	if err := trustWalk(p); err != nil {
		return "", err
	}
	if err := trustWalk(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// trustWalk checks start and every ancestor (without following links, so a
// symlink component is judged by its own owner and its directory). In
// container mode the walk ends below the host root: the mount point belongs
// to the container.
func trustWalk(start string) error {
	for d := start; ; d = filepath.Dir(d) {
		if d == "/" && host.Container() {
			return nil
		}
		st, err := os.Lstat(host.Path(d))
		if err != nil {
			return err
		}
		// A symlink's own mode bits are meaningless; its owner and the
		// directory holding it are what matter.
		if st.Mode()&os.ModeSymlink == 0 && st.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s is writable by group or others", d)
		}
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Uid != rootUID {
			return fmt.Errorf("%s is not owned by root", d)
		}
		if d == "/" {
			return nil
		}
	}
}

// pkg-run -- <package manager> [args...]: runs the package manager and
// prints PkgExitMarker with its exit status.
func pkgRun(args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) < 2 || args[0] != "--" {
		return 1, errors.New("usage: pkg-run -- <package-manager> [args...]")
	}
	lit, bin, err := resolvePackageManager(args[1])
	if err != nil {
		return 1, err
	}
	cmd := host.Command(nil, append([]string{bin}, args[2:]...)...)
	if !host.Container() {
		cmd.Args[0] = lit // programs like dnf look at argv[0]
	}
	if host.Container() {
		cmd.Env = append(cmd.Env, "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C")
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			fmt.Fprintln(stderr, "wargo helper:", err)
			code = 127
		}
	}
	fmt.Fprintf(stdout, "\n%s%d\n", PkgExitMarker, code)
	return code, nil
}

// PrivdCredentialsPath is where wargo-privd keeps trusted passkeys.
const PrivdCredentialsPath = "/var/lib/wargo-privd/credentials.json"

// privd-trust: stdin is the JSON credential store for wargo-privd.
func privdTrust(args []string, stdin io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return err
	}
	if err := validTrustStore(data); err != nil {
		return err
	}
	dir := filepath.Dir(PrivdCredentialsPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// The directory must be root's alone: anyone able to write it could
	// plant their own passkey for root approvals.
	if err := os.Chown(dir, 0, 0); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), PrivdCredentialsPath)
}

// validTrustStore checks the shape of a wargo-privd credential store
// (mirrors privd.CredentialStore) so that garbage is never installed.
func validTrustStore(data []byte) error {
	var cs struct {
		UserHandle  string            `json:"userHandle"`
		RPIDs       []string          `json:"rpIds"`
		Credentials []json.RawMessage `json:"credentials"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cs); err != nil {
		return fmt.Errorf("invalid credential store: %w", err)
	}
	if dec.More() {
		return errors.New("invalid credential store: trailing data")
	}
	if h, err := base64.RawURLEncoding.DecodeString(cs.UserHandle); err != nil || len(h) == 0 || len(h) > 64 {
		return errors.New("invalid credential store: bad user handle")
	}
	if len(cs.Credentials) == 0 || len(cs.Credentials) > 32 {
		return errors.New("invalid credential store: expected 1-32 credentials")
	}
	if len(cs.RPIDs) == 0 || len(cs.RPIDs) > 32 {
		return errors.New("invalid credential store: expected 1-32 RP IDs")
	}
	for _, id := range cs.RPIDs {
		if id == "" || len(id) > 253 || strings.ContainsAny(id, "/:@ \t\r\n") {
			return fmt.Errorf("invalid credential store: bad RP ID %q", id)
		}
	}
	for _, c := range cs.Credentials {
		var cred struct {
			ID        []byte `json:"id"`
			PublicKey []byte `json:"publicKey"`
		}
		if err := json.Unmarshal(c, &cred); err != nil || len(cred.ID) == 0 || len(cred.PublicKey) == 0 {
			return errors.New("invalid credential store: credential without id or public key")
		}
	}
	return nil
}
