// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/docker"
	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/netx"
)

// dockerContainer is the subset of GET /containers/json we use.
type dockerContainer struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
	Image string   `json:"Image"`
	State string   `json:"State"`
	Ports []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
}

func (c dockerContainer) name() string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

// listContainers performs a read-only GET /containers/json on the docker socket.
func listContainers(ctx context.Context, socket string) ([]dockerContainer, error) {
	if socket == "" {
		return nil, errors.New("docker socket not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := docker.Shared(socket).Do(ctx, http.MethodGet, "/containers/json", nil)
	if err != nil {
		var ue interface{ Unwrap() error }
		if errors.As(err, &ue) {
			err = ue.Unwrap()
		}
		return nil, fmt.Errorf("docker: %w", err)
	}
	defer netx.Drain(res)
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker: HTTP %d", res.StatusCode)
	}
	var out []dockerContainer
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	return out, nil
}

// Candidate is a detected service container.
type Candidate struct {
	Container string `json:"container"`
	Image     string `json:"image"`
	State     string `json:"state"`
	URL       string `json:"url,omitempty"`
	// NetworkVia names the container whose network namespace is shared
	// (network_mode: container:<id>).
	NetworkVia string `json:"networkVia,omitempty"`
	Note       string `json:"note,omitempty"`
	// Prowlarr (config.xml) and Plex (Preferences.xml) only.
	ConfigPath     string `json:"configPath,omitempty"`
	ConfigReadable bool   `json:"configReadable,omitempty"`
	// configRoot is the /config bind source; configRel is ConfigPath
	// below it, the part the container controls.
	configRoot, configRel string
}

// setConfig points c at rel below the bind source root.
func (c *Candidate) setConfig(root, rel string) {
	c.configRoot, c.configRel = root, rel
	c.ConfigPath = filepath.Join(root, rel)
	c.ConfigReadable = readable(root, rel)
}

// Discovery is the result of GET /api/integrations/discover.
type Discovery struct {
	Docker struct {
		Available bool   `json:"available"`
		Error     string `json:"error,omitempty"`
	} `json:"docker"`
	Prowlarr     []Candidate `json:"prowlarr"`
	Transmission []Candidate `json:"transmission"`
	Plex         []Candidate `json:"plex"`
	// TransmissionFromProwlarr is the Transmission client configured in
	// Prowlarr (when Prowlarr is configured and reachable).
	TransmissionFromProwlarr *TransmissionHint `json:"transmissionFromProwlarr,omitempty"`
}

// TransmissionHint suggests Transmission settings.
type TransmissionHint struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
}

// plexOthers are Plex companion tools whose images contain "plex" but are
// not a server.
var plexOthers = []string{"meta-manager", "kometa", "tautulli", "overseerr", "jellyseerr", "exporter", "plextraktsync", "autoscan", "ombi"}

func isPlexServer(img string) bool {
	if !strings.Contains(img, "plex") {
		return false
	}
	for _, o := range plexOthers {
		if strings.Contains(img, o) {
			return false
		}
	}
	return true
}

// plexPrefs are where images keep Preferences.xml inside /config
// (linuxserver, plexinc and hotio use the first).
var plexPrefs = []string{filepath.Join("Library", "Application Support", "Plex Media Server", "Preferences.xml"), "Preferences.xml"}

func discover(cs []dockerContainer) (prowlarr, transmission, plex []Candidate) {
	prowlarr, transmission, plex = []Candidate{}, []Candidate{}, []Candidate{}
	for _, c := range cs {
		img := strings.ToLower(c.Image)
		switch {
		case strings.Contains(img, "prowlarr"):
			cand := candidate(c, cs, 9696, "")
			for _, m := range c.Mounts {
				if m.Destination == "/config" && m.Type == "bind" && m.Source != "" {
					cand.setConfig(filepath.Clean(m.Source), "config.xml")
				}
			}
			prowlarr = append(prowlarr, cand)
		case strings.Contains(img, "transmission") && !strings.Contains(img, "exporter"):
			transmission = append(transmission, candidate(c, cs, 9091, "/transmission/rpc"))
		case isPlexServer(img):
			cand := candidate(c, cs, 32400, "")
			for _, m := range c.Mounts {
				if m.Destination != "/config" || m.Type != "bind" || m.Source == "" {
					continue
				}
				// first readable wins; otherwise report the usual location
				root := filepath.Clean(m.Source)
				i := slices.IndexFunc(plexPrefs, func(rel string) bool { return readable(root, rel) })
				cand.setConfig(root, plexPrefs[max(i, 0)])
			}
			plex = append(plex, cand)
		}
	}
	return
}

func candidate(c dockerContainer, all []dockerContainer, port int, path string) Candidate {
	cand := Candidate{Container: c.name(), Image: c.Image, State: c.State}
	netc := c
	if target, ok := strings.CutPrefix(c.HostConfig.NetworkMode, "container:"); ok {
		found := false
		for _, o := range all {
			if o.ID == target || (len(target) >= 12 && strings.HasPrefix(o.ID, target)) || o.name() == strings.TrimPrefix(target, "/") {
				netc, found = o, true
				break
			}
		}
		if !found {
			cand.Note = "Shares the network of a container that is not running"
			return cand
		}
		cand.NetworkVia = netc.name()
	}
	// With host networking the service listens on the host directly.
	if netc.HostConfig.NetworkMode == "host" {
		cand.URL = "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + path
		cand.Note = fmt.Sprintf("Assumes the default port %d", port)
		return cand
	}
	host, pub := "", 0
	for _, p := range netc.Ports {
		if p.PrivatePort == port && p.PublicPort > 0 && p.Type == "tcp" {
			host, pub = p.IP, p.PublicPort
			if host == "0.0.0.0" || host == "" {
				break
			}
		}
	}
	if pub == 0 {
		cand.Note = fmt.Sprintf("Port %d is not published on the host", port)
		return cand
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	cand.URL = "http://" + net.JoinHostPort(host, strconv.Itoa(pub)) + path
	return cand
}

// readable reports whether the admin user can read the config file rel
// below the bind source root (checked with the admin's identity in
// container mode).
func readable(root, rel string) bool {
	return host.AsUser(func() error {
		f, err := openConfigFile(root, rel)
		if err != nil {
			return err
		}
		return f.Close()
	}) == nil
}

func isSymlink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}

// readConfigSecret reads a secret from the config file rel below the bind
// source root, as the admin user.
func readConfigSecret(root, rel string, parse func(io.Reader) (string, error)) (secret string, err error) {
	err = host.AsUser(func() error {
		f, err := openConfigFile(root, rel)
		if err != nil {
			return err
		}
		defer f.Close()
		secret, err = parse(io.LimitReader(f, 256<<10))
		return err
	})
	return secret, err
}

// openConfigFile opens a container's config file: rel below root, the host
// path of the container's /config bind mount. Everything below root is
// writable by the container itself, so it is treated as hostile: root is
// resolved inside the host root (container mode), then each component of
// rel is opened relative to its parent with O_NOFOLLOW, so no symlink below
// root is followed. Doing this with descriptors (openat) instead of Lstat
// checks leaves no window to swap a folder for a link between check and
// use. Only root itself, which the container can't replace, is resolved by
// path. O_NONBLOCK keeps a FIFO from hanging the request, and the file must
// be a regular file. Callers run this with the admin's identity.
func openConfigFile(root, rel string) (*os.File, error) {
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	if filepath.IsAbs(rel) || slices.Contains(parts, "..") || slices.Contains(parts, ".") {
		return nil, errors.New("invalid config path")
	}
	dir, err := host.Resolve(root)
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: root, Err: err}
	}
	last := len(parts) - 1
	for i, name := range parts {
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
		if i < last {
			flags |= syscall.O_DIRECTORY
		} else {
			flags |= syscall.O_NONBLOCK | syscall.O_NOCTTY
		}
		next, err := openat(fd, name, flags)
		syscall.Close(fd)
		if err != nil {
			sub := filepath.Join(parts[:i+1]...)
			// Linux reports a symlinked folder as ENOTDIR; Lstat only
			// picks the message.
			if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) && isSymlink(filepath.Join(dir, sub)) {
				return nil, errors.New(name + " is a symbolic link")
			}
			return nil, &os.PathError{Op: "open", Path: filepath.Join(root, sub), Err: err}
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), filepath.Join(root, rel))
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.New(parts[last] + " is not a regular file")
	}
	return f, nil
}
