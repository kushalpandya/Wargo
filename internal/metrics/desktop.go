// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/host"
)

// Desktop describes the graphical session on the host, if any.
type Desktop struct {
	Name    string `json:"name"`              // "KDE Plasma", "GNOME", "Hyprland"...
	Version string `json:"version,omitempty"` // "6.7.5"
	Session string `json:"session,omitempty"` // "wayland" | "x11" | "tty"
	WM      string `json:"wm,omitempty"`      // window manager / compositor process
	User    string `json:"user,omitempty"`    // session owner
}

// desktopDef maps a desktop to the processes that reveal it and the
// packages whose version is the desktop's version.
type desktopDef struct {
	name     string
	procs    []string // any of these running means this desktop
	wms      []string // compositor / window manager process names
	packages []string // tried in order across package managers
	xdg      []string // XDG_CURRENT_DESKTOP / logind Desktop values
}

var desktops = []desktopDef{
	{"KDE Plasma", []string{"plasmashell"}, []string{"kwin_wayland", "kwin_x11", "kwin"}, []string{"plasma-workspace", "plasma-desktop"}, []string{"KDE", "plasma"}},
	{"GNOME", []string{"gnome-shell"}, []string{"gnome-shell", "mutter"}, []string{"gnome-shell"}, []string{"GNOME", "gnome"}},
	{"COSMIC", []string{"cosmic-session", "cosmic-comp"}, []string{"cosmic-comp"}, []string{"cosmic-session", "cosmic-comp"}, []string{"COSMIC"}},
	{"Cinnamon", []string{"cinnamon"}, []string{"cinnamon", "muffin"}, []string{"cinnamon"}, []string{"X-Cinnamon", "Cinnamon"}},
	{"Xfce", []string{"xfce4-session"}, []string{"xfwm4"}, []string{"xfce4-session"}, []string{"XFCE"}},
	{"MATE", []string{"mate-session"}, []string{"marco"}, []string{"mate-session-manager"}, []string{"MATE"}},
	{"Budgie", []string{"budgie-panel", "budgie-wm"}, []string{"budgie-wm", "labwc"}, []string{"budgie-desktop"}, []string{"Budgie"}},
	{"LXQt", []string{"lxqt-session"}, []string{"openbox", "kwin_x11", "labwc"}, []string{"lxqt-session"}, []string{"LXQt"}},
	{"Deepin", []string{"startdde", "dde-desktop"}, []string{"kwin_x11", "deepin-kwin_x11"}, []string{"startdde", "dde-session-shell"}, []string{"Deepin", "DDE"}},
	{"Hyprland", []string{"Hyprland", "hyprland"}, []string{"Hyprland"}, []string{"hyprland"}, []string{"Hyprland"}},
	{"Sway", []string{"sway"}, []string{"sway"}, []string{"sway"}, []string{"sway"}},
	{"niri", []string{"niri"}, []string{"niri"}, []string{"niri"}, []string{"niri"}},
	{"Wayfire", []string{"wayfire"}, []string{"wayfire"}, []string{"wayfire"}, []string{"Wayfire"}},
	{"i3", []string{"i3"}, []string{"i3"}, []string{"i3-wm", "i3"}, []string{"i3"}},
	{"bspwm", []string{"bspwm"}, []string{"bspwm"}, []string{"bspwm"}, []string{"bspwm"}},
	{"Openbox", []string{"openbox"}, []string{"openbox"}, []string{"openbox"}, []string{"Openbox"}},
}

var (
	desktopMu  sync.Mutex
	desktopAt  time.Time
	desktopVal *Desktop
	versionRe  = regexp.MustCompile(`\d+(\.\d+)+`)
)

// runningProcs returns the set of process names (comm) on the host.
func runningProcs() map[string]bool {
	out := map[string]bool{}
	ents, _ := os.ReadDir(host.Path("/proc"))
	for _, e := range ents {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		if comm := readStr(host.Path(filepath.Join("/proc", e.Name(), "comm"))); comm != "" {
			out[comm] = true
		}
	}
	return out
}

type session struct {
	typ, desktop, user string
	active             bool
}

// graphicalSession asks logind for the active graphical session.
func graphicalSession(ctx context.Context) *session {
	out, err := host.Command(ctx, "loginctl", "list-sessions", "--no-legend").Output()
	if err != nil {
		return nil
	}
	var best *session
	bestScore := -1
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		props, err := host.Command(ctx, "loginctl", "show-session", f[0], "-p", "Type", "-p", "Desktop", "-p", "Name", "-p", "Active", "-p", "Class").Output()
		if err != nil {
			continue
		}
		s := &session{}
		class := ""
		for _, kv := range strings.Split(string(props), "\n") {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "Type":
				s.typ = v
			case "Desktop":
				s.desktop = v
			case "Name":
				s.user = v
			case "Active":
				s.active = v == "yes"
			case "Class":
				class = v
			}
		}
		if class != "user" {
			continue
		}
		// Prefer graphical sessions, then active ones.
		score := 0
		if s.typ == "x11" || s.typ == "wayland" || s.typ == "mir" {
			score += 2
		}
		if s.active {
			score++
		}
		if score > bestScore {
			best, bestScore = s, score
		}
	}
	return best
}

// packageVersion queries the host package database without root.
func packageVersion(ctx context.Context, pkgs []string) string {
	for _, p := range pkgs {
		var out []byte
		var err error
		switch {
		case hasHostBin("pacman"):
			out, err = host.Command(ctx, "pacman", "-Q", p).Output()
			if err == nil {
				if f := strings.Fields(string(out)); len(f) == 2 {
					out = []byte(f[1])
				}
			}
		case hasHostBin("dpkg-query"):
			out, err = host.Command(ctx, "dpkg-query", "-W", "-f=${Version}", p).Output()
		case hasHostBin("rpm"):
			out, err = host.Command(ctx, "rpm", "-q", "--qf", "%{VERSION}", p).Output()
		case hasHostBin("apk"):
			out, err = host.Command(ctx, "apk", "info", "-e", "-v", p).Output()
		default:
			return ""
		}
		if err != nil {
			continue
		}
		// Strip epochs and distro suffixes: "4:6.7.5-1ubuntu1" -> "6.7.5".
		if v := versionRe.FindString(string(out)); v != "" {
			return v
		}
	}
	return ""
}

func hasHostBin(name string) bool {
	_, err := host.LookPath(name)
	return err == nil
}

func detectDesktop(ctx context.Context) *Desktop {
	procs := runningProcs()
	sess := graphicalSession(ctx)
	var def *desktopDef
	for i := range desktops {
		d := &desktops[i]
		for _, p := range d.procs {
			if procs[p] {
				def = d
				break
			}
		}
		if def != nil {
			break
		}
	}
	if def == nil && sess != nil && sess.desktop != "" {
		for i := range desktops {
			for _, x := range desktops[i].xdg {
				if strings.EqualFold(x, sess.desktop) {
					def = &desktops[i]
				}
			}
		}
	}
	if def == nil {
		if sess == nil || (sess.typ != "x11" && sess.typ != "wayland") {
			return nil // headless
		}
		return &Desktop{Name: "Unknown", Session: sess.typ, User: sess.user}
	}
	d := &Desktop{Name: def.name, Version: packageVersion(ctx, def.packages)}
	for _, w := range def.wms {
		if procs[w] {
			d.WM = w
			break
		}
	}
	if sess != nil {
		d.Session, d.User = sess.typ, sess.user
	}
	if d.Session == "" || d.Session == "unspecified" {
		switch {
		case procs["Xorg"] || procs["X"]:
			d.Session = "x11"
		case strings.Contains(d.WM, "wayland") || def.name == "Hyprland" || def.name == "Sway" || def.name == "niri" || def.name == "Wayfire":
			d.Session = "wayland"
		}
	}
	return d
}

// GetDesktop returns the host's desktop session, cached for two minutes.
func GetDesktop(ctx context.Context) *Desktop {
	desktopMu.Lock()
	defer desktopMu.Unlock()
	if time.Since(desktopAt) < 2*time.Minute && !desktopAt.IsZero() {
		return desktopVal
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	desktopVal, desktopAt = detectDesktop(ctx), time.Now()
	if desktopVal == nil {
		// Headless or not detected yet: look again sooner.
		desktopAt = desktopAt.Add(-105 * time.Second)
	}
	return desktopVal
}
