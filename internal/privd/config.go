// SPDX-License-Identifier: AGPL-3.0-only

// Package privd implements wargo-privd, the optional root helper that
// executes a closed set of privileged commands for Wargo — but only after
// verifying a WebAuthn assertion from one of the admin's trusted passkeys.
// A compromised wargo process therefore cannot escalate to root without
// the user's authenticator.
package privd

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/kushalpandya/wargo/internal/helper"
	"github.com/kushalpandya/wargo/internal/privproto"
)

// DefaultConfigPath is where wargo-privd reads its configuration.
const DefaultConfigPath = "/etc/wargo/privd.toml"

// Config is /etc/wargo/privd.toml.
type Config struct {
	// AllowedUID is the only peer uid accepted on the socket (the user
	// Wargo runs as). Required.
	AllowedUID int `toml:"allowed_uid"`
	// Origins are the exact browser origins (scheme://host[:port]) that
	// may request approvals; the WebAuthn RP ID is the origin's hostname.
	Origins []string `toml:"origins"`
	// WargoBinary is the absolute path of the trusted wargo binary used
	// for `wargo helper` operations. It must be root-owned and not
	// writable by anyone else.
	WargoBinary string `toml:"wargo_binary"`
	// Credentials is the trusted passkey store (written by `wargo helper privd-trust`).
	Credentials string `toml:"credentials"`
	// Socket path when not socket-activated.
	Socket string `toml:"socket"`
	// SystemdRun and Systemctl are absolute paths of those tools.
	SystemdRun string `toml:"systemd_run"`
	Systemctl  string `toml:"systemctl"`
}

func defaults() Config {
	return Config{
		AllowedUID:  -1,
		WargoBinary: "/usr/local/bin/wargo",
		Credentials: helper.PrivdCredentialsPath,
		Socket:      privproto.DefaultSocket,
		SystemdRun:  "/usr/bin/systemd-run",
		Systemctl:   "/usr/bin/systemctl",
	}
}

// LoadConfig reads and validates the config file.
func LoadConfig(path string) (Config, error) {
	c := defaults()
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	// The config names the binaries privd executes as root, so it must
	// not be editable by the Wargo user.
	if err := checkTrustedPath(path, f); err != nil {
		return c, fmt.Errorf("config %s is not trusted: %w", path, err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, c.Validate()
}

// Validate checks the configuration for obvious mistakes.
func (c *Config) Validate() error {
	if c.AllowedUID < 0 {
		return errors.New("allowed_uid must be set to the uid wargo runs as")
	}
	if c.AllowedUID == 0 {
		return errors.New("allowed_uid must not be root; run wargo as the admin user")
	}
	for _, p := range []struct{ name, v string }{
		{"wargo_binary", c.WargoBinary}, {"credentials", c.Credentials}, {"socket", c.Socket},
		{"systemd_run", c.SystemdRun}, {"systemctl", c.Systemctl},
	} {
		if !filepath.IsAbs(p.v) || filepath.Clean(p.v) != p.v {
			return fmt.Errorf("%s must be a clean absolute path", p.name)
		}
	}
	if len(c.Origins) == 0 {
		return errors.New("origins must list at least one origin, e.g. \"https://wargo.example.com\"")
	}
	for i, o := range c.Origins {
		o = strings.TrimRight(o, "/")
		u, err := url.Parse(o)
		if err != nil || u.Host == "" || u.Path != "" || (u.Scheme != "https" && !(u.Scheme == "http" && u.Hostname() == "localhost")) {
			return fmt.Errorf("origin %q must be https://host[:port] (or http://localhost:port)", c.Origins[i])
		}
		c.Origins[i] = o
	}
	return nil
}

// matchOrigin returns the configured (canonical) origin and its RP ID if
// origin is allowed.
func (c *Config) matchOrigin(origin string) (canonical, rpID string, ok bool) {
	origin = strings.TrimRight(origin, "/")
	if origin == "" {
		return "", "", false
	}
	for _, o := range c.Origins {
		if strings.EqualFold(o, origin) {
			u, err := url.Parse(o)
			if err != nil {
				return "", "", false
			}
			return o, u.Hostname(), true
		}
	}
	return "", "", false
}

// primaryGID returns the primary group of uid, or -1.
func primaryGID(uid int) int {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return -1
	}
	g, err := strconv.Atoi(u.Gid)
	if err != nil {
		return -1
	}
	return g
}
