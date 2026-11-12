// SPDX-License-Identifier: AGPL-3.0-only

// Package config loads Wargo's TOML configuration.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/kushalpandya/wargo/internal/host"
)

// Duration wraps time.Duration so it can be written as "30m" in TOML.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

type Config struct {
	Listen  string `toml:"listen"`
	DataDir string `toml:"data_dir"`

	Auth         Auth         `toml:"auth"`
	Dev          Dev          `toml:"dev"`
	Files        Files        `toml:"files"`
	Docker       Docker       `toml:"docker"`
	Integrations Integrations `toml:"integrations"`
	Metrics      Metrics      `toml:"metrics"`
}

type Auth struct {
	// User is the single host account allowed to log in. Defaults to the
	// account Wargo runs as.
	User string `toml:"user"`
	// AdminGroup the user must belong to ("wheel", "sudo"). Auto-detected if empty.
	AdminGroup string `toml:"admin_group"`
	// PAMService is the /etc/pam.d service used for password checks.
	PAMService string `toml:"pam_service"`
	// Origins allowed for WebSockets and WebAuthn, e.g. "https://wargo.example.com".
	Origins []string `toml:"origins"`
	// TrustedProxies are CIDRs whose X-Forwarded-For / CF-Connecting-IP are honoured.
	TrustedProxies []string `toml:"trusted_proxies"`
	SessionIdle    Duration `toml:"session_idle"`
	SessionMax     Duration `toml:"session_max"`
	// SessionRemember is how long a "Keep me signed in" session lasts
	// since it was last used.
	SessionRemember Duration `toml:"session_remember"`
	// SessionRememberMax ends a remembered session this long after sign-in,
	// however often it is used.
	SessionRememberMax Duration `toml:"session_remember_max"`
	RequireTOTP        bool     `toml:"require_totp"`
}

type Dev struct {
	// AuthBypass skips login entirely. Only honoured when listening on loopback.
	AuthBypass bool `toml:"auth_bypass"`
	// Origins added to the allow-list during development (Vite dev server).
	Origins []string `toml:"origins"`
}

type Root struct {
	Name string `toml:"name"`
	Path string `toml:"path"`
}

type Files struct {
	Roots       []Root `toml:"roots"`
	TrashDir    string `toml:"trash_dir"`
	MaxEditSize int64  `toml:"max_edit_size"`
	// ShowMounts lists mounted filesystems ("Drives") next to the roots.
	ShowMounts bool `toml:"show_mounts"`
}

type Docker struct {
	Socket string `toml:"socket"`
	// UpdateCheckInterval is how often container images are checked for
	// newer versions in the background (read-only registry lookups).
	// "0" disables background checks.
	UpdateCheckInterval Duration `toml:"update_check_interval"`
}

type Integrations struct {
	Prowlarr     Prowlarr     `toml:"prowlarr"`
	Transmission Transmission `toml:"transmission"`
	Plex         Plex         `toml:"plex"`
}

type Prowlarr struct {
	URL    string `toml:"url"`
	APIKey string `toml:"api_key"`
}

type Transmission struct {
	URL      string `toml:"url"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

type Plex struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

type Metrics struct {
	Interval Duration `toml:"interval"`
	History  Duration `toml:"history"`
}

// DefaultListen is the listen address when neither the config file nor
// WARGO_LISTEN sets one. Native installs stay on loopback (publish through
// a reverse proxy); the container image listens on all interfaces so it is
// reachable on the LAN without extra setup, like other homelab dashboards.
func DefaultListen() string {
	if host.Container() {
		return "0.0.0.0:2207"
	}
	return "127.0.0.1:2207"
}

func userHome() string {
	h, _ := os.UserHomeDir()
	return h
}

// Default returns a configuration suitable for a fresh install.
func Default() Config {
	c := Config{
		Listen: DefaultListen(),
		Auth: Auth{
			PAMService:         "wargo",
			SessionIdle:        Duration{30 * time.Minute},
			SessionMax:         Duration{12 * time.Hour},
			SessionRemember:    Duration{30 * 24 * time.Hour},
			SessionRememberMax: Duration{90 * 24 * time.Hour},
			RequireTOTP:        true,
		},
		Docker:  Docker{Socket: DefaultDockerSocket(os.Getenv, userHome()), UpdateCheckInterval: Duration{6 * time.Hour}},
		Metrics: Metrics{Interval: Duration{time.Second}, History: Duration{10 * time.Minute}},
		Files:   Files{MaxEditSize: 4 << 20, ShowMounts: true},
	}
	if host.Container() {
		// The process runs as root in the container; the admin account is
		// a host user (WARGO_USER, or the first regular wheel/sudo member).
		if name := strings.TrimSpace(os.Getenv("WARGO_USER")); name != "" {
			if u, err := host.LookupUser(name); err == nil {
				c.Auth.User = u.Name
				c.Files.Roots = []Root{{Name: "Home", Path: u.Home}}
			} else {
				c.Auth.User = name
			}
		} else if u, g, err := host.DefaultAdmin(); err == nil {
			slog.Info("container mode: using the first admin-group member as the Wargo user", "user", u.Name, "group", g)
			c.Auth.User = u.Name
			c.Files.Roots = []Root{{Name: "Home", Path: u.Home}}
		}
	} else if u, err := user.Current(); err == nil {
		c.Auth.User = u.Username
		c.Files.Roots = []Root{{Name: "Home", Path: u.HomeDir}}
	}
	if st, err := host.Stat("/DATA"); err == nil && st.IsDir() {
		c.Files.Roots = append(c.Files.Roots, Root{Name: "DATA", Path: "/DATA"})
	}
	return c
}

// ContainerConfigPath and ContainerDataDir are the container-mode defaults.
const (
	ContainerDataDir    = "/data"
	ContainerConfigPath = "/data/config.toml"
)

// DefaultPath returns the config path: $WARGO_CONFIG, /etc/wargo/config.toml,
// or the XDG config dir for user installs.
func DefaultPath() string {
	if p := os.Getenv("WARGO_CONFIG"); p != "" {
		return p
	}
	if host.Container() {
		return ContainerConfigPath
	}
	if _, err := os.Stat("/etc/wargo/config.toml"); err == nil {
		return "/etc/wargo/config.toml"
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "wargo", "config.toml")
	}
	return "config.toml"
}

func defaultDataDir() string {
	if host.Container() {
		return ContainerDataDir
	}
	if st, err := os.Stat("/var/lib/wargo"); err == nil && st.IsDir() {
		if f, err := os.CreateTemp("/var/lib/wargo", ".w"); err == nil {
			f.Close()
			os.Remove(f.Name())
			return "/var/lib/wargo"
		}
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "wargo")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "state", "wargo")
	}
	return "data"
}

// Load reads path (missing file is not an error) on top of defaults.
func Load(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return c, err
	default:
		if err := toml.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if err := c.ApplyEnv(os.Getenv); err != nil {
		return c, err
	}
	if c.DataDir == "" {
		c.DataDir = defaultDataDir()
	}
	// In container mode the trash must live on the host (renames can't
	// cross into the data volume); the files service then defaults it to
	// the admin's ~/.local/share/wargo/trash.
	if c.Files.TrashDir == "" && !host.Container() {
		c.Files.TrashDir = filepath.Join(c.DataDir, "trash")
	}
	return c, c.Validate()
}

// ApplyEnv overrides settings from WARGO_* environment variables (used by
// the Docker image, but honoured natively too):
//
//	WARGO_LISTEN           listen address, e.g. 0.0.0.0:2207
//	WARGO_USER             auth.user
//	WARGO_ORIGINS          auth.origins, comma-separated
//	WARGO_TRUSTED_PROXIES  auth.trusted_proxies, comma-separated CIDRs
//	WARGO_DATA_DIR         data_dir
//	WARGO_FILES_ROOTS      files.roots as "Name=/path,Other=/path" (or bare paths)
func (c *Config) ApplyEnv(getenv func(string) string) error {
	if v := strings.TrimSpace(getenv("WARGO_LISTEN")); v != "" {
		c.Listen = v
	}
	if v := strings.TrimSpace(getenv("WARGO_USER")); v != "" {
		c.Auth.User = v
	}
	if v := getenv("WARGO_ORIGINS"); strings.TrimSpace(v) != "" {
		c.Auth.Origins = splitList(v)
	}
	if v := getenv("WARGO_TRUSTED_PROXIES"); strings.TrimSpace(v) != "" {
		c.Auth.TrustedProxies = splitList(v)
	}
	if v := strings.TrimSpace(getenv("WARGO_DATA_DIR")); v != "" {
		c.DataDir = v
	}
	if v := getenv("WARGO_FILES_ROOTS"); strings.TrimSpace(v) != "" {
		roots, err := parseRoots(v)
		if err != nil {
			return fmt.Errorf("WARGO_FILES_ROOTS: %w", err)
		}
		c.Files.Roots = roots
	}
	return nil
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseRoots parses "Name=/path,/other/path".
func parseRoots(v string) ([]Root, error) {
	var out []Root
	for _, item := range splitList(v) {
		name, p, ok := strings.Cut(item, "=")
		if !ok {
			name, p = "", item
		}
		name, p = strings.TrimSpace(name), strings.TrimSpace(p)
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("%q is not an absolute path", p)
		}
		p = filepath.Clean(p)
		if name == "" {
			name = filepath.Base(p)
			if name == "/" {
				name = "System"
			}
		}
		out = append(out, Root{Name: name, Path: p})
	}
	return out, nil
}

func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if c.Auth.User == "" {
		if host.Container() {
			return errors.New("auth.user is required: set WARGO_USER to your host account")
		}
		return errors.New("auth.user is required")
	}
	if host.Container() && c.Auth.User == "root" {
		return errors.New("auth.user must be a regular admin account, not root (set WARGO_USER)")
	}
	for _, p := range c.Auth.TrustedProxies {
		if _, _, err := net.ParseCIDR(p); err != nil {
			return fmt.Errorf("auth.trusted_proxies: %w", err)
		}
	}
	for _, list := range []struct {
		name string
		v    []string
	}{{"auth.origins", c.Auth.Origins}, {"dev.origins", c.Dev.Origins}} {
		for _, o := range list.v {
			if err := validOrigin(o); err != nil {
				return fmt.Errorf("%s: %w", list.name, err)
			}
		}
	}
	if c.Auth.SessionIdle.Duration <= 0 || c.Auth.SessionMax.Duration <= 0 || c.Auth.SessionRemember.Duration <= 0 {
		return errors.New("auth.session_idle, auth.session_max and auth.session_remember must be positive")
	}
	if c.Auth.SessionRememberMax.Duration < c.Auth.SessionRemember.Duration {
		return errors.New("auth.session_remember_max must be at least auth.session_remember")
	}
	return nil
}

// validOrigin accepts scheme://host[:port] with http or https (a trailing
// slash is tolerated). Origins are allow-list entries for CSRF, WebSocket,
// WebAuthn and Host checks, so anything vaguer is a mistake.
func validOrigin(o string) error {
	u, err := url.Parse(strings.TrimRight(o, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.Contains(u.Host, "*") {
		return fmt.Errorf("%q must look like https://wargo.example.com[:port]", o)
	}
	return nil
}

// ListenAddr returns the effective listen address without loading the full
// configuration (used by `wargo health`): WARGO_LISTEN, then the file's
// listen, then the default.
func ListenAddr(path string) string {
	if v := strings.TrimSpace(os.Getenv("WARGO_LISTEN")); v != "" {
		return v
	}
	var c struct {
		Listen string `toml:"listen"`
	}
	if b, err := os.ReadFile(path); err == nil && toml.Unmarshal(b, &c) == nil && c.Listen != "" {
		return c.Listen
	}
	return DefaultListen()
}

// LoopbackOnly reports whether Listen binds a loopback address only.
func (c Config) LoopbackOnly() bool {
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		// net.Listen binds whatever localhost resolves to; make sure that
		// really is loopback only (a broken /etc/hosts could say otherwise).
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return false
		}
		for _, ip := range ips {
			if !ip.IsLoopback() {
				return false
			}
		}
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
