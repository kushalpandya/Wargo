// SPDX-License-Identifier: AGPL-3.0-only

package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/kushalpandya/wargo/internal/dashicons"
	"github.com/kushalpandya/wargo/internal/docker"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// Suggestion is a tile proposed from a running container.
type Suggestion struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	IconSlug  string `json:"iconSlug,omitempty"`
	Group     string `json:"group"`
	Container string `json:"container"`
	Image     string `json:"image"`
	Ports     []int  `json:"ports"`
	Exists    bool   `json:"exists"`
}

type dockerPort struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

type dockerContainer struct {
	ID         string            `json:"Id"`
	Names      []string          `json:"Names"`
	Image      string            `json:"Image"`
	State      string            `json:"State"`
	Ports      []dockerPort      `json:"Ports"`
	Labels     map[string]string `json:"Labels"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
}

// listContainers performs a read-only GET /containers/json through the
// shared Docker client.
func listContainers(ctx context.Context, socket string) ([]dockerContainer, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := docker.Shared(socket).Do(ctx, http.MethodGet, "/containers/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker: %s", resp.Status)
	}
	var out []dockerContainer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Web UI port knowledge lives in dashicons (shared with container probes).
var (
	webPorts   = dashicons.WebPorts
	knownPorts = dashicons.KnownPorts
)

// pickPort chooses the most likely web UI port among published TCP ports
// not in skip. prefer is the image's known UI port (container side), if any.
func pickPort(ports []dockerPort, prefer int, skip map[int]bool) (public int, all []int, web bool) {
	best, bestRank := 0, len(webPorts)+2
	seen := map[int]bool{}
	for _, p := range ports {
		if p.Type != "tcp" || p.PublicPort == 0 || seen[p.PublicPort] || skip[p.PublicPort] {
			continue
		}
		seen[p.PublicPort] = true
		all = append(all, p.PublicPort)
		rank := slices.Index(webPorts, p.PrivatePort)
		if rank < 0 {
			rank = len(webPorts)
		}
		if prefer != 0 && p.PrivatePort == prefer {
			rank = -1
		}
		if rank < len(webPorts) {
			web = true
		}
		if best == 0 || rank < bestRank || (rank == bestRank && p.PublicPort < best) {
			best, bestRank = p.PublicPort, rank
		}
	}
	sort.Ints(all)
	return best, all, web
}

func guessSlug(image string) string { return dashicons.GuessSlug(image) }

// prettify turns "big-bear_jellyfin" into "Big Bear Jellyfin".
func prettify(name string) string {
	name = strings.TrimPrefix(name, "/")
	f := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for i, w := range f {
		rs := []rune(w)
		rs[0] = unicode.ToUpper(rs[0])
		f[i] = string(rs)
	}
	return strings.Join(f, " ")
}

func isWargo(c dockerContainer) bool {
	if strings.Contains(strings.ToLower(c.Image), "wargo") {
		return true
	}
	for _, n := range c.Names {
		if strings.Contains(strings.ToLower(n), "wargo") {
			return true
		}
	}
	return false
}

// requestHostname returns the hostname the browser used to reach Wargo.
func requestHostname(r *http.Request) string {
	h := r.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	if h == "" {
		return "localhost"
	}
	if strings.Contains(h, ":") {
		return "[" + h + "]"
	}
	return h
}

func containerName(c dockerContainer) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

func buildSuggestions(cs []dockerContainer, host string, existing map[string]bool) []Suggestion {
	out := []Suggestion{}
	add := func(c dockerContainer, slug string, port int, all []int) {
		u := fmt.Sprintf("http://%s:%d", host, port)
		name := containerName(c)
		if all == nil {
			all = []int{port}
		}
		out = append(out, Suggestion{
			Name: prettify(name), URL: u, IconSlug: slug, Group: "Containers",
			Container: name, Image: c.Image, Ports: all, Exists: existing[u] || existing[u+"/"],
		})
	}
	running := make([]dockerContainer, 0, len(cs))
	for _, c := range cs {
		if (c.State == "" || c.State == "running") && !isWargo(c) {
			running = append(running, c)
		}
	}
	// Containers sharing another container's network (e.g. Transmission
	// behind a VPN container) are reachable on the parent's published ports.
	claimed := map[string]map[int]bool{}
	for _, c := range running {
		mode := c.HostConfig.NetworkMode
		slug := guessSlug(c.Image)
		switch {
		case mode == "host":
			if p := knownPorts[slug]; p != 0 {
				add(c, slug, p, nil)
			}
		case strings.HasPrefix(mode, "container:"):
			ref := strings.TrimPrefix(mode, "container:")
			want := knownPorts[slug]
			for _, parent := range running {
				if ref == "" || !(strings.HasPrefix(parent.ID, ref) || (len(parent.ID) >= 12 && strings.HasPrefix(ref, parent.ID)) || containerName(parent) == ref) {
					continue
				}
				for _, p := range parent.Ports {
					if p.Type == "tcp" && p.PublicPort != 0 && want != 0 && p.PrivatePort == want {
						if claimed[parent.ID] == nil {
							claimed[parent.ID] = map[int]bool{}
						}
						claimed[parent.ID][p.PublicPort] = true
						add(c, slug, p.PublicPort, nil)
						break
					}
				}
			}
		}
	}
	for _, c := range running {
		mode := c.HostConfig.NetworkMode
		if mode == "host" || strings.HasPrefix(mode, "container:") {
			continue
		}
		slug := guessSlug(c.Image)
		port, all, web := pickPort(c.Ports, knownPorts[slug], claimed[c.ID])
		if port == 0 || (len(claimed[c.ID]) > 0 && !web) {
			continue
		}
		add(c, slug, port, all)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (s *Service) handleSuggest(w http.ResponseWriter, r *http.Request) error {
	cs, err := listContainers(r.Context(), s.dockerSock)
	if err != nil {
		return httpx.Errorf(http.StatusServiceUnavailable, "docker_unavailable", "Docker is not reachable: "+err.Error())
	}
	tiles, err := s.list(r.Context())
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, t := range tiles {
		existing[t.URL] = true
	}
	httpx.OK(w, buildSuggestions(cs, requestHostname(r), existing))
	return nil
}
