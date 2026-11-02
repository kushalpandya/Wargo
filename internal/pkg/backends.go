// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cmdErr(name string, code int, stderr, stdout []byte) error {
	msg := lastLine(stderr)
	if msg == "" {
		msg = lastLine(stdout)
	}
	if msg == "" {
		return fmt.Errorf("%s exited with status %d", name, code)
	}
	return fmt.Errorf("%s exited with status %d: %s", name, code, msg)
}

// --- Arch family --------------------------------------------------------

type archCommon struct{}

func (archCommon) UpgradeArgv() []string {
	return []string{"pacman", "-Syu", "--noconfirm", "--noprogressbar", "--color", "never"}
}
func (archCommon) Env() []string                                { return nil }
func (archCommon) ParseProgress(line string, st *ProgressState) { parsePacman(line, st) }
func (archCommon) LockFiles() []string                          { return []string{"/var/lib/pacman/db.lck"} }
func (archCommon) ConfigSuffixes() []string                     { return []string{".pacnew", ".pacsave"} }
func (archCommon) RebootRequired(ctx context.Context, run Runner) (bool, string) {
	return kernelModulesMissing()
}

// enrichPacman fills repo and download size from the sync databases for
// updates whose version matches (`pacman -Sp` takes no lock).
func enrichPacman(ctx context.Context, run Runner, ups []Update, dbpath string) {
	if len(ups) == 0 {
		return
	}
	argv := []string{"pacman", "-Sp", "--print-format", "%n %v %r %s"}
	if dbpath != "" {
		argv = append(argv, "--dbpath", dbpath)
	}
	for _, u := range ups {
		argv = append(argv, u.Name)
	}
	out, _, code, err := run(ctx, argv...)
	if err != nil || code != 0 {
		return
	}
	info := ParsePacmanPrint(out)
	for i := range ups {
		t, ok := info[ups[i].Name]
		if !ok {
			continue
		}
		if ups[i].Repo == "" {
			ups[i].Repo = t.Repo
		}
		if t.Version == ups[i].New {
			ups[i].Size = t.Size
		}
	}
}

type pamacBackend struct{ archCommon }

func (*pamacBackend) Name() string          { return "pamac" }
func (*pamacBackend) Binary() string        { return "pamac" }
func (*pamacBackend) CheckNeedsRoot() bool  { return false }
func (*pamacBackend) RefreshArgv() []string { return nil }

// Check runs `pamac checkupdates --aur`. Exit code 100 = updates
// available, 0 = up to date.
func (*pamacBackend) Check(ctx context.Context, run Runner) ([]Update, []Update, error) {
	out, stderr, code, err := run(ctx, "pamac", "checkupdates", "--aur")
	if err != nil {
		return nil, nil, err
	}
	if code != 0 && code != 100 {
		return nil, nil, cmdErr("pamac checkupdates", code, stderr, out)
	}
	ups, extras := ParsePamacCheck(out)
	enrichPacman(ctx, run, ups, "")
	return ups, extras, nil
}

type pacmanBackend struct {
	archCommon
	checkupdates bool
	aurHelper    string
}

func (*pacmanBackend) Name() string           { return "pacman" }
func (*pacmanBackend) Binary() string         { return "pacman" }
func (b *pacmanBackend) CheckNeedsRoot() bool { return !b.checkupdates }
func (b *pacmanBackend) RefreshArgv() []string {
	if b.checkupdates {
		return nil
	}
	return []string{"pacman", "-Sy", "--noprogressbar", "--color", "never"}
}

func (b *pacmanBackend) Check(ctx context.Context, run Runner) ([]Update, []Update, error) {
	var ups []Update
	if b.checkupdates {
		// checkupdates syncs a private copy of the databases; exit 2 = none.
		db := filepath.Join(os.TempDir(), fmt.Sprintf("wargo-checkup-db-%d", os.Getuid()))
		out, stderr, code, err := run(ctx, "env", "CHECKUPDATES_DB="+db, "checkupdates", "--nocolor")
		if err != nil {
			return nil, nil, err
		}
		if code != 0 && code != 2 {
			return nil, nil, cmdErr("checkupdates", code, stderr, out)
		}
		ups = ParseArrowList(out, "repo")
		enrichPacman(ctx, run, ups, db)
	} else {
		out, stderr, code, err := run(ctx, "pacman", "-Qu", "--color", "never")
		if err != nil {
			return nil, nil, err
		}
		// pacman -Qu exits 1 when there is nothing to upgrade.
		if code != 0 && !(code == 1 && len(strings.TrimSpace(string(out))) == 0) {
			return nil, nil, cmdErr("pacman -Qu", code, stderr, out)
		}
		ups = ParseArrowList(out, "repo")
		enrichPacman(ctx, run, ups, "")
	}
	var extras []Update
	if b.aurHelper != "" {
		// Read-only AUR listing; failures here are not fatal.
		if out, _, _, err := run(ctx, b.aurHelper, "-Qua", "--color", "never"); err == nil {
			extras = ParseArrowList(out, "aur")
			for i := range extras {
				extras[i].Repo = "AUR"
			}
		}
	}
	return ups, extras, nil
}

// --- Debian family -------------------------------------------------------

type aptBackend struct{}

func (*aptBackend) Name() string          { return "apt" }
func (*aptBackend) Binary() string        { return "apt-get" }
func (*aptBackend) CheckNeedsRoot() bool  { return true }
func (*aptBackend) RefreshArgv() []string { return []string{"apt-get", "-q", "update"} }
func (*aptBackend) Check(ctx context.Context, run Runner) ([]Update, []Update, error) {
	out, stderr, code, err := run(ctx, "apt-get", "-s", "-o", "Debug::NoLocking=1", "full-upgrade")
	if err != nil {
		return nil, nil, err
	}
	if code != 0 {
		// Fall back to apt list, which needs no lock either.
		out2, _, code2, err2 := run(ctx, "apt", "list", "--upgradable")
		if err2 == nil && code2 == 0 {
			return ParseAptList(out2), nil, nil
		}
		return nil, nil, cmdErr("apt-get -s full-upgrade", code, stderr, out)
	}
	return ParseAptSimulate(out), nil, nil
}
func (*aptBackend) UpgradeArgv() []string {
	return []string{"apt-get", "-y", "-q",
		"-o", "APT::Status-Fd=1",
		"-o", "Dpkg::Options::=--force-confdef",
		"-o", "Dpkg::Options::=--force-confold",
		"-o", "Dpkg::Use-Pty=0",
		"full-upgrade"}
}
func (*aptBackend) Env() []string {
	return []string{"DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=l"}
}
func (*aptBackend) ParseProgress(line string, st *ProgressState) { parseApt(line, st) }
func (*aptBackend) LockFiles() []string {
	return []string{"/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock", "/var/lib/apt/lists/lock", "/var/cache/apt/archives/lock"}
}
func (*aptBackend) ConfigSuffixes() []string {
	return []string{".dpkg-dist", ".dpkg-new", ".dpkg-old", ".ucf-dist"}
}
func (*aptBackend) RebootRequired(ctx context.Context, run Runner) (bool, string) {
	if _, err := os.Stat("/var/run/reboot-required"); err == nil {
		reason := "Updated packages require a reboot"
		if b, err := os.ReadFile("/var/run/reboot-required.pkgs"); err == nil {
			if pk := strings.Fields(string(b)); len(pk) > 0 {
				reason += ": " + strings.Join(uniq(pk), ", ")
			}
		}
		return true, reason
	}
	return false, ""
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// --- Fedora family -------------------------------------------------------

type dnfBackend struct{}

func (*dnfBackend) Name() string          { return "dnf" }
func (*dnfBackend) Binary() string        { return "dnf" }
func (*dnfBackend) CheckNeedsRoot() bool  { return false }
func (*dnfBackend) RefreshArgv() []string { return nil }
func (*dnfBackend) Check(ctx context.Context, run Runner) ([]Update, []Update, error) {
	out, stderr, code, err := run(ctx, "dnf", "check-update", "-q")
	if err != nil {
		return nil, nil, err
	}
	if code != 0 && code != 100 {
		return nil, nil, cmdErr("dnf check-update", code, stderr, out)
	}
	ups := ParseDnfCheckUpdate(out)
	if len(ups) > 0 {
		if q, _, c, err := run(ctx, "rpm", "-qa", "--qf", "%{NAME}.%{ARCH} %{VERSION}-%{RELEASE}\n"); err == nil && c == 0 {
			cur := ParseRpmQuery(q)
			for i := range ups {
				ups[i].Current = cur[ups[i].Name]
			}
		}
	}
	return ups, nil, nil
}
func (*dnfBackend) UpgradeArgv() []string                        { return []string{"dnf", "-y", "--color=never", "upgrade"} }
func (*dnfBackend) Env() []string                                { return nil }
func (*dnfBackend) ParseProgress(line string, st *ProgressState) { parseDnf(line, st) }
func (*dnfBackend) LockFiles() []string                          { return []string{"/var/run/dnf.pid", "/run/dnf.pid"} }
func (*dnfBackend) ConfigSuffixes() []string                     { return []string{".rpmnew", ".rpmsave"} }
func (*dnfBackend) RebootRequired(ctx context.Context, run Runner) (bool, string) {
	for _, argv := range [][]string{{"needs-restarting", "-r"}, {"dnf", "needs-restarting", "-r"}} {
		if !hasBin(argv[0]) {
			continue
		}
		out, _, code, err := run(ctx, argv...)
		if err != nil {
			continue
		}
		if code == 1 {
			return true, firstLine(out, "Core libraries or services have been updated")
		}
		if code == 0 {
			return false, ""
		}
	}
	return kernelModulesMissing()
}

func firstLine(b []byte, def string) string {
	for _, l := range lines(b) {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return def
}

// --- openSUSE ------------------------------------------------------------

type zypperBackend struct{ tumbleweed bool }

func (*zypperBackend) Name() string         { return "zypper" }
func (*zypperBackend) Binary() string       { return "zypper" }
func (*zypperBackend) CheckNeedsRoot() bool { return true }
func (*zypperBackend) RefreshArgv() []string {
	return []string{"zypper", "--non-interactive", "refresh"}
}
func (*zypperBackend) Check(ctx context.Context, run Runner) ([]Update, []Update, error) {
	out, stderr, code, err := run(ctx, "zypper", "--xmlout", "-q", "--no-refresh", "list-updates")
	if err != nil {
		return nil, nil, err
	}
	if code != 0 && code != 100 && code != 101 {
		return nil, nil, cmdErr("zypper list-updates", code, stderr, out)
	}
	ups, err := ParseZypperXML(out)
	return ups, nil, err
}
func (z *zypperBackend) UpgradeArgv() []string {
	cmd := "update"
	if z.tumbleweed {
		cmd = "dist-upgrade"
	}
	return []string{"zypper", "--non-interactive", "--xmlout", cmd, "--auto-agree-with-licenses"}
}
func (*zypperBackend) Env() []string                                { return nil }
func (*zypperBackend) ParseProgress(line string, st *ProgressState) { parseZypper(line, st) }
func (*zypperBackend) LockFiles() []string                          { return []string{"/run/zypp.pid", "/var/run/zypp.pid"} }
func (*zypperBackend) ConfigSuffixes() []string                     { return []string{".rpmnew", ".rpmsave"} }
func (*zypperBackend) RebootRequired(ctx context.Context, run Runner) (bool, string) {
	for _, p := range []string{"/run/reboot-needed", "/var/run/reboot-needed"} {
		if _, err := os.Stat(p); err == nil {
			return true, "zypper reports that a reboot is needed"
		}
	}
	return kernelModulesMissing()
}

// --- Alpine --------------------------------------------------------------

type apkBackend struct{}

func (*apkBackend) Name() string          { return "apk" }
func (*apkBackend) Binary() string        { return "apk" }
func (*apkBackend) CheckNeedsRoot() bool  { return true }
func (*apkBackend) RefreshArgv() []string { return []string{"apk", "update", "--no-progress"} }
func (*apkBackend) Check(ctx context.Context, run Runner) ([]Update, []Update, error) {
	out, stderr, code, err := run(ctx, "apk", "version", "-l", "<")
	if err != nil {
		return nil, nil, err
	}
	if code != 0 {
		return nil, nil, cmdErr("apk version", code, stderr, out)
	}
	return ParseApkVersion(out), nil, nil
}
func (*apkBackend) UpgradeArgv() []string                        { return []string{"apk", "upgrade", "--no-progress"} }
func (*apkBackend) Env() []string                                { return nil }
func (*apkBackend) ParseProgress(line string, st *ProgressState) { parseApk(line, st) }
func (*apkBackend) LockFiles() []string                          { return []string{"/lib/apk/db/lock"} }
func (*apkBackend) ConfigSuffixes() []string                     { return []string{".apk-new"} }
func (*apkBackend) RebootRequired(ctx context.Context, run Runner) (bool, string) {
	return kernelModulesMissing()
}

// --- Flatpak (read-only extras) -------------------------------------------

func checkFlatpak(ctx context.Context, run Runner) []Update {
	if !hasBin("flatpak") {
		return nil
	}
	out, _, code, err := run(ctx, "flatpak", "remote-ls", "--updates", "--columns=application,version,origin")
	if err != nil || code != 0 {
		return nil
	}
	cur := map[string]string{}
	if l, _, c, err := run(ctx, "flatpak", "list", "--columns=application,version"); err == nil && c == 0 {
		cur = ParseFlatpakList(l)
	}
	return ParseFlatpakUpdates(out, cur)
}
