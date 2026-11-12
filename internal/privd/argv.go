// SPDX-License-Identifier: AGPL-3.0-only

package privd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

var (
	unitRe = regexp.MustCompile(`^--unit=wargo-upgrade-[A-Za-z0-9_.-]{1,64}$`)

	// systemd-run flags that cannot change what runs or where it writes.
	plainRunFlags = []string{"--collect", "-G", "--quiet", "-q", "--wait", "--pipe", "-P", "--no-block", "--send-sighup"}

	// Unit properties that may be set with --property=KEY=VALUE.
	allowedProps = map[string]func(string) bool{
		"Type":                 oneOf("exec", "simple"),
		"StandardOutput":       logTarget,
		"StandardError":        logTarget,
		"KillMode":             oneOf("control-group", "mixed", "process"),
		"TimeoutStopSec":       numeric,
		"Nice":                 numeric,
		"CPUWeight":            numeric,
		"IOWeight":             numeric,
		"IOSchedulingClass":    oneOf("idle", "best-effort", "realtime", "2", "3"),
		"IOSchedulingPriority": numeric,
		"LogLevelMax":          oneOf("info", "notice", "warning", "debug"),
	}

	// Environment variables that may be set with --setenv=KEY=VALUE.
	allowedEnv = []string{"DEBIAN_FRONTEND", "LANG", "LC_ALL", "TERM", "NEEDRESTART_MODE", "APT_LISTCHANGES_FRONTEND"}

	// Package managers that may be launched in a wargo-upgrade unit.
	packageManagers = []string{"pacman", "pamac", "apt-get", "apt", "dnf", "yum", "zypper", "apk", "flatpak"}

	// Directories accepted for absolute package-manager paths.
	systemBinDirs = []string{"/usr/bin", "/usr/sbin", "/bin", "/sbin", "/usr/local/bin", "/usr/local/sbin"}

	// Directories where upgrade unit output may be written.
	logDirs = []string{"/var/log/wargo/", "/var/lib/wargo/", "/run/wargo/"}
)

func oneOf(vals ...string) func(string) bool {
	return func(v string) bool { return slices.Contains(vals, v) }
}

func numeric(v string) bool {
	if v == "" || len(v) > 12 {
		return false
	}
	for i, r := range v {
		if !(r >= '0' && r <= '9') && !(i == 0 && r == '-') && r != 's' {
			return false
		}
	}
	return true
}

func logTarget(v string) bool {
	switch v {
	case "journal", "null", "inherit", "journal+console":
		return true
	}
	for _, pfx := range []string{"file:", "append:", "truncate:"} {
		if p, ok := strings.CutPrefix(v, pfx); ok {
			if !filepath.IsAbs(p) || filepath.Clean(p) != p {
				return false
			}
			for _, d := range logDirs {
				if strings.HasPrefix(p, d) {
					return true
				}
			}
			return false
		}
	}
	return false
}

// checkArgv validates argv against the allow-list and returns the argv to
// execute (argv[0] is always replaced by a configured absolute path, so no
// PATH lookup ever happens in privd itself).
func (c *Config) checkArgv(argv []string) ([]string, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	for _, a := range argv {
		if strings.ContainsRune(a, 0) || len(a) > 4096 {
			return nil, errors.New("invalid argument")
		}
	}
	if len(argv) > 256 {
		return nil, errors.New("too many arguments")
	}
	switch {
	case argv[0] == c.WargoBinary:
		if len(argv) < 3 || argv[1] != "helper" {
			return nil, errors.New("only `wargo helper <op>` may be run")
		}
		return slices.Clone(argv), nil
	case argv[0] == c.Systemctl || argv[0] == "systemctl":
		if len(argv) != 2 || (argv[1] != "reboot" && argv[1] != "poweroff") {
			return nil, errors.New("only `systemctl reboot|poweroff` may be run")
		}
		return []string{c.Systemctl, argv[1]}, nil
	case argv[0] == c.SystemdRun || argv[0] == "systemd-run":
		if err := c.checkSystemdRun(argv[1:]); err != nil {
			return nil, err
		}
		return append([]string{c.SystemdRun}, argv[1:]...), nil
	}
	return nil, fmt.Errorf("command %q is not allowed", argv[0])
}

func (c *Config) checkSystemdRun(args []string) error {
	if len(args) == 0 || !unitRe.MatchString(args[0]) {
		return errors.New("systemd-run must start with --unit=wargo-upgrade-<id>")
	}
	i := 1
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") {
			break
		}
		if slices.Contains(plainRunFlags, a) {
			continue
		}
		if kv, ok := strings.CutPrefix(a, "--property="); ok {
			k, v, _ := strings.Cut(kv, "=")
			if check, ok := allowedProps[k]; !ok || !check(v) {
				return fmt.Errorf("systemd-run property %q is not allowed", kv)
			}
			continue
		}
		if kv, ok := strings.CutPrefix(a, "--setenv="); ok {
			k, _, _ := strings.Cut(kv, "=")
			if !slices.Contains(allowedEnv, k) {
				return fmt.Errorf("environment variable %q is not allowed", k)
			}
			continue
		}
		if d, ok := strings.CutPrefix(a, "--description="); ok && len(d) < 200 {
			continue
		}
		return fmt.Errorf("systemd-run option %q is not allowed", a)
	}
	if i >= len(args) {
		return errors.New("systemd-run: missing command")
	}
	cmd := args[i]
	if cmd == c.WargoBinary {
		if len(args) < i+3 || args[i+1] != "helper" {
			return errors.New("only `wargo helper <op>` may be run")
		}
		// systemd-run will exec this binary as root: it must not be
		// replaceable by the Wargo user.
		if err := checkTrustedFile(cmd); err != nil {
			return fmt.Errorf("wargo binary is not trusted: %w", err)
		}
		return nil
	}
	// Bare names are refused: systemd would search its own PATH, which can
	// put a user-writable /usr/local/bin ahead of the directory checked here.
	if !filepath.IsAbs(cmd) {
		return fmt.Errorf("package manager %q must be an absolute path", cmd)
	}
	if filepath.Clean(cmd) != cmd || !slices.Contains(systemBinDirs, filepath.Dir(cmd)) {
		return fmt.Errorf("command %q is not in a system directory", cmd)
	}
	if !slices.Contains(packageManagers, filepath.Base(cmd)) {
		return fmt.Errorf("systemd-run may only launch a package manager, not %q", cmd)
	}
	// root runs this binary: it must not be replaceable by the Wargo user.
	if err := checkTrustedFile(cmd); err != nil {
		return fmt.Errorf("package manager is not trusted: %w", err)
	}
	return nil
}

// checkTrustedFile refuses to execute binaries that someone other than
// root could have replaced (ownership only enforced when privd runs as
// root): the file and every parent directory must be root-owned and not
// group/world-writable (a writable /usr/local/bin would allow swapping the
// binary by rename).
func checkTrustedFile(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if err := checkOwner(path, st); err != nil {
		return err
	}
	return checkParents(path)
}

// checkTrustedPath checks an open file f (at path) and its parents.
func checkTrustedPath(path string, f *os.File) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if err := checkOwner(path, st); err != nil {
		return err
	}
	return checkParents(path)
}

// requireRootOwner is true when privd runs as root, which is when file
// ownership is enforced. Tests override it to exercise the root-owned
// checks without root.
var requireRootOwner = os.Geteuid() == 0

// rootUID is the uid that counts as root, and trustStop (when set) ends
// the ancestor walks. Tests override both.
var (
	rootUID   = uint32(0)
	trustStop = ""
)

func checkOwner(path string, st os.FileInfo) error {
	if st.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others", path)
	}
	if requireRootOwner {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Uid != rootUID {
			return fmt.Errorf("%s is not owned by root", path)
		}
	}
	return nil
}

// checkParents walks the ancestors of the literal path (a symlink
// component must itself be root-owned and sit in a trusted directory) and
// then those of the resolved path.
func checkParents(path string) error {
	if !requireRootOwner {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if err := walkTrusted(path, true); err != nil {
		return err
	}
	return walkTrusted(resolved, false)
}

// walkTrusted checks the ancestors of start without following links. With
// withSelf, start itself is also checked when it is a symlink (a regular
// file is checked by the caller).
func walkTrusted(start string, withSelf bool) error {
	for d := start; ; d = filepath.Dir(d) {
		if d == trustStop {
			return nil
		}
		if d != start || withSelf {
			st, err := os.Lstat(d)
			if err != nil {
				return err
			}
			switch {
			case st.Mode()&os.ModeSymlink != 0:
				// Mode bits of a link are meaningless; only the owner counts.
				if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Uid != rootUID {
					return fmt.Errorf("%s is not owned by root", d)
				}
			case d != start:
				// Sticky world-writable dirs (/tmp) still allow others to
				// plant files, so they are refused as well.
				if err := checkOwner(d, st); err != nil {
					return err
				}
			}
		}
		if d == "/" {
			return nil
		}
	}
}
