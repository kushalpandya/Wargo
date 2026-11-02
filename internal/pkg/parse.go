// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"regexp"
	"strconv"
	"strings"
)

func lines(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

var reArrow = regexp.MustCompile(`^(\S+)\s+(\S+)\s+->\s+(\S+)(?:\s+(\S+))?(?:\s+\[ignored\])?\s*$`)

// ParsePamacCheck parses `pamac checkupdates [--aur]` output:
//
//	3 available updates:
//	linux618      6.18.49-1  -> 6.18.55-1  core
//	yay           12.4.2-1   -> 12.5.0-1   AUR
//
//	Out of Date:
//	teamviewer  15.81.5-1  AUR
//
// Entries from the "AUR" repo go to extras. The "Out of Date" section lists
// flagged AUR packages without a newer version and is ignored.
func ParsePamacCheck(b []byte) (updates, extras []Update) {
	section := ""
	for _, l := range lines(b) {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if strings.HasSuffix(t, ":") && !strings.Contains(t, "->") {
			section = strings.ToLower(strings.TrimSuffix(t, ":"))
			continue
		}
		if section == "out of date" {
			continue
		}
		m := reArrow.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		u := Update{Name: m[1], Current: m[2], New: m[3], Repo: m[4], Source: "repo"}
		if strings.EqualFold(u.Repo, "AUR") {
			u.Repo, u.Source = "AUR", "aur"
			extras = append(extras, u)
			continue
		}
		updates = append(updates, u)
	}
	return
}

// ParseArrowList parses `checkupdates`, `pacman -Qu` and `yay -Qua` output:
//
//	linux 6.18.1-1 -> 6.18.2-1
//	foo 1.0-1 -> 1.1-1 [ignored]
func ParseArrowList(b []byte, source string) []Update {
	var out []Update
	for _, l := range lines(b) {
		t := strings.TrimSpace(l)
		if strings.HasSuffix(t, "[ignored]") {
			continue
		}
		m := reArrow.FindStringSubmatch(t)
		if m == nil || m[4] != "" {
			continue
		}
		out = append(out, Update{Name: m[1], Current: m[2], New: m[3], Source: source})
	}
	return out
}

// pacmanTarget is one line of `pacman -S[u]p --print-format '%n %v %r %s'`.
type pacmanTarget struct {
	Version, Repo string
	Size          int64
}

// ParsePacmanPrint parses `%n %v %r %s` lines into a map by name.
func ParsePacmanPrint(b []byte) map[string]pacmanTarget {
	out := map[string]pacmanTarget{}
	for _, l := range lines(b) {
		f := strings.Fields(l)
		if len(f) != 4 {
			continue
		}
		n, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			continue
		}
		out[f[0]] = pacmanTarget{Version: f[1], Repo: f[2], Size: n}
	}
	return out
}

var (
	reAptInst = regexp.MustCompile(`^Inst (\S+) (?:\[([^\]]*)\] )?\((\S+) ([^\[\)]*?)\s*(?:\[([^\]]*)\])?\)`)
)

// ParseAptSimulate parses `apt-get -s full-upgrade` "Inst" lines:
//
//	Inst libc6 [2.36-9+deb12u3] (2.36-9+deb12u4 Debian:12.5/stable [amd64]) []
//	Inst linux-image-6.1.0-18-amd64 (6.1.76-1 Debian-Security:12/stable-security [amd64])
//
// Packages without a current version are new dependencies.
func ParseAptSimulate(b []byte) []Update {
	var out []Update
	for _, l := range lines(b) {
		m := reAptInst.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		out = append(out, Update{Name: m[1], Current: m[2], New: m[3], Repo: aptRepo(m[4]), Source: "repo"})
	}
	return out
}

// aptRepo turns "Debian:12.5/stable, Debian-Security:12/stable-security" into "stable".
func aptRepo(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ","); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

var reAptList = regexp.MustCompile(`^(\S+?)/(\S+)\s+(\S+)\s+\S+\s+\[upgradable from: ([^\]]+)\]`)

// ParseAptList parses `apt list --upgradable`:
//
//	bash/stable 5.2.15-2+b7 amd64 [upgradable from: 5.2.15-2+b2]
func ParseAptList(b []byte) []Update {
	var out []Update
	for _, l := range lines(b) {
		m := reAptList.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		repo := m[2]
		if i := strings.Index(repo, ","); i >= 0 {
			repo = repo[:i]
		}
		out = append(out, Update{Name: m[1], Current: m[4], New: m[3], Repo: repo, Source: "repo"})
	}
	return out
}

// ParseDnfCheckUpdate parses `dnf check-update -q` (dnf4 and dnf5):
//
//	kernel.x86_64          6.8.9-300.fc40        updates
//	very-long-package-name.noarch
//	                       1.2-1.fc40            updates
//	Obsoleting Packages
//	...
//
// Names keep their architecture suffix so they can be matched against rpm.
func ParseDnfCheckUpdate(b []byte) []Update {
	var out []Update
	carry := ""
	for _, l := range lines(b) {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "Obsoleting Packages") || strings.HasPrefix(t, "Security:") {
			break
		}
		if strings.HasPrefix(t, "Last metadata expiration") || strings.HasPrefix(t, "Updating and loading") ||
			strings.HasPrefix(t, "Repositories loaded") || strings.HasPrefix(t, "Available upgrades") {
			continue
		}
		f := strings.Fields(t)
		if carry != "" {
			f = append([]string{carry}, f...)
			carry = ""
		}
		if len(f) == 1 && strings.Contains(f[0], ".") {
			carry = f[0]
			continue
		}
		if len(f) != 3 || !strings.Contains(f[0], ".") {
			continue
		}
		out = append(out, Update{Name: f[0], New: f[1], Repo: f[2], Source: "repo"})
	}
	return out
}

// ParseRpmQuery parses `rpm -qa --qf '%{NAME}.%{ARCH} %{VERSION}-%{RELEASE}\n'`.
func ParseRpmQuery(b []byte) map[string]string {
	out := map[string]string{}
	for _, l := range lines(b) {
		if f := strings.Fields(l); len(f) == 2 {
			out[f[0]] = f[1]
		}
	}
	return out
}

type zypperStream struct {
	Updates []struct {
		Kind       string `xml:"kind,attr"`
		Name       string `xml:"name,attr"`
		Edition    string `xml:"edition,attr"`
		EditionOld string `xml:"edition-old,attr"`
		Source     struct {
			Alias string `xml:"alias,attr"`
			URL   string `xml:"url,attr"`
		} `xml:"source"`
	} `xml:"update-status>update-list>update"`
	Messages []struct {
		Type string `xml:"type,attr"`
		Text string `xml:",chardata"`
	} `xml:"message"`
}

// ParseZypperXML parses `zypper --xmlout -q list-updates`. Only packages are
// returned (patches/products are reflected by their packages).
func ParseZypperXML(b []byte) ([]Update, error) {
	var s zypperStream
	if err := xml.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	var out []Update
	for _, u := range s.Updates {
		if u.Kind != "" && u.Kind != "package" {
			continue
		}
		out = append(out, Update{Name: u.Name, Current: u.EditionOld, New: u.Edition, Repo: u.Source.Alias, Source: "repo"})
	}
	return out, nil
}

var reApkVer = regexp.MustCompile(`^(\S+)\s+<\s+(\S+)`)

// ParseApkVersion parses `apk version -l '<'`:
//
//	Installed:                                Available:
//	busybox-1.36.1-r2                       < 1.36.1-r5
func ParseApkVersion(b []byte) []Update {
	var out []Update
	for _, l := range lines(b) {
		m := reApkVer.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		name, cur := splitApkName(m[1])
		if name == "" {
			continue
		}
		out = append(out, Update{Name: name, Current: cur, New: m[2], Source: "repo"})
	}
	return out
}

var reApkRel = regexp.MustCompile(`-r\d+$`)

// splitApkName splits "py3-foo-1.2.3-r0" into ("py3-foo", "1.2.3-r0").
func splitApkName(s string) (string, string) {
	rel := reApkRel.FindString(s)
	base := strings.TrimSuffix(s, rel)
	i := strings.LastIndex(base, "-")
	if i <= 0 {
		return "", ""
	}
	return base[:i], base[i+1:] + rel
}

// ParseFlatpakUpdates parses `flatpak remote-ls --updates
// --columns=application,version,origin` (tab separated). current maps
// application → installed version from `flatpak list --columns=application,version`.
func ParseFlatpakUpdates(b []byte, current map[string]string) []Update {
	var out []Update
	for _, l := range lines(b) {
		f := strings.Split(l, "\t")
		if len(f) < 1 || strings.TrimSpace(f[0]) == "" || f[0] == "Application ID" {
			continue
		}
		u := Update{Name: strings.TrimSpace(f[0]), Source: "flatpak", Repo: "flatpak"}
		if len(f) > 1 {
			u.New = strings.TrimSpace(f[1])
		}
		if len(f) > 2 && strings.TrimSpace(f[2]) != "" {
			u.Repo = strings.TrimSpace(f[2])
		}
		u.Current = current[u.Name]
		out = append(out, u)
	}
	return out
}

// ParseFlatpakList parses `flatpak list --columns=application,version`.
func ParseFlatpakList(b []byte) map[string]string {
	out := map[string]string{}
	for _, l := range lines(b) {
		f := strings.Split(l, "\t")
		if len(f) >= 2 {
			out[strings.TrimSpace(f[0])] = strings.TrimSpace(f[1])
		}
	}
	return out
}
