// SPDX-License-Identifier: AGPL-3.0-only

// Package config loads Wargo's TOML configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
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
	RequireTOTP    bool     `toml:"require_totp"`
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

type Metrics struct {
	Interval Duration `toml:"interval"`
	History  Duration `toml:"history"`
}

// Default returns a configuration suitable for a fresh install.
func Default() Config {
	c := Config{
		Listen: "127.0.0.1:2207",
		Auth: Auth{
			PAMService:  "wargo",
			SessionIdle: Duration{30 * time.Minute},
			SessionMax:  Duration{12 * time.Hour},
			RequireTOTP: true,
		},
		Docker:  Docker{Socket: "/var/run/docker.sock", UpdateCheckInterval: Duration{6 * time.Hour}},
		Metrics: Metrics{Interval: Duration{time.Second}, History: Duration{10 * time.Minute}},
		Files:   Files{MaxEditSize: 4 << 20, ShowMounts: true},
	}
	if u, err := user.Current(); err == nil {
		c.Auth.User = u.Username
		c.Files.Roots = []Root{{Name: "Home", Path: u.HomeDir}}
	}
	if st, err := os.Stat("/DATA"); err == nil && st.IsDir() {
		c.Files.Roots = append(c.Files.Roots, Root{Name: "DATA", Path: "/DATA"})
	}
	return c
}

// DefaultPath returns the config path: $WARGO_CONFIG, /etc/wargo/config.toml,
// or the XDG config dir for user installs.
func DefaultPath() string {
	if p := os.Getenv("WARGO_CONFIG"); p != "" {
		return p
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
	if c.DataDir == "" {
		c.DataDir = defaultDataDir()
	}
	if c.Files.TrashDir == "" {
		c.Files.TrashDir = filepath.Join(c.DataDir, "trash")
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if c.Auth.User == "" {
		return errors.New("auth.user is required")
	}
	for _, p := range c.Auth.TrustedProxies {
		if _, _, err := net.ParseCIDR(p); err != nil {
			return fmt.Errorf("auth.trusted_proxies: %w", err)
		}
	}
	return nil
}

// LoopbackOnly reports whether Listen binds a loopback address only.
func (c Config) LoopbackOnly() bool {
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
