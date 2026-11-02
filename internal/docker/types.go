// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	refRe   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)
	imageRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]{0,511}$`)
)

// validRef accepts container ids, id prefixes and names.
func validRef(s string) bool { return refRe.MatchString(s) }

// validImage accepts image references like "lscr.io/linuxserver/plex:latest".
func validImage(s string) bool { return imageRe.MatchString(s) && !strings.Contains(s, "..") }

// --- Docker API shapes (subset) ---

type apiPort struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

type apiContainer struct {
	ID         string            `json:"Id"`
	Names      []string          `json:"Names"`
	Image      string            `json:"Image"`
	ImageID    string            `json:"ImageID"`
	Command    string            `json:"Command"`
	Created    int64             `json:"Created"`
	State      string            `json:"State"`
	Status     string            `json:"Status"`
	Ports      []apiPort         `json:"Ports"`
	Labels     map[string]string `json:"Labels"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
}

// --- Wargo shapes (see web/src/features/containers/types.ts) ---

type Port struct {
	IP      string `json:"ip,omitempty"`
	Private int    `json:"private"`
	Public  int    `json:"public,omitempty"`
	Type    string `json:"type"`
}

type Compose struct {
	Project     string `json:"project"`
	Service     string `json:"service"`
	WorkingDir  string `json:"workingDir,omitempty"`
	ConfigFiles string `json:"configFiles,omitempty"`
}

type Container struct {
	ID          string `json:"id"` // 12-char short id
	FullID      string `json:"fullId"`
	Name        string `json:"name"`
	Image       string `json:"image"`
	ImageID     string `json:"imageId"`
	Command     string `json:"command,omitempty"`
	State       string `json:"state"`
	Status      string `json:"status"`
	Health      string `json:"health,omitempty"`
	Created     int64  `json:"created"` // unix ms
	Ports       []Port `json:"ports"`
	NetworkMode string `json:"networkMode"`
	// For network mode "container:<id>": the target's name, id and ports.
	NetworkContainer   string `json:"networkContainer,omitempty"`
	NetworkContainerID string `json:"networkContainerId,omitempty"`
	NetworkPorts       []Port `json:"networkPorts,omitempty"`
	// Names of containers sharing this container's network namespace.
	NetworkUsers []string `json:"networkUsers,omitempty"`
	Compose      *Compose `json:"compose,omitempty"`
	CasaOS       bool     `json:"casaos"`
	// Self marks Wargo's own container (container mode).
	Self            bool  `json:"self,omitempty"`
	UpdateAvailable *bool `json:"updateAvailable"`
	// UpdateCheckedAt is when the image was last checked (unix ms, 0 = never).
	UpdateCheckedAt int64 `json:"updateCheckedAt,omitempty"`
	// Version is a best-effort human version of the image (labels, then
	// tag, then short image id).
	Version string `json:"version"`
	// ImageRef is the image without tag/digest; Tag defaults to "latest"
	// ("" for digest-pinned refs and images referenced by id).
	ImageRef string `json:"imageRef"`
	Tag      string `json:"tag"`
	// Icon is a Wargo URL for the container's logo; omitted when no
	// candidate exists. It may still 404 (show a fallback).
	Icon string `json:"icon,omitempty"`
	// Probe is Wargo's own reachability check, set only for running
	// containers without a Docker health check.
	Probe *Probe `json:"probe,omitempty"`
}

const (
	labelProject    = "com.docker.compose.project"
	labelService    = "com.docker.compose.service"
	labelWorkingDir = "com.docker.compose.project.working_dir"
	labelConfig     = "com.docker.compose.project.config_files"
	casaosDir       = "/var/lib/casaos"
)

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// parseHealth extracts the health state from list Status text such as
// "Up 18 hours (healthy)".
func parseHealth(status string) string {
	switch {
	case strings.Contains(status, "(healthy)"):
		return "healthy"
	case strings.Contains(status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(status, "(health: starting)"):
		return "starting"
	}
	return ""
}

// normalizePorts dedupes IPv4/IPv6 twins and sorts by private port.
func normalizePorts(in []apiPort) []Port {
	type key struct {
		priv, pub int
		typ       string
	}
	seen := map[key]bool{}
	out := []Port{}
	for _, p := range in {
		k := key{p.PrivatePort, p.PublicPort, p.Type}
		if seen[k] {
			continue
		}
		seen[k] = true
		ip := p.IP
		if ip == "::" || ip == "0.0.0.0" {
			ip = ""
		}
		out = append(out, Port{IP: ip, Private: p.PrivatePort, Public: p.PublicPort, Type: p.Type})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Public > 0) != (out[j].Public > 0) {
			return out[i].Public > 0
		}
		if out[i].Private != out[j].Private {
			return out[i].Private < out[j].Private
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func isCasaOS(labels map[string]string) bool {
	if wd := labels[labelWorkingDir]; wd == casaosDir || strings.HasPrefix(wd, casaosDir+"/") {
		return true
	}
	for k := range labels {
		if strings.HasPrefix(k, "io.casaos.") {
			return true
		}
	}
	return false
}

func convert(a apiContainer) Container {
	name := ""
	if len(a.Names) > 0 {
		name = strings.TrimPrefix(a.Names[0], "/")
	}
	c := Container{
		ID: shortID(a.ID), FullID: a.ID, Name: name, Image: a.Image, ImageID: a.ImageID, Command: a.Command,
		State: a.State, Status: a.Status, Health: parseHealth(a.Status), Created: a.Created * 1000,
		Ports: normalizePorts(a.Ports), NetworkMode: a.HostConfig.NetworkMode, CasaOS: isCasaOS(a.Labels),
	}
	if p := a.Labels[labelProject]; p != "" {
		c.Compose = &Compose{Project: p, Service: a.Labels[labelService], WorkingDir: a.Labels[labelWorkingDir], ConfigFiles: a.Labels[labelConfig]}
	}
	return c
}

// findContainer matches ref against full ids, id prefixes and names.
func findContainer(list []Container, ref string) int {
	ref = strings.TrimPrefix(ref, "/")
	for i, c := range list {
		if c.FullID == ref || c.Name == ref {
			return i
		}
	}
	if len(ref) >= 4 {
		for i, c := range list {
			if strings.HasPrefix(c.FullID, ref) {
				return i
			}
		}
	}
	return -1
}

// convertList converts the list API answer and resolves network-container
// relationships.
func convertList(raw []apiContainer) []Container {
	out := make([]Container, len(raw))
	for i, a := range raw {
		out[i] = convert(a)
	}
	for i := range out {
		mode := out[i].NetworkMode
		if !strings.HasPrefix(mode, "container:") {
			continue
		}
		j := findContainer(out, strings.TrimPrefix(mode, "container:"))
		if j < 0 {
			continue
		}
		out[i].NetworkContainer = out[j].Name
		out[i].NetworkContainerID = out[j].ID
		out[i].NetworkPorts = out[j].Ports
		out[j].NetworkUsers = append(out[j].NetworkUsers, out[i].Name)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// --- inspect ---

type healthLog struct {
	Start    string `json:"Start"`
	End      string `json:"End"`
	ExitCode int    `json:"ExitCode"`
	Output   string `json:"Output"`
}

type inspectState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Paused     bool   `json:"Paused"`
	Restarting bool   `json:"Restarting"`
	OOMKilled  bool   `json:"OOMKilled"`
	Dead       bool   `json:"Dead"`
	Pid        int    `json:"Pid"`
	ExitCode   int    `json:"ExitCode"`
	Error      string `json:"Error"`
	StartedAt  string `json:"StartedAt"`
	FinishedAt string `json:"FinishedAt"`
	Health     *struct {
		Status        string      `json:"Status"`
		FailingStreak int         `json:"FailingStreak"`
		Log           []healthLog `json:"Log"`
	} `json:"Health"`
}

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type inspectMount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Mode        string `json:"Mode"`
	RW          bool   `json:"RW"`
	Propagation string `json:"Propagation"`
}

type inspectNetwork struct {
	NetworkID         string   `json:"NetworkID"`
	IPAddress         string   `json:"IPAddress"`
	IPPrefixLen       int      `json:"IPPrefixLen"`
	GlobalIPv6Address string   `json:"GlobalIPv6Address"`
	Gateway           string   `json:"Gateway"`
	MacAddress        string   `json:"MacAddress"`
	Aliases           []string `json:"Aliases"`
	DNSNames          []string `json:"DNSNames"`
}

type healthcheck struct {
	Test        []string `json:"Test"`
	Interval    int64    `json:"Interval"`
	Timeout     int64    `json:"Timeout"`
	StartPeriod int64    `json:"StartPeriod"`
	Retries     int      `json:"Retries"`
}

type inspect struct {
	ID           string       `json:"Id"`
	Created      string       `json:"Created"`
	Path         string       `json:"Path"`
	Args         []string     `json:"Args"`
	Name         string       `json:"Name"`
	Image        string       `json:"Image"` // image id
	RestartCount int          `json:"RestartCount"`
	Platform     string       `json:"Platform"`
	Driver       string       `json:"Driver"`
	State        inspectState `json:"State"`
	Config       struct {
		Hostname     string              `json:"Hostname"`
		User         string              `json:"User"`
		WorkingDir   string              `json:"WorkingDir"`
		Image        string              `json:"Image"`
		Env          []string            `json:"Env"`
		Cmd          []string            `json:"Cmd"`
		Entrypoint   []string            `json:"Entrypoint"`
		Labels       map[string]string   `json:"Labels"`
		Tty          bool                `json:"Tty"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
		Healthcheck  *healthcheck        `json:"Healthcheck"`
		StopTimeout  *int                `json:"StopTimeout"`
	} `json:"Config"`
	HostConfig struct {
		NetworkMode   string `json:"NetworkMode"`
		RestartPolicy struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
		Privileged   bool                     `json:"Privileged"`
		CapAdd       []string                 `json:"CapAdd"`
		NanoCpus     int64                    `json:"NanoCpus"`
		CPUShares    int64                    `json:"CpuShares"`
		Memory       int64                    `json:"Memory"`
		MemorySwap   int64                    `json:"MemorySwap"`
		PidsLimit    *int64                   `json:"PidsLimit"`
		CpusetCpus   string                   `json:"CpusetCpus"`
		Devices      []json.RawMessage        `json:"Devices"`
		LogConfig    struct{ Type string }    `json:"LogConfig"`
		PortBindings map[string][]portBinding `json:"PortBindings"`
		Binds        []string                 `json:"Binds"`
		Mounts       []struct {
			Target string `json:"Target"`
		} `json:"Mounts"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Ports    map[string][]portBinding  `json:"Ports"`
		Networks map[string]inspectNetwork `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []inspectMount `json:"Mounts"`
}

// envNames strips values: env vars routinely hold secrets.
func envNames(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

var secretKeyRe = regexp.MustCompile(`(?i)(pass(word|wd|phrase)?|secret|token|api[-_.]?key|access[-_.]?key|private[-_.]?key|credential|auth|cookie|htpasswd|bearer|jwt|salt|\bdsn\b|\bpwd\b|\.key$|_key$|-key$)`)

// urlCredsRe matches credentials embedded in URLs ("scheme://user:pass@").
var urlCredsRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@]*:[^/\s@]+@`)

// redactURLCreds masks passwords in URLs, e.g. in a DSN label or a
// healthcheck command.
func redactURLCreds(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}
	return urlCredsRe.ReplaceAllString(s, "${1}[redacted]@")
}

func redactAll(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = redactURLCreds(s)
	}
	return out
}

// redactLabels masks values whose key looks like it holds a secret and
// credentials embedded in URLs.
func redactLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if secretKeyRe.MatchString(k) {
			v = "[redacted]"
		} else {
			v = redactURLCreds(v)
			if len(v) > 2048 {
				v = truncate(v, 2048)
			}
		}
		out[k] = v
	}
	return out
}

// portsFromBindings converts inspect port maps ("80/tcp" -> bindings).
func portsFromBindings(m map[string][]portBinding) []Port {
	var raw []apiPort
	for k, bs := range m {
		ps, typ, _ := strings.Cut(k, "/")
		priv, _ := strconv.Atoi(ps)
		if typ == "" {
			typ = "tcp"
		}
		if len(bs) == 0 {
			raw = append(raw, apiPort{PrivatePort: priv, Type: typ})
		}
		for _, b := range bs {
			pub, _ := strconv.Atoi(b.HostPort)
			raw = append(raw, apiPort{IP: b.HostIP, PrivatePort: priv, PublicPort: pub, Type: typ})
		}
	}
	return normalizePorts(raw)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
