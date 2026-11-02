// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	hostfs "github.com/kushalpandya/wargo/internal/host"
)

// Specs are fastfetch-style details about the machine. Static parts are
// computed once; package counts are refreshed every few minutes.
type Specs struct {
	Model     string         `json:"model,omitempty"`
	Board     string         `json:"board,omitempty"`
	GPUs      []string       `json:"gpus,omitempty"`
	Shell     string         `json:"shell,omitempty"`
	Init      string         `json:"init,omitempty"`
	Timezone  string         `json:"timezone,omitempty"`
	Locale    string         `json:"locale,omitempty"`
	LocalIP   string         `json:"localIp,omitempty"`
	Packages  []PackageCount `json:"packages,omitempty"`
	Desktop   *Desktop       `json:"desktop,omitempty"`
	Logo      bool           `json:"logo"`
	LogoColor string         `json:"logoColor,omitempty"`
}

type PackageCount struct {
	Manager string `json:"manager"`
	Count   int    `json:"count"`
}

var (
	specsOnce   sync.Once
	staticSpecs Specs
	pkgMu       sync.Mutex
	pkgAt       time.Time
	pkgCache    []PackageCount
)

func cleanDMI(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "oem", "o.e.m.", "default string", "to be filled by o.e.m.", "system product name", "system manufacturer", "not applicable", "none":
		return ""
	}
	return s
}

func dmi(name string) string { return cleanDMI(readStr(hostfs.Path("/sys/class/dmi/id/" + name))) }

func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" && !strings.Contains(strings.Join(out, " "), p) {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

func computeStatic() Specs {
	s := Specs{
		Model:    joinNonEmpty(dmi("sys_vendor"), dmi("product_name"), dmi("product_version")),
		Board:    joinNonEmpty(dmi("board_vendor"), dmi("board_name")),
		GPUs:     gpus(),
		Timezone: timezone(),
	}
	if s.Model == "" {
		// Raspberry Pi and other device-tree boards.
		s.Model = strings.TrimRight(readStr(hostfs.Path("/proc/device-tree/model")), "\x00")
	}
	if _, err := os.Stat(hostfs.Path("/run/systemd/system")); err == nil {
		s.Init = "systemd"
	}
	if hostfs.Container() {
		if a := hostfs.Admin(); a != nil {
			s.Shell = filepath.Base(a.Shell)
		}
		s.Locale = hostLocale()
	} else {
		if u, err := user.Current(); err == nil {
			s.Shell = loginShell(u.Username)
		}
		s.Locale = os.Getenv("LANG")
	}
	_, s.Logo = logoPath()
	s.LogoColor = osReleaseColor()
	return s
}

func loginShell(name string) string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), ":")
		if len(parts) >= 7 && parts[0] == name {
			return filepath.Base(parts[6])
		}
	}
	return ""
}

// hostLocale reads LANG from the host's locale configuration.
func hostLocale() string {
	for _, p := range []string{"/etc/locale.conf", "/etc/default/locale"} {
		b, err := hostfs.ReadFile(p)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "LANG="); ok {
				return strings.Trim(v, `"'`)
			}
		}
	}
	return ""
}

func timezone() string {
	if t := readStr(hostfs.Path("/etc/timezone")); t != "" {
		return t
	}
	if l, err := os.Readlink(hostfs.Path("/etc/localtime")); err == nil {
		if i := strings.Index(l, "zoneinfo/"); i >= 0 {
			return l[i+len("zoneinfo/"):]
		}
	}
	if hostfs.Container() {
		return ""
	}
	return time.Now().Location().String()
}

// gpus lists display controllers (PCI class 0x03) named via pci.ids.
func gpus() []string {
	devs, _ := filepath.Glob(hostfs.Path("/sys/bus/pci/devices") + "/*")
	type id struct{ vendor, device string }
	var ids []id
	for _, d := range devs {
		if !strings.HasPrefix(readStr(d+"/class"), "0x03") {
			continue
		}
		ids = append(ids, id{strings.TrimPrefix(readStr(d+"/vendor"), "0x"), strings.TrimPrefix(readStr(d+"/device"), "0x")})
	}
	if len(ids) == 0 {
		return nil
	}
	names := pciNames()
	var out []string
	for _, i := range ids {
		v, d := names[i.vendor], names[i.vendor+":"+i.device]
		switch {
		case d != "":
			out = append(out, shortVendor(v)+" "+d)
		case v != "":
			out = append(out, shortVendor(v)+" "+i.device)
		default:
			out = append(out, i.vendor+":"+i.device)
		}
	}
	return out
}

func shortVendor(v string) string {
	switch {
	case strings.HasPrefix(v, "Intel"):
		return "Intel"
	case strings.HasPrefix(v, "Advanced Micro Devices"), strings.Contains(v, "AMD/ATI"):
		return "AMD"
	case strings.HasPrefix(v, "NVIDIA"):
		return "NVIDIA"
	}
	return strings.Fields(v + " ")[0]
}

// pciNames parses pci.ids into "vendor" and "vendor:device" keys.
func pciNames() map[string]string {
	out := map[string]string{}
	for _, p := range []string{"/usr/share/hwdata/pci.ids", "/usr/share/misc/pci.ids", "/usr/share/pci.ids"} {
		b, err := hostfs.ReadFile(p)
		if err != nil {
			continue
		}
		vendor := ""
		for _, line := range bytes.Split(b, []byte("\n")) {
			if len(line) == 0 || line[0] == '#' {
				continue
			}
			if line[0] == 'C' && len(line) > 1 && line[1] == ' ' {
				break // device classes section
			}
			if line[0] != '\t' {
				if len(line) > 6 {
					vendor = string(line[:4])
					out[vendor] = strings.TrimSpace(string(line[4:]))
				}
				continue
			}
			if len(line) > 6 && line[1] != '\t' && vendor != "" {
				out[vendor+":"+string(line[1:5])] = strings.TrimSpace(string(line[5:]))
			}
		}
		break
	}
	return out
}

func countDirs(dir string) int {
	ents, err := os.ReadDir(hostfs.Path(dir))
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() {
			n++
		}
	}
	return n
}

func packageCounts() []PackageCount {
	pkgMu.Lock()
	defer pkgMu.Unlock()
	if time.Since(pkgAt) < 5*time.Minute && pkgAt.Unix() > 0 {
		return pkgCache
	}
	var out []PackageCount
	if n := countDirs("/var/lib/pacman/local"); n > 0 {
		out = append(out, PackageCount{"pacman", n})
	}
	if b, err := hostfs.ReadFile("/var/lib/dpkg/status"); err == nil {
		if n := bytes.Count(b, []byte("Status: install ok installed")); n > 0 {
			out = append(out, PackageCount{"dpkg", n})
		}
	}
	if ents, err := os.ReadDir(hostfs.Path("/lib/apk/db")); err == nil && len(ents) > 0 {
		if b, err := hostfs.ReadFile("/lib/apk/db/installed"); err == nil {
			out = append(out, PackageCount{"apk", bytes.Count(b, []byte("\nP:")) + 1})
		}
	}
	if n := countDirs("/var/lib/flatpak/app"); n > 0 {
		out = append(out, PackageCount{"flatpak", n})
	}
	if n := countDirs("/var/lib/snapd/snaps"); n > 0 {
		out = append(out, PackageCount{"snap", n})
	}
	pkgCache, pkgAt = out, time.Now()
	return out
}

// primaryIP returns the IPv4 address of the default-route interface.
func primaryIP() string {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	iface := ""
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 2 && fields[1] == "00000000" {
			iface = fields[0]
			break
		}
	}
	if iface == "" {
		return ""
	}
	ni, err := net.InterfaceByName(iface)
	if err != nil {
		return ""
	}
	addrs, _ := ni.Addrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			ones, _ := ipn.Mask.Size()
			return ipn.IP.String() + "/" + strconv.Itoa(ones)
		}
	}
	return ""
}

// GetSpecs returns machine specs.
func GetSpecs() Specs {
	specsOnce.Do(func() { staticSpecs = computeStatic() })
	s := staticSpecs
	s.Packages = packageCounts()
	s.LocalIP = primaryIP()
	s.Desktop = GetDesktop(context.Background())
	return s
}

func osReleaseField(key string) string {
	b, _ := hostfs.ReadFile("/etc/os-release")
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && k == key {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

// osReleaseColor converts ANSI_COLOR's 24-bit form ("38;2;R;G;B", or the
// trailing R;G;B triple some distros use) to a hex colour.
func osReleaseColor() string {
	parts := strings.Split(osReleaseField("ANSI_COLOR"), ";")
	if len(parts) < 3 {
		return ""
	}
	rgb := parts[len(parts)-3:]
	var b strings.Builder
	b.WriteByte('#')
	for _, p := range rgb {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return ""
		}
		b.WriteString(strconv.FormatInt(int64(n|0x100), 16)[1:])
	}
	return b.String()
}

// logoPath finds the distro logo shipped with the OS (os-release LOGO).
func logoPath() (string, bool) {
	logo, id := osReleaseField("LOGO"), osReleaseField("ID")
	var cands []string
	for _, name := range []string{logo, "distributor-logo-" + id, id + "-logo", id} {
		if name == "" || strings.ContainsAny(name, "/\\") || name == "-logo" {
			continue
		}
		cands = append(cands,
			"/usr/share/pixmaps/"+name+".svg", "/usr/share/pixmaps/"+name+".png",
			"/usr/share/icons/hicolor/scalable/apps/"+name+".svg",
			"/usr/share/icons/hicolor/256x256/apps/"+name+".png",
			"/usr/share/icons/hicolor/128x128/apps/"+name+".png",
			"/usr/share/icons/hicolor/48x48/apps/"+name+".png",
		)
	}
	for _, c := range cands {
		if st, err := hostfs.Stat(c); err == nil && st.Mode().IsRegular() && st.Size() < 2<<20 {
			return c, true
		}
	}
	return "", false
}

func serveLogo(w http.ResponseWriter, r *http.Request) {
	p, ok := logoPath()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(p, ".svg") {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	rp, err := hostfs.Resolve(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, rp)
}
