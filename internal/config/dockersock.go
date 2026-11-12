// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const linuxDockerSocket = "/var/run/docker.sock"

// DefaultDockerSocket is the Docker socket used when the config names none.
// Linux keeps /var/run/docker.sock. On macOS Docker usually lives in a VM
// (Colima, Docker Desktop), so it follows the docker CLI's own choice.
func DefaultDockerSocket(getenv func(string) string, home string) string {
	return dockerSocketFor(runtime.GOOS, getenv, home, fileExists)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// dockerSocketFor picks the socket for goos. On darwin the first match wins:
// DOCKER_HOST (unix only), the active docker context (DOCKER_CONTEXT or
// currentContext in config.json), Colima's default profile, then
// /var/run/docker.sock.
func dockerSocketFor(goos string, getenv func(string) string, home string, exists func(string) bool) string {
	if goos != "darwin" {
		return linuxDockerSocket
	}
	if h, ok := strings.CutPrefix(getenv("DOCKER_HOST"), "unix://"); ok && filepath.IsAbs(h) {
		return h
	}
	cfgDir := getenv("DOCKER_CONFIG")
	if cfgDir == "" {
		cfgDir = filepath.Join(home, ".docker")
	}
	name := getenv("DOCKER_CONTEXT")
	if name == "" {
		name = currentContext(cfgDir)
	}
	if name != "" && name != "default" {
		if s := contextSocket(cfgDir, name); s != "" {
			return s
		}
	}
	if c := filepath.Join(home, ".colima", "default", "docker.sock"); exists(c) {
		return c
	}
	return linuxDockerSocket
}

func currentContext(configDir string) string {
	b, err := readSmall(filepath.Join(configDir, "config.json"))
	if err != nil {
		return ""
	}
	var c struct {
		CurrentContext string `json:"currentContext"`
	}
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	return c.CurrentContext
}

// contextSocket returns the unix socket path of a docker context, or "".
func contextSocket(configDir, name string) string {
	sum := sha256.Sum256([]byte(name))
	b, err := readSmall(filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Endpoints struct {
			Docker struct {
				Host string `json:"Host"`
			} `json:"docker"`
		} `json:"Endpoints"`
	}
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	s, ok := strings.CutPrefix(m.Endpoints.Docker.Host, "unix://")
	if !ok || !filepath.IsAbs(s) {
		return ""
	}
	return s
}

const maxDockerMeta = 64 << 10

// readSmall reads a docker config file, treating one over 64 KiB as missing.
func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxDockerMeta+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxDockerMeta {
		return nil, errors.New("too large")
	}
	return b, nil
}
