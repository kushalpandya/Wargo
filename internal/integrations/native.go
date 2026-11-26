// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/netx"
	"github.com/kushalpandya/wargo/internal/plist"
)

// Natively installed apps are found from config files at fixed locations
// and confirmed by probing 127.0.0.1. Nothing else on the host is read:
// the import endpoint only accepts a path this list produced.

// nativeApps are the apps Detect looks for on the host.
var nativeApps = []string{"prowlarr", "transmission", "plex"}

// transmissionRPC is Transmission's default RPC path.
const transmissionRPC = "/transmission/rpc"

// defaultConf is where each app listens unless its config says otherwise.
func defaultConf(app string) nativeConf {
	switch app {
	case "prowlarr":
		return nativeConf{Port: 9696}
	case "transmission":
		return nativeConf{Port: 9091, RPCPath: transmissionRPC}
	case "plex":
		return nativeConf{Port: 32400}
	}
	return nativeConf{}
}

func validPort(p int64) bool { return p > 0 && p < 65536 }

// nativeLoc is a config file of a native install. Root is a folder only
// root or the admin can replace; Rel is the file below it, opened without
// following symlinks. Folders owned by service users (everything below
// /var/lib/<app>) therefore belong in Rel, never in Root.
type nativeLoc struct {
	App    string // prowlarr | transmission | plex
	Root   string
	Rel    string
	Format string // prowlarr-xml | transmission-json | transmission-plist | plex-xml | plex-plist
}

// nativeLocations lists where native installs keep their config. home is
// the admin account's home folder; without one, only system-wide
// locations are listed.
func nativeLocations(home, goos string) []nativeLoc {
	var locs []nativeLoc
	if home != "" {
		locs = append(locs, nativeLoc{App: "prowlarr", Root: filepath.Join(home, ".config", "Prowlarr"), Rel: "config.xml", Format: "prowlarr-xml"})
		if goos == "darwin" {
			prefs := filepath.Join(home, "Library", "Preferences")
			locs = append(locs,
				nativeLoc{App: "transmission", Root: prefs, Rel: "org.m0k.transmission.plist", Format: "transmission-plist"},
				nativeLoc{App: "plex", Root: prefs, Rel: "com.plexapp.plexmediaserver.plist", Format: "plex-plist"})
		} else {
			locs = append(locs, nativeLoc{App: "transmission", Root: filepath.Join(home, ".config", "transmission-daemon"), Rel: "settings.json", Format: "transmission-json"})
		}
	}
	if goos == "darwin" {
		return locs
	}
	return append(locs,
		nativeLoc{App: "prowlarr", Root: "/var/lib", Rel: "prowlarr/config.xml", Format: "prowlarr-xml"},
		nativeLoc{App: "transmission", Root: "/etc/transmission-daemon", Rel: "settings.json", Format: "transmission-json"},
		nativeLoc{App: "transmission", Root: "/var/lib", Rel: "transmission/.config/transmission-daemon/settings.json", Format: "transmission-json"},
		nativeLoc{App: "plex", Root: "/var/lib", Rel: "plexmediaserver/Library/Application Support/Plex Media Server/Preferences.xml", Format: "plex-xml"})
}

// nativeConf is where a native install listens, from its config file.
type nativeConf struct {
	Port    int
	PortSet bool   // the file named the port (else Port is the default)
	Base    string // Prowlarr URL base, e.g. "/prowlarr"
	RPCPath string // Transmission RPC path, e.g. "/transmission/rpc"
	ID      string // Plex server's machine identifier
}

// readNativeConf reads a native config file as the admin user. ok is false
// when the file is missing, unreadable or unparsable. Values the file
// leaves out are the app's defaults.
func readNativeConf(l nativeLoc) (c nativeConf, ok bool) {
	c = defaultConf(l.App)
	err := readConfig(l.Root, l.Rel, func(r io.Reader) error {
		switch l.Format {
		case "prowlarr-xml":
			var x struct {
				Port    int    `xml:"Port"`
				UrlBase string `xml:"UrlBase"`
			}
			if err := xml.NewDecoder(r).Decode(&x); err != nil {
				return err
			}
			if validPort(int64(x.Port)) {
				c.Port, c.PortSet = x.Port, true
			}
			if b := strings.Trim(x.UrlBase, "/ "); b != "" {
				c.Base = "/" + b
			}
		case "transmission-json":
			var j struct {
				Port int    `json:"rpc-port"`
				URL  string `json:"rpc-url"`
			}
			if err := json.NewDecoder(r).Decode(&j); err != nil {
				return err
			}
			if validPort(int64(j.Port)) {
				c.Port, c.PortSet = j.Port, true
			}
			if j.URL != "" {
				c.RPCPath = path.Join("/", j.URL, "rpc")
			}
		case "transmission-plist":
			m, err := decodePrefs(r)
			if err != nil {
				return err
			}
			if p, ok := m["RPCPort"].(int64); ok && validPort(p) {
				c.Port, c.PortSet = int(p), true
			}
		case "plex-xml":
			var x struct {
				XMLName xml.Name `xml:"Preferences"`
				ID      string   `xml:"ProcessedMachineIdentifier,attr"`
			}
			if err := xml.NewDecoder(r).Decode(&x); err != nil {
				return err
			}
			c.ID = x.ID
		case "plex-plist":
			m, err := decodePrefs(r)
			if err != nil {
				return err
			}
			c.ID, _ = m["ProcessedMachineIdentifier"].(string)
		default:
			return errors.New("unknown config format")
		}
		return nil
	})
	return c, err == nil
}

// decodePrefs reads a macOS preferences plist into a dictionary.
func decodePrefs(r io.Reader) (map[string]any, error) {
	v, err := plist.DecodeAny(r)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("plist is not a dictionary")
	}
	return m, nil
}

// newProbeClient is the client probes use: no redirects (a redirect is an
// answer to judge, not a place to go) and no proxy.
func newProbeClient() *http.Client {
	tr := netx.NewTransport(probeTimeout)
	tr.DisableKeepAlives = true
	return &http.Client{Timeout: probeTimeout, Transport: tr, CheckRedirect: netx.NoRedirects}
}

// probeBodyMax caps how much of a probe reply is read.
const probeBodyMax = 64 << 10

// probeID reports whether the app answers at url, judged by a reply only
// that app gives, and what it says it is: Plex's machine identifier,
// empty for the others. For Transmission url is the RPC URL, otherwise the
// base.
func probeID(ctx context.Context, client *http.Client, app, url string) (bool, string) {
	switch app {
	case "plex":
		res, body, ok := get(ctx, client, strings.TrimSuffix(url, "/")+"/identity")
		if !ok || res.StatusCode != http.StatusOK {
			return false, ""
		}
		var mc struct {
			XMLName xml.Name `xml:"MediaContainer"`
			ID      string   `xml:"machineIdentifier,attr"`
		}
		if xml.Unmarshal(body, &mc) != nil || mc.ID == "" {
			return false, ""
		}
		return true, mc.ID
	case "transmission":
		res, _, ok := get(ctx, client, url)
		return ok && (res.StatusCode == http.StatusConflict && res.Header.Get("X-Transmission-Session-Id") != "" ||
			res.StatusCode == http.StatusUnauthorized && strings.Contains(res.Header.Get("WWW-Authenticate"), "Transmission")), ""
	case "prowlarr":
		// /ping alone also answers for Sonarr and Radarr: the page names
		// the app. With a login set up (Prowlarr asks for one on first
		// run), / redirects to the login page, which names it too, or
		// Basic auth answers 401 with a Prowlarr realm.
		base := strings.TrimSuffix(url, "/")
		res, body, ok := get(ctx, client, base+"/")
		switch {
		case !ok:
			return false, ""
		case res.StatusCode == http.StatusOK:
			return strings.Contains(string(body), "Prowlarr"), ""
		case res.StatusCode == http.StatusUnauthorized:
			return strings.Contains(res.Header.Get("WWW-Authenticate"), "Prowlarr"), ""
		case res.StatusCode >= 300 && res.StatusCode < 400 && strings.Contains(res.Header.Get("Location"), "/login"):
			res, body, ok = get(ctx, client, base+"/login")
			return ok && res.StatusCode == http.StatusOK && strings.Contains(string(body), "Prowlarr"), ""
		}
		return false, ""
	}
	return false, ""
}

func get(ctx context.Context, client *http.Client, url string) (*http.Response, []byte, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, false
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, nil, false
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, probeBodyMax))
	return res, body, err == nil
}

// hostNative is where to look on this host: the known config locations,
// with ~ as the admin account's home, and each app's default port.
func hostNative() ([]nativeLoc, map[string]int) {
	home := ""
	if a := host.Admin(); a != nil {
		home = a.Home
	}
	defaults := map[string]int{}
	for _, app := range nativeApps {
		defaults[app] = defaultConf(app).Port
	}
	return nativeLocations(home, runtime.GOOS), defaults
}

// probeTimeout bounds each probe; all of them run at once.
const probeTimeout = time.Second

// nativeTarget is a URL to probe and the config file that named it, if any.
type nativeTarget struct {
	app  string
	url  string
	port int
	loc  *nativeLoc
	conf nativeConf // of loc
	ok   bool
	id   string // what the app said it is (Plex)
}

func appURL(app, hostIP string, c nativeConf) string {
	u := "http://" + net.JoinHostPort(hostIP, strconv.Itoa(c.Port))
	switch app {
	case "prowlarr":
		return u + c.Base
	case "transmission":
		return u + c.RPCPath
	}
	return u
}

// configExists reports whether a native config file is there, readable by
// the admin or not. It walks the path as openConfigFile does (as the
// admin, no symlinks below Root), and only a refusal to open the file
// itself counts as "there but unreadable".
func configExists(l nativeLoc) bool {
	full := filepath.Join(l.Root, l.Rel)
	err := host.AsUser(func() error {
		f, err := openConfigFile(l.Root, l.Rel)
		if err == nil {
			f.Close()
		}
		return err
	})
	var pe *os.PathError
	return err == nil || errors.As(err, &pe) && pe.Path == full && errors.Is(err, fs.ErrPermission)
}

// nativeCandidates probes hostIP for each app at its default port (from
// defaults; 0 or missing skips it) and at the port each readable config in
// locs names. A confirmed URL is a native candidate unless a container in
// have already has it. A config file whose app does not answer is listed
// with a note. Prowlarr and Plex candidates carry their config file for
// import.
func nativeCandidates(ctx context.Context, locs []nativeLoc, hostIP string, defaults map[string]int, have []Candidate) (prowlarr, transmission, plex []Candidate) {
	taken := map[string]bool{}
	for _, c := range have {
		if c.URL != "" {
			taken[c.URL] = true
		}
	}
	var targets []*nativeTarget
	seen := map[string]*nativeTarget{}
	add := func(app string, c nativeConf, l *nativeLoc) {
		u := appURL(app, hostIP, c)
		if t := seen[u]; t != nil {
			if t.loc == nil {
				t.loc, t.conf = l, c
			}
			return
		}
		t := &nativeTarget{app: app, url: u, port: c.Port, loc: l, conf: c}
		seen[u] = t
		targets = append(targets, t)
	}
	// unread are config files that exist but the admin cannot read.
	var unread []nativeLoc
	for i := range locs {
		l := &locs[i]
		if c, ok := readNativeConf(*l); ok {
			if p := defaults[l.App]; !c.PortSet && p > 0 {
				c.Port = p
			}
			add(l.App, c, l)
		} else if configExists(*l) {
			unread = append(unread, *l)
		}
	}
	for _, app := range nativeApps {
		if p := defaults[app]; p > 0 {
			c := defaultConf(app)
			c.Port = p
			add(app, c, nil)
		}
	}

	client := newProbeClient()
	var wg sync.WaitGroup
	for _, t := range targets {
		if !taken[t.url] {
			wg.Go(func() { t.ok, t.id = probeID(ctx, client, t.app, t.url) })
		}
	}
	wg.Wait()

	out := map[string]*[]Candidate{"prowlarr": &prowlarr, "transmission": &transmission, "plex": &plex}
	used := map[nativeLoc]bool{}
	// emit adds a candidate for app; url is empty when nothing answered.
	// Prowlarr and Plex carry their config file (readable or not) for
	// import; Transmission has no secret to import.
	emit := func(app, url, note string, l *nativeLoc, readable bool) {
		c := Candidate{Source: "native", Container: "Native install", URL: url, Note: note}
		if url != "" {
			c.State = "running"
		}
		if l != nil {
			used[*l] = true
			if app != "transmission" {
				c.setConfigKnown(l.Root, l.Rel, readable)
			}
		}
		*out[app] = append(*out[app], c)
	}
	for _, t := range targets {
		if !t.ok || taken[t.url] {
			continue
		}
		// A Plex config goes only with the server it belongs to: anything
		// else on the port (another server, or a squatter while Plex is
		// stopped) would be sent the token on import.
		if t.loc != nil && t.app == "plex" && t.conf.ID != t.id {
			emit(t.app, t.url, "", nil, false)
			emit(t.app, "", "Another Plex server answers on port "+strconv.Itoa(t.port), t.loc, true)
			continue
		}
		if t.loc != nil {
			emit(t.app, t.url, "", t.loc, true)
			continue
		}
		// Found on the default port: point at an unread config if one exists.
		var l *nativeLoc
		for i := range unread {
			if unread[i].App == t.app && !used[unread[i]] {
				l = &unread[i]
				break
			}
		}
		emit(t.app, t.url, "", l, false)
	}
	// Config files whose app did not answer.
	for _, t := range targets {
		if t.loc != nil && !t.ok && !taken[t.url] && !used[*t.loc] {
			emit(t.app, "", "Not answering on port "+strconv.Itoa(t.port), t.loc, true)
		}
	}
	for i := range unread {
		if !used[unread[i]] {
			emit(unread[i].App, "", "Installed, but not answering on its default port", &unread[i], false)
		}
	}
	return
}
