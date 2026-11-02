// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"regexp"
	"strconv"
	"strings"
)

var reANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][A-Z0-9]`)

// CleanLine strips ANSI escapes and keeps only the text after the last
// carriage return (progress-bar redraws).
func CleanLine(s string) string {
	s = strings.TrimRight(s, "\r\n")
	if i := strings.LastIndexByte(s, '\r'); i >= 0 {
		s = s[i+1:]
	}
	s = reANSI.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, s)
}

// set moves the state forward; the overall fraction never goes backwards.
func (st *ProgressState) set(stage string, frac float64, current string) {
	if stage != "" {
		st.Stage = stage
	}
	frac = max(0, min(1, frac))
	if frac > st.Frac {
		st.Frac = frac
	}
	if current != "" || stage != "" {
		st.Current = current
	}
}

func ratio(a, b int) float64 {
	if b <= 0 {
		return 0
	}
	return min(1, float64(a)/float64(b))
}

var (
	reCounter   = regexp.MustCompile(`^\(\s*(\d+)/(\d+)\)\s+(.*)$`)
	rePacBar    = regexp.MustCompile(`\s*\[[#\-oc C\s]*\]\s+\d+%\s*$`)
	rePacPkgs   = regexp.MustCompile(`^Packages \((\d+)\)`)
	rePacOp     = regexp.MustCompile(`^(upgrading|installing|reinstalling|downgrading|removing) (\S+?)(?:\.\.\.)?$`)
	rePacDlNew  = regexp.MustCompile(`^(\S+) downloading\.\.\.$`)
	rePacDlOld  = regexp.MustCompile(`^downloading (\S+?)\.\.\.$`)
	rePacTrimFn = regexp.MustCompile(`(-[^-]+-[^-]+-(x86_64|aarch64|armv7h|i686|any))?(\.pkg\.tar\.\w+)?(\.sig)?$`)
)

// parsePacman understands pacman's non-tty output (what systemd-run
// captures) as well as tty-style "( 3/12) upgrading foo [###] 45%" lines.
func parsePacman(line string, st *ProgressState) {
	t := strings.TrimSpace(rePacBar.ReplaceAllString(CleanLine(line), ""))
	if t == "" {
		return
	}
	switch {
	case strings.HasPrefix(t, ":: Synchronizing package databases"):
		st.phase = "sync"
		st.set(StagePreparing, 0.01, "Synchronizing package databases")
		return
	case strings.HasPrefix(t, ":: Starting full system upgrade"), t == "resolving dependencies...", t == "looking for conflicting packages...":
		st.phase = "resolve"
		st.set(StagePreparing, 0.04, "Resolving dependencies")
		return
	case strings.Contains(t, "there is nothing to do"):
		st.phase = "done"
		st.set(StageDone, 1, "Nothing to do")
		return
	case strings.HasPrefix(t, ":: Retrieving packages"):
		st.phase = "download"
		st.set(StageDownloading, 0.05, "")
		return
	case strings.HasPrefix(t, ":: Running pre-transaction hooks"):
		st.phase = "prehooks"
		st.set(StageInstalling, 0.49, "Pre-transaction hooks")
		return
	case strings.HasPrefix(t, ":: Processing package changes"):
		st.phase = "install"
		st.set(StageInstalling, 0.5, "")
		return
	case strings.HasPrefix(t, ":: Running post-transaction hooks"):
		st.phase = "posthooks"
		st.set(StageHooks, 0.9, "")
		return
	}
	if m := rePacPkgs.FindStringSubmatch(t); m != nil {
		st.total, _ = strconv.Atoi(m[1])
		st.dlTotal = st.total
		return
	}
	switch t {
	case "checking keyring...", "checking package integrity...", "loading package files...",
		"checking for file conflicts...", "checking available disk space...", "downloading required keys...":
		st.set(StageInstalling, 0.46, strings.TrimSuffix(t, "..."))
		return
	}
	if m := reCounter.FindStringSubmatch(t); m != nil {
		n, _ := strconv.Atoi(m[1])
		total, _ := strconv.Atoi(m[2])
		msg := strings.TrimSpace(m[3])
		switch {
		case st.phase == "posthooks":
			st.set(StageHooks, 0.9+0.1*ratio(n-1, total), strings.TrimSuffix(msg, "..."))
		case st.phase == "prehooks":
			st.set(StageInstalling, 0.49, strings.TrimSuffix(msg, "..."))
		case strings.HasPrefix(msg, "checking") || strings.HasPrefix(msg, "loading"):
			st.set(StageInstalling, 0.46, msg)
		default:
			if op := rePacOp.FindStringSubmatch(msg); op != nil {
				st.phase = "install"
				st.total = total
				st.done = n
				st.Upgraded = max(st.Upgraded, n)
				st.set(StageInstalling, 0.5+0.4*ratio(n-1, total), op[2])
			}
		}
		return
	}
	if op := rePacOp.FindStringSubmatch(t); op != nil && (st.phase == "install" || st.phase == "prehooks" || st.phase == "") {
		st.phase = "install"
		st.done++
		st.Upgraded++
		st.set(StageInstalling, 0.5+0.4*ratio(st.done-1, st.total), op[2])
		return
	}
	var dl string
	if m := rePacDlNew.FindStringSubmatch(t); m != nil {
		dl = m[1]
	} else if m := rePacDlOld.FindStringSubmatch(t); m != nil {
		dl = m[1]
	}
	if dl != "" {
		if st.phase != "download" {
			// Database downloads during -Sy.
			st.set(StagePreparing, 0.02, "Synchronizing "+strings.TrimSuffix(dl, ".db"))
			return
		}
		st.dlDone++
		st.set(StageDownloading, 0.05+0.4*ratio(st.dlDone-1, st.dlTotal), pacmanPkgName(dl))
	}
}

// pacmanPkgName turns "linux-6.18.2-1-x86_64.pkg.tar.zst" into "linux".
func pacmanPkgName(s string) string {
	if out := rePacTrimFn.ReplaceAllString(s, ""); out != "" {
		return out
	}
	return s
}

// apt with -o APT::Status-Fd=1:
//
//	dlstatus:1:9.0498:Retrieving file 1 of 3
//	pmstatus:libc6:amd64:22.2222:Unpacking libc6:amd64 (2.36-9+deb12u4) over (2.36-9+deb12u3)
//	pmstatus:dpkg-exec:0:Running dpkg
func parseApt(line string, st *ProgressState) {
	t := strings.TrimSpace(CleanLine(line))
	switch {
	case strings.HasPrefix(t, "dlstatus:"):
		_, pct, msg, ok := aptStatus(t[len("dlstatus:"):])
		if !ok {
			return
		}
		st.set(StageDownloading, 0.05+0.4*pct/100, msg)
	case strings.HasPrefix(t, "pmstatus:"):
		pkg, pct, msg, ok := aptStatus(t[len("pmstatus:"):])
		if !ok {
			return
		}
		stage := StageInstalling
		if strings.HasPrefix(msg, "Running") && strings.Contains(msg, "trigger") || strings.HasPrefix(msg, "Processing triggers") {
			stage = StageHooks
		}
		if strings.HasPrefix(msg, "Installing ") || strings.HasPrefix(msg, "Upgrading ") || strings.HasPrefix(msg, "Unpacking ") {
			st.Upgraded++
		}
		cur := pkg
		if pkg == "dpkg-exec" {
			cur = msg
		}
		if i := strings.IndexByte(cur, ':'); i > 0 && pkg != "dpkg-exec" {
			cur = cur[:i]
		}
		st.set(stage, 0.45+0.5*pct/100, cur)
	case strings.HasPrefix(t, "Reading package lists"), strings.HasPrefix(t, "Building dependency tree"),
		strings.HasPrefix(t, "Calculating upgrade"):
		st.set(StagePreparing, 0.02, strings.TrimSuffix(t, "..."))
	case strings.HasPrefix(t, "Processing triggers for "):
		st.set(StageHooks, 0, strings.TrimSuffix(strings.TrimPrefix(t, "Processing triggers for "), " ..."))
	case strings.HasPrefix(t, "0 upgraded, 0 newly installed"):
		st.set(StageDone, 1, "Nothing to do")
	}
}

// aptStatus splits "<pkg>:<pct>:<msg>" where pkg may itself contain a
// ":arch" suffix.
func aptStatus(s string) (pkg string, pct float64, msg string, ok bool) {
	parts := strings.SplitN(s, ":", 4)
	// Try pkg:pct:msg first, then pkg:arch:pct:msg.
	if len(parts) >= 3 {
		if p, err := strconv.ParseFloat(parts[1], 64); err == nil {
			return parts[0], p, strings.Join(parts[2:], ":"), true
		}
	}
	if len(parts) == 4 {
		if p, err := strconv.ParseFloat(parts[2], 64); err == nil {
			return parts[0] + ":" + parts[1], p, parts[3], true
		}
	}
	return "", 0, "", false
}

var (
	reDnfDl  = regexp.MustCompile(`^[\(\[]\s*(\d+)/(\d+)[\)\]]:?\s+(\S+)`)
	reDnfTx  = regexp.MustCompile(`^(Preparing|Upgrading|Installing|Cleanup|Erasing|Obsoleting|Reinstalling|Downgrading|Verifying|Running scriptlet)\s*:\s*(\S*)\s+(\d+)/(\d+)\s*$`)
	reDnf5Tx = regexp.MustCompile(`^\[\s*(\d+)/(\d+)\]\s+(Upgrading|Installing|Removing|Verify package|Prepare transaction|Running scriptlet|Downgrading|Reinstalling)\s*(?:files)?\s*(\S*)`)
)

// parseDnf handles dnf4 and dnf5 non-tty output.
func parseDnf(line string, st *ProgressState) {
	t := strings.TrimSpace(CleanLine(line))
	switch {
	case t == "":
		return
	case strings.HasPrefix(t, "Dependencies resolved"), strings.HasPrefix(t, "Last metadata expiration"),
		strings.HasPrefix(t, "Updating and loading repositories"):
		st.set(StagePreparing, 0.03, strings.TrimSuffix(t, "."))
		return
	case strings.HasPrefix(t, "Downloading Packages"):
		st.phase = "download"
		st.set(StageDownloading, 0.05, "")
		return
	case strings.HasPrefix(t, "Running transaction"):
		st.phase = "tx"
		st.set(StageInstalling, 0.46, strings.TrimSuffix(t, "."))
		return
	case strings.HasPrefix(t, "Nothing to do"):
		st.set(StageDone, 1, "Nothing to do")
		return
	case t == "Complete!":
		st.set(StageDone, 1, "")
		return
	}
	if m := reDnfTx.FindStringSubmatch(t); m != nil {
		n, _ := strconv.Atoi(m[3])
		total, _ := strconv.Atoi(m[4])
		st.phase = "tx"
		switch m[1] {
		case "Verifying":
			st.set(StageHooks, 0.9+0.1*ratio(n, total), m[2])
		case "Running scriptlet":
			st.set(StageInstalling, 0.5+0.4*ratio(n, total), m[2])
		default:
			if m[1] != "Cleanup" && m[1] != "Preparing" {
				st.Upgraded++
			}
			st.set(StageInstalling, 0.5+0.4*ratio(n, total), m[2])
		}
		return
	}
	if m := reDnf5Tx.FindStringSubmatch(t); m != nil {
		n, _ := strconv.Atoi(m[1])
		total, _ := strconv.Atoi(m[2])
		st.phase = "tx"
		if m[3] == "Verify package" {
			st.set(StageInstalling, 0.46, m[4])
			return
		}
		if m[3] == "Upgrading" || m[3] == "Installing" {
			st.Upgraded++
		}
		st.set(StageInstalling, 0.46+0.5*ratio(n, total), m[4])
		return
	}
	if m := reDnfDl.FindStringSubmatch(t); m != nil && st.phase != "tx" {
		n, _ := strconv.Atoi(m[1])
		total, _ := strconv.Atoi(m[2])
		st.phase = "download"
		st.set(StageDownloading, 0.05+0.4*ratio(n, total), strings.TrimSuffix(m[3], ".rpm"))
	}
}

var (
	reZyAttr    = regexp.MustCompile(`(\w[\w-]*)="([^"]*)"`)
	reZyCounter = regexp.MustCompile(`\((\d+)/(\d+)\)\s*(Installing|Removing|Retrieving)?:?\s*(?:package\s+)?([^\s(,]+)?`)
)

// parseZypper handles `zypper --xmlout` progress:
//
//	<message type="info">Retrieving: vim-9.1.0-1.1.x86_64.rpm (2/5), 1.8 MiB</message>
//	<progress id="install-resolvable" name="(2/5) Installing: vim-9.1.0-1.1.x86_64" value="40"/>
//	<message type="info">Running post-transaction scripts</message>
func parseZypper(line string, st *ProgressState) {
	t := strings.TrimSpace(CleanLine(line))
	if t == "" {
		return
	}
	attrs := map[string]string{}
	for _, m := range reZyAttr.FindAllStringSubmatch(t, -1) {
		attrs[m[1]] = xmlUnescape(m[2])
	}
	text := t
	if i := strings.IndexByte(t, '>'); i >= 0 && strings.HasPrefix(t, "<message") {
		text = xmlUnescape(strings.TrimSuffix(t[i+1:], "</message>"))
	}
	switch {
	case strings.HasPrefix(t, "<progress"):
		name := attrs["name"]
		if m := reZyCounter.FindStringSubmatch(name); m != nil && m[3] == "Installing" || m != nil && m[3] == "Removing" {
			n, _ := strconv.Atoi(m[1])
			total, _ := strconv.Atoi(m[2])
			v, _ := strconv.ParseFloat(attrs["value"], 64)
			if attrs["done"] == "1" {
				v = 100
			}
			if v == 0 && attrs["done"] == "" {
				st.Upgraded++
			}
			st.set(StageInstalling, 0.46+0.44*(float64(n-1)+v/100)/float64(max(total, 1)), m[4])
			return
		}
		if strings.Contains(strings.ToLower(name), "refresh") || strings.Contains(name, "Loading repository") {
			st.set(StagePreparing, 0.02, name)
		}
	case strings.HasPrefix(t, "<download"):
		st.set(StageDownloading, 0, attrs["url"])
	case strings.HasPrefix(t, "<message"):
		switch {
		case strings.HasPrefix(text, "Retrieving:"):
			if m := reZyCounter.FindStringSubmatch(text); m != nil {
				n, _ := strconv.Atoi(m[1])
				total, _ := strconv.Atoi(m[2])
				name := strings.Fields(strings.TrimPrefix(text, "Retrieving:"))
				cur := ""
				if len(name) > 0 {
					cur = strings.TrimSuffix(name[0], ".rpm")
					if cur == "package" && len(name) > 1 {
						cur = name[1]
					}
				}
				st.set(StageDownloading, 0.05+0.4*ratio(n, total), cur)
			}
		case strings.Contains(text, "post-transaction") || strings.Contains(text, "%posttrans"):
			st.set(StageHooks, 0.9, text)
		case strings.HasPrefix(text, "Nothing to do"):
			st.set(StageDone, 1, "Nothing to do")
		case strings.HasPrefix(text, "Loading repository data"), strings.HasPrefix(text, "Reading installed packages"),
			strings.HasPrefix(text, "Computing"):
			st.set(StagePreparing, 0.03, strings.TrimSuffix(text, "..."))
		}
	}
}

func xmlUnescape(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&").Replace(s)
}

var reApkOp = regexp.MustCompile(`^\((\d+)/(\d+)\)\s+(Upgrading|Installing|Purging|Replacing|Downgrading|Reinstalling)\s+(\S+)`)

// parseApk handles `apk upgrade --no-progress`:
//
//	fetch https://dl-cdn.alpinelinux.org/alpine/v3.19/main/x86_64/APKINDEX.tar.gz
//	(1/3) Upgrading busybox (1.36.1-r2 -> 1.36.1-r5)
//	Executing busybox-1.36.1-r5.post-upgrade
//	OK: 120 MiB in 60 packages
func parseApk(line string, st *ProgressState) {
	t := strings.TrimSpace(CleanLine(line))
	switch {
	case strings.HasPrefix(t, "fetch "):
		st.set(StagePreparing, 0.03, "Fetching indexes")
	case strings.HasPrefix(t, "Executing ") && strings.Contains(t, ".trigger"):
		st.set(StageHooks, 0.92, strings.TrimPrefix(t, "Executing "))
	case strings.HasPrefix(t, "OK:"):
		st.set(StageDone, 1, "")
	default:
		if m := reApkOp.FindStringSubmatch(t); m != nil {
			n, _ := strconv.Atoi(m[1])
			total, _ := strconv.Atoi(m[2])
			st.Upgraded++
			st.set(StageInstalling, 0.1+0.8*ratio(n-1, total), m[4])
		}
	}
}
