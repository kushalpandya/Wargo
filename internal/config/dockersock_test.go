// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDockerSocketDarwinOrder(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	has := func(p ...string) func(string) bool { return func(s string) bool { return slices.Contains(p, s) } }
	h := "/Users/k"
	if s := dockerSocketFor("darwin", env(map[string]string{"DOCKER_HOST": "unix:///tmp/d.sock"}), h, has()); s != "/tmp/d.sock" {
		t.Fatal(s)
	}
	if s := dockerSocketFor("darwin", env(map[string]string{"DOCKER_HOST": "tcp://x:2375"}), h, has(h+"/.colima/default/docker.sock")); s != h+"/.colima/default/docker.sock" {
		t.Fatal(s)
	}
	if s := dockerSocketFor("darwin", env(nil), h, has()); s != "/var/run/docker.sock" {
		t.Fatal(s)
	}
	if s := dockerSocketFor("linux", env(map[string]string{"DOCKER_HOST": "unix:///tmp/d.sock"}), h, has()); s != "/var/run/docker.sock" {
		t.Fatal(s)
	}
}

func writeContext(t *testing.T, dir, name, host string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	p := filepath.Join(dir, "contexts/meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"Name":"` + name + `","Endpoints":{"docker":{"Host":"` + host + `"}}}`
	if err := os.WriteFile(filepath.Join(p, "meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestContextSocket(t *testing.T) {
	d := t.TempDir()
	writeContext(t, d, "colima-wargo", "unix:///Users/k/.colima/wargo/docker.sock")
	writeContext(t, d, "remote", "ssh://host")
	if s := contextSocket(d, "colima-wargo"); s != "/Users/k/.colima/wargo/docker.sock" {
		t.Fatal(s)
	}
	if s := contextSocket(d, "remote"); s != "" {
		t.Fatal(s)
	}
}

func TestDockerSocketDarwinContext(t *testing.T) {
	d := t.TempDir()
	writeContext(t, d, "ctx", "unix:///c/docker.sock")
	if err := os.WriteFile(filepath.Join(d, "config.json"), []byte(`{"currentContext":"ctx"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"DOCKER_CONFIG": d}
	get := func(k string) string { return env[k] }
	if s := dockerSocketFor("darwin", get, "/h", func(string) bool { return false }); s != "/c/docker.sock" {
		t.Fatal(s)
	}
	env["DOCKER_CONTEXT"] = "default"
	if s := dockerSocketFor("darwin", get, "/h", func(string) bool { return false }); s != "/var/run/docker.sock" {
		t.Fatal(s)
	}
}

func TestDockerMetaBadInputs(t *testing.T) {
	big := []byte(`{"currentContext":"x","pad":"` + strings.Repeat("a", 70<<10) + `"}`)
	d := t.TempDir()
	// no config.json, no meta.json
	if currentContext(d) != "" || contextSocket(d, "nope") != "" {
		t.Fatal("missing files must give empty")
	}
	// oversized config.json
	if err := os.WriteFile(filepath.Join(d, "config.json"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if s := currentContext(d); s != "" {
		t.Fatal(s)
	}
	// malformed and oversized meta.json
	sum := sha256.Sum256([]byte("bad"))
	p := filepath.Join(d, "contexts/meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "meta.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := contextSocket(d, "bad"); s != "" {
		t.Fatal(s)
	}
	sum = sha256.Sum256([]byte("huge"))
	p = filepath.Join(d, "contexts/meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	huge := []byte(`{"Endpoints":{"docker":{"Host":"unix:///x.sock"}},"pad":"` + strings.Repeat("a", 70<<10) + `"}`)
	if err := os.WriteFile(filepath.Join(p, "meta.json"), huge, 0o600); err != nil {
		t.Fatal(err)
	}
	if s := contextSocket(d, "huge"); s != "" {
		t.Fatal(s)
	}
}

func TestDockerSocketRelativeIgnored(t *testing.T) {
	d := t.TempDir()
	writeContext(t, d, "rel", "unix://relative.sock")
	if s := contextSocket(d, "rel"); s != "" {
		t.Fatal(s)
	}
	env := map[string]string{"DOCKER_HOST": "unix://rel.sock", "DOCKER_CONFIG": d}
	get := func(k string) string { return env[k] }
	if s := dockerSocketFor("darwin", get, "/h", func(string) bool { return false }); s != "/var/run/docker.sock" {
		t.Fatal(s)
	}
}
