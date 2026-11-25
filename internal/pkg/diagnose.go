// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"fmt"
	"regexp"
	"strings"
)

// Diagnosis explains why an upgrade failed, from its log: a stable code,
// a short title, a sentence for the owner, and the lines that show it.
type Diagnosis struct {
	// Code is one of the Code* constants.
	Code    string `json:"code"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	// Lines are the log lines the diagnosis is based on.
	Lines []string `json:"lines,omitempty"`
	// Conflicts lists package conflicts the package manager refused to
	// resolve on its own (removing a package needs the owner's consent).
	Conflicts []Conflict `json:"conflicts,omitempty"`
}

// Conflict is one package conflict: installing Package (at Version) needs
// Remove (installed at RemoveVersion) to go.
type Conflict struct {
	Package       string `json:"package"`
	Version       string `json:"version,omitempty"`
	Remove        string `json:"remove"`
	RemoveVersion string `json:"removeVersion,omitempty"`
}

// Diagnosis codes (also the audit code of the failed run).
const (
	CodeConflict     = "pkg.conflict"      // packages conflict; one has to be removed
	CodeFileConflict = "pkg.file_conflict" // a file already exists or belongs to another package
	CodeDependency   = "pkg.dependency"    // dependencies can't be satisfied
	CodeLocked       = "pkg.locked"        // another package manager holds the lock
	CodeNoSpace      = "pkg.no_space"      // not enough disk space
	CodeSignature    = "pkg.signature"     // a signature or key couldn't be verified
	CodeNetwork      = "pkg.network"       // mirrors or repositories couldn't be reached
	CodeScript       = "pkg.script"        // a package's install script or hook failed
	CodeFailed       = "pkg.failed"        // something else; Lines has the errors
)

// maxDiagLines caps Diagnosis.Lines.
const maxDiagLines = 20

// Explanation describes a diagnosis code. Rank orders codes from most to
// least specific: the first error is often only the consequence ("failed
// to prepare transaction").
type Explanation struct {
	Rank           int
	Title, Summary string
}

// Diagnoser collects the log lines that explain a failure and keeps the
// most specific code (by Codes[code].Rank). Homebrew uses it with its own
// codes.
type Diagnoser struct {
	Codes map[string]Explanation
	code  string
	lines []string
	seen  map[string]bool
}

// Add records line as evidence for code.
func (d *Diagnoser) Add(code, line string) {
	if d.Codes[code].Rank > d.Codes[d.code].Rank {
		d.code = code
	}
	line = strings.TrimSpace(line)
	if line == "" || d.seen[line] || len(d.lines) >= maxDiagLines {
		return
	}
	if d.seen == nil {
		d.seen = map[string]bool{}
	}
	d.seen[line] = true
	d.lines = append(d.lines, line)
}

// Rules every package manager shares.
var (
	reNoSpace = regexp.MustCompile(`(?i)no space left on device|not enough (free )?(disk )?space|too full|more space needed`)
	reNetwork = regexp.MustCompile(`(?i)could not resolve host|temporary failure (in name )?resolution|failed to connect|connection (timed out|refused)|network is unreachable|operation timed out`)
)

// Common records l under noSpace or network when it says the disk is full
// or the network failed, and reports whether it did.
func (d *Diagnoser) Common(l, noSpace, network string) bool {
	switch {
	case reNoSpace.MatchString(l):
		d.Add(noSpace, l)
	case reNetwork.MatchString(l):
		d.Add(network, l)
	default:
		return false
	}
	return true
}

// Result is the diagnosis, or nil when no line matched.
func (d *Diagnoser) Result() *Diagnosis {
	if d.code == "" {
		return nil
	}
	e := d.Codes[d.code]
	return &Diagnosis{Code: d.code, Title: e.Title, Summary: e.Summary, Lines: d.lines}
}

// pacmanVersionRe matches the "version-release" end of "name-version-release"
// (pkgrel is a number such as 1 or 1.1; the version may have an epoch).
var pacmanVersionRe = regexp.MustCompile(`-((?:\d+:)?[^-:/\s]*\d[^-/\s]*-\d+(?:\.\d+)?)$`)

// splitNameVersion splits pacman's "name-version-release". Older pacman
// prints bare names, which are returned whole ("plasma-x11-session").
func splitNameVersion(s string) (name, version string) {
	if m := pacmanVersionRe.FindStringSubmatchIndex(s); m != nil {
		return s[:m[0]], s[m[2]:m[3]]
	}
	return s, ""
}

// --- pacman (and pamac, which installs through pacman here) --------------

var (
	// ":: a-1-1 and b-2-1 are in conflict. Remove b? [y/N]", sometimes
	// with the virtual package in parentheses, and with the prompt and the
	// next error on the same line when nobody answered it.
	rePacConflict = regexp.MustCompile(`^:: (\S+) and (\S+) are in conflict(?: \(([^)]*)\))?\.?(?: Remove (\S+)\? \[[yY]/[nN]\])?`)
	rePacError    = regexp.MustCompile(`(?:^|\s)error: (.*)$`)
)

func diagnosePacman(lines []string) *Diagnosis {
	d := Diagnoser{Codes: pkgCodes}
	var conflicts []Conflict
	have := map[string]bool{}
	for _, raw := range lines {
		l := strings.TrimSpace(CleanLine(raw))
		if l == "" {
			continue
		}
		if m := rePacConflict.FindStringSubmatch(l); m != nil {
			a, av := splitNameVersion(m[1])
			b, bv := splitNameVersion(m[2])
			c := Conflict{Package: a, Version: av, Remove: b, RemoveVersion: bv}
			// pacman asks to remove the installed one, whichever side it is.
			if m[4] != "" && m[4] == a && m[4] != b {
				c = Conflict{Package: b, Version: bv, Remove: a, RemoveVersion: av}
			}
			if !have[c.Package+"/"+c.Remove] {
				have[c.Package+"/"+c.Remove] = true
				conflicts = append(conflicts, c)
			}
			// Keep the conflict and the error, not the prompt between them.
			if i := strings.Index(l, " Remove "); i > 0 {
				d.Add(CodeConflict, l[:i])
			} else {
				d.Add(CodeConflict, l)
			}
			if e := rePacError.FindStringSubmatch(l); e != nil {
				d.Add(CodeFailed, "error: "+e[1])
			}
			continue
		}
		e := rePacError.FindStringSubmatch(l)
		switch {
		case strings.Contains(l, "exists in filesystem") || strings.Contains(l, "exists in both"):
			d.Add(CodeFileConflict, l)
		case strings.Contains(l, "unable to satisfy dependency") || strings.Contains(l, "breaks dependency") ||
			strings.Contains(l, "could not satisfy dependencies") || strings.Contains(l, "target not found"):
			d.Add(CodeDependency, l)
		case strings.Contains(l, "unable to lock database") || strings.Contains(l, "db.lck"):
			d.Add(CodeLocked, l)
		case strings.Contains(l, "signature") || strings.Contains(l, "PGP") || strings.Contains(l, "unknown trust") ||
			strings.Contains(l, "could not be looked up remotely") || strings.Contains(l, "keyring"):
			if e != nil {
				d.Add(CodeSignature, l)
			}
		case strings.Contains(l, "failed retrieving file") || strings.Contains(l, "failed to synchronize"):
			d.Add(CodeNetwork, l)
		case e != nil && strings.Contains(l, "command failed to execute correctly"):
			d.Add(CodeScript, l)
		case d.Common(l, CodeNoSpace, CodeNetwork):
		case e != nil:
			d.Add(CodeFailed, l)
		}
	}
	return pkgResult(&d, conflicts)
}

// --- apt -----------------------------------------------------------------

func diagnoseApt(lines []string) *Diagnosis {
	d := Diagnoser{Codes: pkgCodes}
	for _, raw := range lines {
		l := strings.TrimSpace(CleanLine(raw))
		// Status-Fd progress lines repeat what follows ("pmerror:" is kept).
		if l == "" || strings.HasPrefix(l, "dlstatus:") || strings.HasPrefix(l, "pmstatus:") {
			continue
		}
		switch {
		case strings.Contains(l, "trying to overwrite"):
			d.Add(CodeFileConflict, l)
		case strings.Contains(l, "Breaks:") || strings.Contains(l, "Conflicts:"):
			d.Add(CodeConflict, l)
		case strings.Contains(l, "unmet dependencies") || strings.Contains(l, "held broken packages") ||
			strings.Contains(l, "Depends:") || strings.Contains(l, "Unable to correct problems"):
			d.Add(CodeDependency, l)
		case strings.Contains(l, "Could not get lock") || strings.Contains(l, "Unable to acquire the dpkg frontend lock") ||
			strings.Contains(l, "is another process using it"):
			d.Add(CodeLocked, l)
		case strings.Contains(l, "NO_PUBKEY") || strings.Contains(l, "signatures couldn't be verified") ||
			strings.Contains(l, "is not signed") || strings.Contains(l, "EXPKEYSIG"):
			d.Add(CodeSignature, l)
		case strings.HasPrefix(l, "E: Failed to fetch") || strings.HasPrefix(l, "Err:") || strings.Contains(l, "Some index files failed"):
			d.Add(CodeNetwork, l)
		case strings.HasPrefix(l, "dpkg: error processing") || strings.Contains(l, "subprocess installed") ||
			strings.HasPrefix(l, "pmerror:"):
			d.Add(CodeScript, l)
		case d.Common(l, CodeNoSpace, CodeNetwork):
		case strings.HasPrefix(l, "E: ") || strings.HasPrefix(l, "dpkg: error"):
			d.Add(CodeFailed, l)
		}
	}
	return pkgResult(&d, nil)
}

// --- dnf (4 and 5) ---------------------------------------------------------

var (
	// dnf4 "package A conflicts with B provided by C", and dnf5 with
	// "from <repo>" after the packages.
	reDnfConflict = regexp.MustCompile(`package (\S+)(?: from \S+)? conflicts with (.+?) provided by (\S+)`)
	reDnfProblem  = regexp.MustCompile(`^(?:Problem(?: \d+)?:|- )`)
)

func diagnoseDnf(lines []string) *Diagnosis {
	d := Diagnoser{Codes: pkgCodes}
	var conflicts []Conflict
	inProblem := false
	for _, raw := range lines {
		l := strings.TrimSpace(CleanLine(raw))
		if l == "" {
			inProblem = false
			continue
		}
		if m := reDnfConflict.FindStringSubmatch(l); m != nil {
			conflicts = append(conflicts, Conflict{Package: m[1], Remove: m[3]})
		}
		switch {
		case strings.Contains(l, "conflicts with file from package") || strings.Contains(l, "file /") && strings.Contains(l, "conflicts"):
			d.Add(CodeFileConflict, l)
		case strings.Contains(l, "conflicts with") || strings.Contains(l, "--allowerasing"):
			d.Add(CodeConflict, l)
		case strings.Contains(l, "nothing provides") || strings.Contains(l, "requires") && strings.Contains(l, "none of the providers") ||
			strings.Contains(l, "cannot install the best update candidate"):
			d.Add(CodeDependency, l)
		case strings.HasPrefix(l, "Problem") || inProblem && reDnfProblem.MatchString(l):
			inProblem = true
			d.Add(CodeDependency, l)
		case strings.Contains(l, "GPG check FAILED") || strings.Contains(l, "public key") || strings.Contains(l, "Signature verification"):
			d.Add(CodeSignature, l)
		case strings.Contains(l, "Waiting for process with pid") || strings.Contains(l, "lock"):
			if strings.Contains(l, "Error") || strings.Contains(l, "Waiting") {
				d.Add(CodeLocked, l)
			}
		case strings.Contains(l, "Failed to download metadata") || strings.Contains(l, "Curl error") ||
			strings.Contains(l, "Cannot download") || strings.Contains(l, "No more mirrors to try"):
			d.Add(CodeNetwork, l)
		case strings.Contains(l, "scriptlet failed") || strings.Contains(l, "Error in PREIN") || strings.Contains(l, "Error in POSTIN"):
			d.Add(CodeScript, l)
		case d.Common(l, CodeNoSpace, CodeNetwork):
		case strings.HasPrefix(l, "Error:") || strings.HasPrefix(l, "error:"):
			d.Add(CodeFailed, l)
		}
	}
	return pkgResult(&d, conflicts)
}

// --- zypper (XML output) ------------------------------------------------------

var reZyTag = regexp.MustCompile(`<[^>]*>`)

// zypperText turns one XML output line into its text.
func zypperText(l string) (text string, isError bool) {
	isError = strings.Contains(l, `type="error"`)
	return strings.TrimSpace(xmlUnescape(reZyTag.ReplaceAllString(l, " "))), isError
}

func diagnoseZypper(lines []string) *Diagnosis {
	d := Diagnoser{Codes: pkgCodes}
	for _, raw := range lines {
		l, isErr := zypperText(strings.TrimSpace(CleanLine(raw)))
		if l == "" {
			continue
		}
		switch {
		case strings.Contains(l, "conflicts with file from package") || strings.Contains(l, "File conflicts"):
			d.Add(CodeFileConflict, l)
		case strings.Contains(l, "conflicts with") || strings.Contains(l, "obsoletes"):
			d.Add(CodeConflict, l)
		case strings.HasPrefix(l, "Problem:") || strings.Contains(l, "nothing provides") || strings.Contains(l, "requires"):
			if strings.HasPrefix(l, "Problem:") || isErr {
				d.Add(CodeDependency, l)
			}
		case strings.Contains(l, "System management is locked"):
			d.Add(CodeLocked, l)
		case strings.Contains(l, "Signature verification failed") || strings.Contains(l, "GPG") && isErr:
			d.Add(CodeSignature, l)
		case strings.Contains(l, "Download (curl) error") || strings.Contains(l, "Valid metadata not found") ||
			strings.Contains(l, "Repository") && strings.Contains(l, "is invalid"):
			d.Add(CodeNetwork, l)
		case strings.Contains(l, "%post") || strings.Contains(l, "%pre") && isErr:
			d.Add(CodeScript, l)
		case d.Common(l, CodeNoSpace, CodeNetwork):
		case isErr:
			d.Add(CodeFailed, l)
		}
	}
	return pkgResult(&d, nil)
}

// --- apk ---------------------------------------------------------------------

func diagnoseApk(lines []string) *Diagnosis {
	d := Diagnoser{Codes: pkgCodes}
	inSelect := false
	for _, raw := range lines {
		l := strings.TrimSpace(CleanLine(raw))
		if l == "" {
			continue
		}
		switch {
		case strings.Contains(l, "unable to select packages"):
			inSelect = true
			d.Add(CodeDependency, l)
		case inSelect && !strings.HasPrefix(l, "ERROR") && !strings.HasPrefix(l, "("):
			if strings.Contains(l, "conflicts:") || strings.Contains(l, "breaks:") {
				d.Add(CodeConflict, l)
			} else {
				d.Add(CodeDependency, l)
			}
		case strings.Contains(l, "trying to overwrite"):
			d.Add(CodeFileConflict, l)
		case strings.Contains(l, "Unable to lock database"):
			d.Add(CodeLocked, l)
		case strings.Contains(l, "UNTRUSTED signature") || strings.Contains(l, "BAD signature"):
			d.Add(CodeSignature, l)
		case strings.Contains(l, "temporary error") || strings.Contains(l, "network error") || strings.Contains(l, "DNS lookup error"):
			d.Add(CodeNetwork, l)
		case strings.Contains(l, "script exited with error"):
			d.Add(CodeScript, l)
		case d.Common(l, CodeNoSpace, CodeNetwork):
		case strings.HasPrefix(l, "ERROR:"):
			inSelect = false
			d.Add(CodeFailed, l)
		}
	}
	return pkgResult(&d, nil)
}

// --- summaries ---------------------------------------------------------------

var pkgCodes = map[string]Explanation{
	CodeConflict: {9, "Package conflict", ""}, // the summary names the packages (conflictSummary)
	CodeFileConflict: {8, "File conflict",
		"A package wants to install a file that already exists or belongs to another package. Nothing was changed; see which files below."},
	CodeDependency: {7, "Dependency problem",
		"The package manager couldn't work out the dependencies of these updates. Nothing was changed; this often clears up once the mirrors finish syncing."},
	CodeSignature: {6, "Signature problem",
		"A package signature or key couldn't be verified. Update the distribution's keyring first, then try again."},
	CodeNoSpace: {5, "Not enough disk space",
		"There wasn't enough free disk space for the upgrade. Free some space (for example the package cache) and try again."},
	CodeLocked: {4, "Package manager busy",
		"Another package manager (or an automatic update) was running. Wait for it to finish and try again."},
	CodeNetwork: {3, "Download failed",
		"Packages or package lists couldn't be downloaded. Check the server's network and mirrors, then try again."},
	CodeScript: {2, "Install script failed",
		"A package's install script or hook failed. The upgrade may be partly installed; see the lines below."},
	CodeFailed: {1, "Upgrade failed", "The package manager stopped with an error; see the lines below."},
}

// pkgResult is d's diagnosis, with the conflicts when that's the reason.
func pkgResult(d *Diagnoser, conflicts []Conflict) *Diagnosis {
	diag := d.Result()
	if diag != nil && diag.Code == CodeConflict {
		diag.Conflicts = conflicts
		diag.Summary = conflictSummary(conflicts)
	}
	return diag
}

// conflictSummary describes the conflicts and what resolving them takes.
func conflictSummary(cs []Conflict) string {
	switch len(cs) {
	case 0:
		return "Two packages conflict, and the package manager won't remove one without your permission, so nothing was changed."
	case 1:
		c := cs[0]
		return fmt.Sprintf("The update to %s conflicts with %s, which has to be removed first. "+
			"The package manager won't remove it without your permission, so nothing was changed.",
			withVersion(c.Package, c.Version), withVersion(c.Remove, c.RemoveVersion))
	}
	removes := make([]string, len(cs))
	for i, c := range cs {
		removes[i] = withVersion(c.Remove, c.RemoveVersion)
	}
	return fmt.Sprintf("The updates conflict with %s, which have to be removed first. "+
		"The package manager won't remove them without your permission, so nothing was changed.", joinAnd(removes))
}

func withVersion(name, version string) string {
	if version == "" {
		return name
	}
	return name + " " + version
}

func joinAnd(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}
