// SPDX-License-Identifier: AGPL-3.0-only

package brew

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/kushalpandya/wargo/internal/pkg"
)

// outdated is `brew outdated --json=v2`.
type outdated struct {
	Formulae []struct {
		Name              string   `json:"name"`
		InstalledVersions []string `json:"installed_versions"`
		CurrentVersion    string   `json:"current_version"`
		Pinned            bool     `json:"pinned"`
	} `json:"formulae"`
	Casks []struct {
		Name string `json:"name"`
		// A list in current Homebrew, a single string in older releases.
		InstalledVersions json.RawMessage `json:"installed_versions"`
		CurrentVersion    string          `json:"current_version"`
	} `json:"casks"`
}

// caskInfo is the part of `brew info --cask --json=v2` Wargo reads.
type caskInfo struct {
	Token     string                       `json:"token"`
	Tap       string                       `json:"tap"`
	Artifacts []map[string]json.RawMessage `json:"artifacts"`
}

// formulaInfo is the part of `brew info --formula --json=v2` Wargo reads.
type formulaInfo struct {
	Name string `json:"name"`
	Tap  string `json:"tap"`
}

type infoResult struct {
	Formulae []formulaInfo `json:"formulae"`
	Casks    []caskInfo    `json:"casks"`
}

// parseOutdated turns `brew outdated --json=v2` into updates. Pinned
// formulae are left out of updates (brew upgrade skips them) and returned
// as read-only extras.
func parseOutdated(b []byte) (updates, pinned []pkg.Update, err error) {
	var o outdated
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, nil, err
	}
	for _, f := range o.Formulae {
		u := pkg.Update{Name: f.Name, Current: last(f.InstalledVersions), New: f.CurrentVersion, Source: "formula"}
		if f.Pinned {
			u.Source = "pinned"
			pinned = append(pinned, u)
			continue
		}
		updates = append(updates, u)
	}
	for _, c := range o.Casks {
		updates = append(updates, pkg.Update{Name: c.Name, Current: caskVersion(c.InstalledVersions), New: c.CurrentVersion, Source: "cask"})
	}
	return updates, pinned, nil
}

func last(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

func caskVersion(raw json.RawMessage) string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return last(list)
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one
	}
	return ""
}

// applyInfo fills in each update's tap and, for casks, whether its
// installer may ask for an administrator password.
func applyInfo(updates []pkg.Update, b []byte) {
	var info infoResult
	if json.Unmarshal(b, &info) != nil {
		return
	}
	taps := map[string]string{}
	pw := map[string]bool{}
	for _, f := range info.Formulae {
		taps["formula:"+f.Name] = f.Tap
	}
	for _, c := range info.Casks {
		taps["cask:"+c.Token] = c.Tap
		pw[c.Token] = needsPassword(c.Artifacts)
	}
	for i := range updates {
		u := &updates[i]
		kind := u.Source
		if kind == "pinned" {
			kind = "formula"
		}
		if t := taps[kind+":"+u.Name]; t != "" {
			u.Repo = t
		}
		if u.Source == "cask" {
			u.NeedsPassword = pw[u.Name]
		}
	}
}

// needsPassword reports whether installing or replacing a cask is likely
// to run sudo: .pkg installers always do, and so do uninstall steps that
// touch launchd services, kernel extensions, package receipts or system
// paths, and scripts marked to run as root.
func needsPassword(artifacts []map[string]json.RawMessage) bool {
	for _, a := range artifacts {
		for kind, raw := range a {
			switch kind {
			case "pkg":
				return true
			case "uninstall", "installer", "preflight", "postflight":
				if rootSteps(raw) {
					return true
				}
			}
		}
	}
	return false
}

// rootSteps looks inside an uninstall or installer stanza for steps that
// need root.
func rootSteps(raw json.RawMessage) bool {
	var list []map[string]any
	if json.Unmarshal(raw, &list) != nil {
		return false
	}
	for _, m := range list {
		for k, v := range m {
			switch k {
			case "pkgutil", "launchctl", "kext":
				return true
			case "script", "early_script":
				if sm, ok := v.(map[string]any); ok && sm["sudo"] == true {
					return true
				}
			case "delete", "rmdir":
				for _, p := range strs(v) {
					if systemPath(p) {
						return true
					}
				}
			}
		}
	}
	return false
}

func strs(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// systemPath is a location an admin user can't change without sudo.
func systemPath(p string) bool {
	for _, pre := range []string{"/Library/", "/usr/", "/private/", "/etc/", "/var/", "/System/", "/opt/"} {
		if strings.HasPrefix(p, pre) && !strings.HasPrefix(p, "/opt/homebrew/") {
			return true
		}
	}
	return false
}

// Upgrade stages shown in the UI stepper.
const (
	StagePreparing   = pkg.StagePreparing
	StageDownloading = pkg.StageDownloading
	StageInstalling  = pkg.StageInstalling
	StageCleanup     = "Cleaning up"
	StageDone        = pkg.StageDone
)

// progress is the parsed state of a `brew upgrade` run.
type progress struct {
	Stage    string
	Frac     float64
	Current  string
	Upgraded int

	planned  int // packages the last check listed
	total    int // packages brew said it would upgrade so far
	fetched  int
	sudoFail bool // a cask asked for a password that wasn't given
}

func (p *progress) set(stage string, frac float64, current string) {
	if stage != "" {
		p.Stage = stage
	}
	frac = max(0, min(1, frac))
	if frac > p.Frac {
		p.Frac = frac
	}
	p.Current = current
}

// want is how many packages this run should upgrade.
func (p *progress) want() int { return max(p.planned, p.total) }

func ratio(a, b int) float64 {
	if b <= 0 {
		return 0
	}
	return min(1, float64(a)/float64(b))
}

var (
	reUpgradingN = regexp.MustCompile(`^==> Upgrading (\d+) outdated (?:packages?|formulae|formula|casks?)`)
	reFetching   = regexp.MustCompile(`^==> Fetching (?:downloads for: )?(\S+?)(?:,.*)?$`)
	reDownload   = regexp.MustCompile(`^==> Downloading https?://\S*?/([^/\s]+?)(?:--[^/\s]*)?(?:\.bottle\S*|\.tar\S*|\.zip|\.dmg|\.pkg)?$`)
	reUpgrading  = regexp.MustCompile(`^==> Upgrading (\S+)$`)
	rePouring    = regexp.MustCompile(`^==> Pouring (\S+?)--`)
	reCellar     = regexp.MustCompile(`/Cellar/[^/]+/[^:]+: [\d,]+ files?`)
	reCaskDone   = regexp.MustCompile(`^(?:🍺\s+)?(\S+) was successfully upgraded!`)
)

// parseLine advances p from one line of `brew upgrade` output (run with
// HOMEBREW_NO_COLOR and HOMEBREW_NO_EMOJI).
func parseLine(line string, p *progress) {
	t := strings.TrimSpace(pkg.CleanLine(line))
	if t == "" {
		return
	}
	low := strings.ToLower(t)
	switch {
	case strings.Contains(low, "no administrator password was provided"),
		strings.Contains(low, "a password is required"),
		strings.Contains(low, "a terminal is required to read the password"),
		strings.Contains(low, "incorrect password attempt"):
		p.sudoFail = true
	}
	if m := reUpgradingN.FindStringSubmatch(t); m != nil {
		n, _ := strconv.Atoi(m[1])
		p.total += n
		p.set(StagePreparing, 0.03, "")
		return
	}
	if m := reFetching.FindStringSubmatch(t); m != nil {
		p.fetched++
		p.set(StageDownloading, 0.05+0.35*ratio(p.fetched-1, max(p.want(), p.fetched)), m[1])
		return
	}
	if m := reDownload.FindStringSubmatch(t); m != nil {
		if p.Stage == "" || p.Stage == StagePreparing {
			p.set(StageDownloading, 0.05, m[1])
		}
		return
	}
	if m := reUpgrading.FindStringSubmatch(t); m != nil {
		p.set(StageInstalling, 0.4+0.5*ratio(p.Upgraded, max(p.want(), p.Upgraded+1)), m[1])
		return
	}
	if m := rePouring.FindStringSubmatch(t); m != nil {
		p.set(StageInstalling, 0.4+0.5*ratio(p.Upgraded, max(p.want(), p.Upgraded+1)), m[1])
		return
	}
	if reCellar.MatchString(t) && !strings.HasPrefix(t, "Removing:") {
		p.Upgraded++
		p.set(StageInstalling, 0.4+0.5*ratio(p.Upgraded, max(p.want(), p.Upgraded)), p.Current)
		return
	}
	if m := reCaskDone.FindStringSubmatch(t); m != nil {
		p.Upgraded++
		p.set(StageInstalling, 0.4+0.5*ratio(p.Upgraded, max(p.want(), p.Upgraded)), m[1])
		return
	}
	switch {
	case strings.HasPrefix(t, "==> Checking for dependents"):
		p.set(StageCleanup, 0.9, "Checking dependents")
	case strings.HasPrefix(t, "==> Running `brew cleanup"), strings.HasPrefix(t, "==> Cleaning up"),
		strings.HasPrefix(t, "==> `brew cleanup` has not been run"), strings.HasPrefix(t, "Removing: "):
		// brew cleans up after each formula too; only the final cleanup,
		// once every package is in, is a stage of its own.
		if p.want() > 0 && p.Upgraded >= p.want() {
			p.set(StageCleanup, 0.92, "Removing old versions")
		}
	case strings.HasPrefix(t, "==> Caveats"):
		p.set("", p.Frac, p.Current)
	}
}
