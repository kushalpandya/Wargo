// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
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
	Model string   `json:"model,omitempty"`
	Board string   `json:"board,omitempty"`
	GPUs  []string `json:"gpus,omitempty"`
	// GPUDetails adds memory to each GPU (same order as GPUs).
	GPUDetails []GPU          `json:"gpuDetails,omitempty"`
	Shell      string         `json:"shell,omitempty"`
	Init       string         `json:"init,omitempty"`
	Timezone   string         `json:"timezone,omitempty"`
	Locale     string         `json:"locale,omitempty"`
	LocalIP    string         `json:"localIp,omitempty"`
	Packages   []PackageCount `json:"packages,omitempty"`
	Desktop    *Desktop       `json:"desktop,omitempty"`
	Logo       bool           `json:"logo"`
	LogoColor  string         `json:"logoColor,omitempty"`
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
		Model:      joinNonEmpty(dmi("sys_vendor"), dmi("product_name"), dmi("product_version")),
		Board:      joinNonEmpty(dmi("board_vendor"), dmi("board_name")),
		GPUDetails: gpuDetails(),
		Timezone:   timezone(),
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
			uid, _ := strconv.Atoi(u.Uid)
			s.Shell = loginShell(u.Username, uid)
		}
		s.Locale = os.Getenv("LANG")
	}
	_, s.Logo = logoPath()
	s.LogoColor = osReleaseColor()
	applyPlatformSpecs(&s)
	return s
}

func loginShell(name string, uid int) string {
	if sh := hostfs.LoginShell(name, uid); sh != "" {
		return filepath.Base(sh)
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

// GPU is a display controller and its memory.
type GPU struct {
	Name string `json:"name"`
	// VRAM is dedicated memory in bytes (0 when unknown or shared).
	VRAM uint64 `json:"vram,omitempty"`
	// Shared: an integrated GPU using system memory.
	Shared bool `json:"shared,omitempty"`
}

// gpuDetails lists display controllers (PCI class 0x03) named via pci.ids,
// with dedicated memory where the kernel or driver reports it: amdgpu's
// mem_info_vram_total, or nvidia-smi when installed. Intel GPUs without
// reported VRAM are integrated and share system memory.
func gpuDetails() []GPU {
	devs, _ := filepath.Glob(hostfs.Path("/sys/bus/pci/devices") + "/*")
	type id struct{ vendor, device, addr string }
	var ids []id
	for _, d := range devs {
		if !strings.HasPrefix(readStr(d+"/class"), "0x03") {
			continue
		}
		ids = append(ids, id{strings.TrimPrefix(readStr(d+"/vendor"), "0x"), strings.TrimPrefix(readStr(d+"/device"), "0x"), d})
	}
	if len(ids) == 0 {
		return nil
	}
	names := pciNames()
	var nv map[string]uint64
	var out []GPU
	for _, i := range ids {
		v, d := names[i.vendor], names[i.vendor+":"+i.device]
		g := GPU{}
		switch {
		case d != "":
			g.Name = shortVendor(v) + " " + d
		case v != "":
			g.Name = shortVendor(v) + " " + i.device
		default:
			g.Name = i.vendor + ":" + i.device
		}
		if n, ok := readInt(i.addr + "/mem_info_vram_total"); ok && n > 0 {
			g.VRAM = uint64(n)
		}
		switch i.vendor {
		case "10de": // NVIDIA: the proprietary driver doesn't expose VRAM in sysfs.
			if nv == nil {
				nv = nvidiaVRAM()
			}
			if n := nv[strings.ToLower(filepath.Base(i.addr))]; n > 0 {
				g.VRAM = n
			}
		case "8086":
			g.Shared = g.VRAM == 0
		}
		out = append(out, g)
	}
	return out
}

// nvidiaVRAM maps PCI addresses (0000:01:00.0) to total memory from
// nvidia-smi, when it is installed.
func nvidiaVRAM() map[string]uint64 {
	out := map[string]uint64{}
	if _, err := hostfs.LookPath("nvidia-smi"); err != nil {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, err := hostfs.Command(ctx, "nvidia-smi", "--query-gpu=pci.bus_id,memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		addr, mib, ok := strings.Cut(l, ",")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(mib), 10, 64)
		if err != nil {
			continue
		}
		// nvidia-smi prints 00000000:01:00.0; sysfs uses 0000:01:00.0.
		a := strings.ToLower(strings.TrimSpace(addr))
		if len(a) > 12 {
			a = a[len(a)-12:]
		}
		out[a] = n << 20
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
	out = append(out, platformPackages()...)
	pkgCache, pkgAt = out, time.Now()
	return out
}

// primaryIP returns the IPv4 address of the default-route interface.
func primaryIP() string {
	iface := primaryIf()
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
	specsOnce.Do(func() {
		staticSpecs = computeStatic()
		for _, g := range staticSpecs.GPUDetails {
			staticSpecs.GPUs = append(staticSpecs.GPUs, g.Name)
		}
	})
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

// Timezone is the host's IANA timezone (e.g. "America/Toronto"), or "".
func Timezone() string { return timezone() }
