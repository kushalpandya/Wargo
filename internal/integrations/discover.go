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
	// Prowlarr only.
	ConfigPath     string `json:"configPath,omitempty"`
	ConfigReadable bool   `json:"configReadable,omitempty"`
}

// Discovery is the result of GET /api/integrations/discover.
type Discovery struct {
	Docker struct {
		Available bool   `json:"available"`
		Error     string `json:"error,omitempty"`
	} `json:"docker"`
	Prowlarr     []Candidate `json:"prowlarr"`
	Transmission []Candidate `json:"transmission"`
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

func discover(cs []dockerContainer) (prowlarr, transmission []Candidate) {
	prowlarr, transmission = []Candidate{}, []Candidate{}
	for _, c := range cs {
		img := strings.ToLower(c.Image)
		switch {
		case strings.Contains(img, "prowlarr"):
			cand := candidate(c, cs, 9696, "")
			for _, m := range c.Mounts {
				if m.Destination == "/config" && m.Type == "bind" && m.Source != "" {
					cand.ConfigPath = filepath.Join(filepath.Clean(m.Source), "config.xml")
					cand.ConfigReadable = readable(cand.ConfigPath)
				}
			}
			prowlarr = append(prowlarr, cand)
		case strings.Contains(img, "transmission") && !strings.Contains(img, "exporter"):
			transmission = append(transmission, candidate(c, cs, 9091, "/transmission/rpc"))
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

// readable reports whether the admin user can read the host file path
// (checked with the admin's identity in container mode).
func readable(path string) bool {
	return host.AsUser(func() error {
		f, err := openConfigFile(path)
		if err != nil {
			return err
		}
		return f.Close()
	}) == nil
}

// openConfigFile opens a container's config file given as a host path.
// The file lives in a folder the container itself can write to, so it is
// treated as hostile: the folder's symlinks are resolved inside the host
// root (container mode), the file itself must not be a symlink (O_NOFOLLOW,
// checked atomically on open rather than with a racy Lstat), O_NONBLOCK
// keeps a FIFO from hanging the request, and the descriptor must be a
// regular file. Callers run this with the admin's identity.
func openConfigFile(path string) (*os.File, error) {
	dir, err := host.Resolve(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, filepath.Base(path)), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, errors.New("config.xml is a symbolic link")
		}
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("config.xml is not a regular file")
	}
	return f, nil
}
