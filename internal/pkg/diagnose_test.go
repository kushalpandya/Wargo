// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func split(s string) []string { return strings.Split(strings.TrimPrefix(s, "\n"), "\n") }

// A real failed upgrade on Manjaro (Plasma 6.8 dropped the X11 session).
func TestDiagnosePacmanConflict(t *testing.T) {
	b, err := os.ReadFile("testdata/pacman-conflict.txt")
	if err != nil {
		t.Fatal(err)
	}
	d := diagnosePacman(split(string(b)))
	if d == nil || d.Code != CodeConflict || d.Title != "Package conflict" {
		t.Fatalf("diagnosis: %+v", d)
	}
	want := []Conflict{{Package: "plasma-workspace", Version: "6.8.2-1", Remove: "plasma-x11-session", RemoveVersion: "6.7.5-1"}}
	if !slices.Equal(d.Conflicts, want) {
		t.Fatalf("conflicts: %+v", d.Conflicts)
	}
	if d.Summary != "The update to plasma-workspace 6.8.2-1 conflicts with plasma-x11-session 6.7.5-1, which has to be removed first. "+
		"The package manager won't remove it without your permission, so nothing was changed." {
		t.Fatalf("summary: %q", d.Summary)
	}
	wantLines := []string{
		":: plasma-workspace-6.8.2-1 and plasma-x11-session-6.7.5-1 are in conflict.",
		"error: unresolvable package conflicts detected",
		"error: failed to prepare transaction (conflicting dependencies)",
		":: plasma-workspace-6.8.2-1 and plasma-x11-session-6.7.5-1 are in conflict",
	}
	if !slices.Equal(d.Lines, wantLines) {
		t.Fatalf("lines:\n%s", strings.Join(d.Lines, "\n"))
	}
}

func TestDiagnosePacmanConflictOtherSide(t *testing.T) {
	// The installed package can be named first; pacman asks to remove it.
	d := diagnosePacman([]string{":: iptables-1.8.10-1 and iptables-nft-1:1.8.11-2 are in conflict. Remove iptables? [y/N] error: unresolvable package conflicts detected"})
	if d == nil || len(d.Conflicts) != 1 || d.Conflicts[0].Remove != "iptables" || d.Conflicts[0].Package != "iptables-nft" {
		t.Fatalf("diagnosis: %+v", d)
	}
}

func TestDiagnose(t *testing.T) {
	for _, c := range []struct {
		name  string
		fn    func([]string) *Diagnosis
		log   string
		code  string
		lines int // at least
	}{
		{"pacman file conflict", diagnosePacman, `
error: failed to commit transaction (conflicting files)
python-foo: /usr/lib/python3.12/site-packages/foo/__init__.py exists in filesystem
python-foo: /usr/lib/python3.12/site-packages/foo/core.py exists in filesystem
Errors occurred, no packages were upgraded.`, CodeFileConflict, 3},
		{"pacman lock", diagnosePacman, `
error: failed to init transaction (unable to lock database)
error: could not lock database: File exists
  if you're sure a package manager is not already
  running, you can remove /var/lib/pacman/db.lck`, CodeLocked, 2},
		{"pacman signature", diagnosePacman, `
error: linux-firmware: signature from "Some Packager <p@example.org>" is unknown trust
:: File /var/cache/pacman/pkg/linux-firmware-20261101.pkg.tar.zst is corrupted (invalid or corrupted package (PGP signature)).
error: failed to commit transaction (invalid or corrupted package)`, CodeSignature, 2},
		{"pacman dependency", diagnosePacman, `
:: unable to satisfy dependency 'libfoo.so=2-64' required by bar
error: failed to prepare transaction (could not satisfy dependencies)`, CodeDependency, 2},
		{"pacman download", diagnosePacman, `
error: failed retrieving file 'core.db' from mirror.example.org : Could not resolve host: mirror.example.org
error: failed to synchronize all databases (download library error)`, CodeNetwork, 2},
		{"pacman space", diagnosePacman, `
checking available disk space...
error: Partition / too full: 512000 blocks needed, 102400 blocks free
error: not enough free disk space`, CodeNoSpace, 2},
		{"pacman hook", diagnosePacman, `
(3/5) Updating linux initcpios...
error: command failed to execute correctly`, CodeScript, 1},
		{"pacman other", diagnosePacman, `
error: something unusual happened`, CodeFailed, 1},
		{"apt held", diagnoseApt, `
The following packages have unmet dependencies:
 libfoo2 : Depends: libbar (>= 2) but 1 is to be installed
E: Unable to correct problems, you have held broken packages.`, CodeDependency, 3},
		{"apt overwrite", diagnoseApt, `
dpkg: error processing archive /var/cache/apt/archives/foo_2_amd64.deb (--unpack):
 trying to overwrite '/usr/bin/foo', which is also in package bar 1.0
Errors were encountered while processing:
E: Sub-process /usr/bin/dpkg returned an error code (1)`, CodeFileConflict, 2},
		{"apt lock", diagnoseApt, `
E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 1234 (unattended-upgr)
E: Unable to acquire the dpkg frontend lock (/var/lib/dpkg/lock-frontend), is another process using it?`, CodeLocked, 2},
		{"apt key", diagnoseApt, `
W: GPG error: https://repo.example.org stable InRelease: The following signatures couldn't be verified because the public key is not available: NO_PUBKEY 0123456789ABCDEF`, CodeSignature, 1},
		{"apt postinst", diagnoseApt, `
pmerror:foo:50.0000:installed foo package post-installation script subprocess returned error exit status 1
dpkg: error processing package foo (--configure):
 installed foo package post-installation script subprocess returned error exit status 1`, CodeScript, 2},
		{"dnf conflict", diagnoseDnf, `
Error: 
 Problem: package foo-2.0-1.fc41.x86_64 conflicts with bar < 2 provided by bar-1.0-1.fc41.x86_64
  - cannot install the best update candidate for package foo-1.0-1.fc41.x86_64
  - problem with installed package bar-1.0-1.fc41.x86_64
(try to add '--allowerasing' to command line to replace conflicting packages or '--skip-broken' to skip uninstallable packages)`, CodeConflict, 3},
		{"dnf file conflict", diagnoseDnf, `
Error: Transaction test error:
  file /usr/bin/foo from install of foo-2.0-1.fc41.x86_64 conflicts with file from package bar-1.0-1.fc41.x86_64`, CodeFileConflict, 1},
		{"dnf metadata", diagnoseDnf, `
Errors during downloading metadata for repository 'updates':
  - Curl error (6): Couldn't resolve host name for https://mirrors.fedoraproject.org/metalink
Error: Failed to download metadata for repo 'updates': Cannot download repomd.xml`, CodeNetwork, 2},
		{"dnf gpg", diagnoseDnf, `
Public key for foo-2.0-1.fc41.x86_64.rpm is not installed
Error: GPG check FAILED`, CodeSignature, 1},
		{"zypper problem", diagnoseZypper, `
<message type="info">Computing upgrade...</message>
<message type="error">Problem: the to be installed bar-2.0-1.1.x86_64 conflicts with &apos;foo&apos; provided by the installed foo-1.0-1.1.x86_64</message>`, CodeConflict, 1},
		{"zypper lock", diagnoseZypper, `
<message type="error">System management is locked by the application with pid 812 (zypper).</message>`, CodeLocked, 1},
		{"zypper download", diagnoseZypper, `
<message type="error">Download (curl) error for &apos;https://download.opensuse.org/repodata/repomd.xml&apos;:</message>`, CodeNetwork, 1},
		{"apk select", diagnoseApk, `
ERROR: unable to select packages:
  so:libfoo.so.2 (no such package):
    required by: bar-1.0-r0[so:libfoo.so.2]`, CodeDependency, 2},
		{"apk lock", diagnoseApk, `
ERROR: Unable to lock database: Resource temporarily unavailable`, CodeLocked, 1},
		{"apk signature", diagnoseApk, `
ERROR: https://dl-cdn.alpinelinux.org/alpine/v3.20/main: UNTRUSTED signature`, CodeSignature, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := c.fn(split(c.log))
			if d == nil {
				t.Fatal("no diagnosis")
			}
			if d.Code != c.code || len(d.Lines) < c.lines || d.Title == "" || d.Summary == "" {
				t.Fatalf("got %s %q (%d lines):\n%s", d.Code, d.Summary, len(d.Lines), strings.Join(d.Lines, "\n"))
			}
		})
	}
}

func TestDiagnoseDnfConflict(t *testing.T) {
	d := diagnoseDnf([]string{" Problem: package foo-2.0-1.fc41.x86_64 conflicts with bar < 2 provided by bar-1.0-1.fc41.x86_64"})
	if d == nil || len(d.Conflicts) != 1 || d.Conflicts[0].Package != "foo-2.0-1.fc41.x86_64" || d.Conflicts[0].Remove != "bar-1.0-1.fc41.x86_64" {
		t.Fatalf("diagnosis: %+v", d)
	}
}

func TestDiagnoseNothing(t *testing.T) {
	b, err := os.ReadFile("testdata/pacman-upgrade.txt")
	if err != nil {
		t.Fatal(err)
	}
	if d := diagnosePacman(split(string(b))); d != nil {
		t.Fatalf("a good upgrade was diagnosed: %+v", d)
	}
	if d := diagnoseApt([]string{"Reading package lists...", "pmstatus:foo:50:Installing foo"}); d != nil {
		t.Fatalf("apt progress was diagnosed: %+v", d)
	}
}

func TestDiagnoseLinesAreCapped(t *testing.T) {
	var lines []string
	for range 100 {
		lines = append(lines, "python-foo: /usr/lib/python3/"+strings.Repeat("x", 5)+"/f"+string(rune('a'+len(lines)%26))+string(rune('a'+len(lines)/26))+" exists in filesystem")
	}
	if d := diagnosePacman(lines); d == nil || len(d.Lines) != maxDiagLines {
		t.Fatalf("lines: %v", d)
	}
}

// pacman before 6.1 (and pamac) print names without versions; the package
// to remove is the one in the "Remove …?" question.
func TestDiagnosePacmanConflictWithoutVersions(t *testing.T) {
	for _, line := range []string{
		":: plasma-workspace and plasma-x11-session are in conflict. Remove plasma-x11-session? [y/N] error: unresolvable package conflicts detected",
		":: plasma-x11-session and plasma-workspace are in conflict. Remove plasma-x11-session? [y/N]",
	} {
		d := diagnosePacman([]string{line})
		want := []Conflict{{Package: "plasma-workspace", Remove: "plasma-x11-session"}}
		if d == nil || !slices.Equal(d.Conflicts, want) {
			t.Errorf("%q: %+v", line, d)
		}
	}
}

func TestSplitNameVersion(t *testing.T) {
	for in, want := range map[string][2]string{
		"plasma-workspace-6.8.2-1":  {"plasma-workspace", "6.8.2-1"},
		"iptables-nft-1:1.8.11-2":   {"iptables-nft", "1:1.8.11-2"},
		"lib32-gcc-libs-14.2.1-1.1": {"lib32-gcc-libs", "14.2.1-1.1"},
		"linux-firmware-20261101-1": {"linux-firmware", "20261101-1"},
		"plasma-x11-session":        {"plasma-x11-session", ""},
		"qt5-base":                  {"qt5-base", ""},
		"python-requests-2.32.3-1":  {"python-requests", "2.32.3-1"},
		"vim-9.1.1629+r12+g0abc-1":  {"vim", "9.1.1629+r12+g0abc-1"},
	} {
		if n, v := splitNameVersion(in); n != want[0] || v != want[1] {
			t.Errorf("splitNameVersion(%q) = %q, %q", in, n, v)
		}
	}
}

func TestDiagnoseDnf5Conflict(t *testing.T) {
	d := diagnoseDnf([]string{" Problem: package foo-2.0-1.fc41.x86_64 from updates conflicts with bar < 2 provided by bar-1.0-1.fc41.x86_64 from @System"})
	if d == nil || d.Code != CodeConflict || len(d.Conflicts) != 1 || d.Conflicts[0].Remove != "bar-1.0-1.fc41.x86_64" {
		t.Fatalf("diagnosis: %+v", d)
	}
}
