// SPDX-License-Identifier: AGPL-3.0-only

// Package pkg checks for and installs OS package updates. Nothing here runs
// automatically: checks and upgrades happen only on explicit user action.
// Listing updates runs unprivileged wherever the package manager allows it;
// database refreshes and upgrades go through the elevation service and run
// as transient systemd units so they survive Wargo restarts.
package pkg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Update is one pending package update.
type Update struct {
	Name    string `json:"name"`
	Current string `json:"current"`
	New     string `json:"new"`
	Repo    string `json:"repo"`
	// Size is the download size in bytes when known, else 0.
	Size int64 `json:"size"`
	// Source is "repo", "aur" or "flatpak". AUR and Flatpak are read-only.
	Source string `json:"source"`
}

// Runner executes argv unprivileged and returns stdout and the exit code.
// err is set only when the command could not run at all (not found,
// timeout); a non-zero exit is reported through code.
type Runner func(ctx context.Context, argv ...string) (stdout []byte, stderr []byte, code int, err error)

// ProgressState is the parsed state of an upgrade.
type ProgressState struct {
	Stage   string  `json:"stage"`
	Frac    float64 `json:"frac"`
	Current string  `json:"current"`

	// Parser bookkeeping.
	phase   string
	total   int // packages in the transaction
	done    int // packages processed
	dlTotal int
	dlDone  int
	// Upgraded counts "upgrading/installing/…" events.
	Upgraded int `json:"upgraded"`
}

// Stage names shown in the UI stepper.
const (
	StagePreparing   = "Preparing"
	StageDownloading = "Downloading"
	StageInstalling  = "Installing"
	StageHooks       = "Post-install hooks"
	StageDone        = "Done"
)

// Backend adapts one package manager.
type Backend interface {
	// Name is "pacman", "pamac", "apt", "dnf", "zypper" or "apk".
	Name() string
	// Binary is the executable that must be present for the backend to work.
	Binary() string
	// CheckNeedsRoot reports whether listing updates requires an elevated
	// database refresh first (RefreshArgv).
	CheckNeedsRoot() bool
	// RefreshArgv is run as root before Check when CheckNeedsRoot is true.
	RefreshArgv() []string
	// Check lists pending updates (unprivileged). extras are read-only
	// (e.g. AUR) and never installed by Wargo.
	Check(ctx context.Context, run Runner) (updates, extras []Update, err error)
	// UpgradeArgv is the command run as root inside systemd-run.
	UpgradeArgv() []string
	// Env is extra environment for the upgrade unit.
	Env() []string
	// ParseProgress folds one output line into st.
	ParseProgress(line string, st *ProgressState)
	// LockFiles are checked before upgrading.
	LockFiles() []string
	// ConfigSuffixes are new-config-file suffixes to look for under /etc.
	ConfigSuffixes() []string
	// RebootRequired reports whether the host should be rebooted.
	RebootRequired(ctx context.Context, run Runner) (bool, string)
}

// OSRelease holds the fields of /etc/os-release that matter here.
type OSRelease struct {
	ID         string
	IDLike     []string
	PrettyName string
	VersionID  string
}

// ParseOSRelease parses os-release(5) content.
func ParseOSRelease(b []byte) OSRelease {
	var o OSRelease
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			o.ID = strings.ToLower(v)
		case "ID_LIKE":
			o.IDLike = strings.Fields(strings.ToLower(v))
		case "PRETTY_NAME":
			o.PrettyName = v
		case "VERSION_ID":
			o.VersionID = v
		}
	}
	return o
}

func readOSRelease() OSRelease {
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		if b, err := os.ReadFile(p); err == nil {
			return ParseOSRelease(b)
		}
	}
	return OSRelease{}
}

// family maps an os-release to a package-manager family.
func family(o OSRelease) string {
	ids := append([]string{o.ID}, o.IDLike...)
	for _, id := range ids {
		switch {
		case id == "manjaro" || strings.HasPrefix(id, "manjaro"):
			return "manjaro"
		}
	}
	for _, id := range ids {
		switch id {
		case "arch", "archlinux", "endeavouros", "garuda", "artix", "cachyos":
			return "arch"
		case "debian", "ubuntu", "raspbian", "linuxmint", "pop", "elementary", "kali", "zorin", "neon":
			return "debian"
		case "fedora", "rhel", "centos", "rocky", "almalinux", "ol", "nobara":
			return "fedora"
		case "alpine", "postmarketos":
			return "alpine"
		}
		if strings.HasPrefix(id, "opensuse") || id == "suse" || id == "sles" || id == "sled" {
			return "suse"
		}
	}
	return ""
}

type lookFn func(string) bool

func hasBin(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Detect picks the backend for this host. look reports binary presence.
func Detect(o OSRelease, look lookFn) Backend {
	switch family(o) {
	case "manjaro":
		if look("pamac") {
			return &pamacBackend{}
		}
		return &pacmanBackend{checkupdates: look("checkupdates"), aurHelper: aurHelper(look)}
	case "arch":
		return &pacmanBackend{checkupdates: look("checkupdates"), aurHelper: aurHelper(look)}
	case "debian":
		return &aptBackend{}
	case "fedora":
		return &dnfBackend{}
	case "suse":
		return &zypperBackend{tumbleweed: strings.Contains(o.ID, "tumbleweed") || strings.Contains(o.ID, "slowroll")}
	case "alpine":
		return &apkBackend{}
	}
	// Unknown distro: fall back to whatever is installed.
	switch {
	case look("pamac"):
		return &pamacBackend{}
	case look("pacman"):
		return &pacmanBackend{checkupdates: look("checkupdates"), aurHelper: aurHelper(look)}
	case look("apt-get"):
		return &aptBackend{}
	case look("dnf"):
		return &dnfBackend{}
	case look("zypper"):
		return &zypperBackend{}
	case look("apk"):
		return &apkBackend{}
	}
	return nil
}

func aurHelper(look lookFn) string {
	for _, h := range []string{"yay", "paru"} {
		if look(h) {
			return h
		}
	}
	return ""
}

// maxOutput caps captured command output.
const maxOutput = 8 << 20

type capped struct {
	buf bytes.Buffer
	n   int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.n - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

// ExecRunner runs commands with LC_ALL=C and a timeout.
func ExecRunner(timeout time.Duration) Runner {
	return func(ctx context.Context, argv ...string) ([]byte, []byte, int, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "NO_COLOR=1", "TERM=dumb")
		cmd.Stdin = nil
		out, errb := &capped{n: maxOutput}, &capped{n: 64 << 10}
		cmd.Stdout, cmd.Stderr = out, errb
		cmd.WaitDelay = 5 * time.Second
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) && ctx.Err() == nil {
			return out.buf.Bytes(), errb.buf.Bytes(), ee.ExitCode(), nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return out.buf.Bytes(), errb.buf.Bytes(), -1, errors.New(argv[0] + ": timed out")
			}
			return out.buf.Bytes(), errb.buf.Bytes(), -1, err
		}
		return out.buf.Bytes(), errb.buf.Bytes(), 0, nil
	}
}

// lastLine returns the last meaningful line of command output, skipping
// known noise (pamac's polkit agent warning).
func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || strings.Contains(l, "textual authentication agent") {
			continue
		}
		return l
	}
	return ""
}
