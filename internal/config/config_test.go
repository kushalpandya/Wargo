// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kushalpandya/wargo/internal/host"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestApplyEnv(t *testing.T) {
	c := Default()
	c.Auth.User = "someone"
	err := c.ApplyEnv(envMap(map[string]string{
		"WARGO_LISTEN":          "0.0.0.0:2207",
		"WARGO_USER":            "kushal",
		"WARGO_ORIGINS":         " https://a.example.com, https://b.example.com ,",
		"WARGO_TRUSTED_PROXIES": "127.0.0.1/32,10.0.0.0/8",
		"WARGO_DATA_DIR":        "/data",
		"WARGO_FILES_ROOTS":     "Home=/home/kushal, Media=/mnt/Store/, /srv/files",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:2207" || c.Auth.User != "kushal" || c.DataDir != "/data" {
		t.Fatalf("unexpected %+v", c)
	}
	if !reflect.DeepEqual(c.Auth.Origins, []string{"https://a.example.com", "https://b.example.com"}) {
		t.Fatalf("origins %q", c.Auth.Origins)
	}
	if !reflect.DeepEqual(c.Auth.TrustedProxies, []string{"127.0.0.1/32", "10.0.0.0/8"}) {
		t.Fatalf("proxies %q", c.Auth.TrustedProxies)
	}
	want := []Root{{"Home", "/home/kushal"}, {"Media", "/mnt/Store"}, {"files", "/srv/files"}}
	if !reflect.DeepEqual(c.Files.Roots, want) {
		t.Fatalf("roots %+v", c.Files.Roots)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyEnvEmptyKeepsValues(t *testing.T) {
	c := Default()
	before := c
	if err := c.ApplyEnv(envMap(nil)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, c) {
		t.Fatal("empty environment changed the config")
	}
}

func TestApplyEnvBadRoot(t *testing.T) {
	c := Default()
	if err := c.ApplyEnv(envMap(map[string]string{"WARGO_FILES_ROOTS": "Home=relative/path"})); err == nil {
		t.Fatal("expected error for relative root")
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	os.WriteFile(p, []byte("listen = \"127.0.0.1:1111\"\ndata_dir = \""+dir+"\"\n[auth]\nuser = \"fileuser\"\n"), 0o600)
	t.Setenv("WARGO_LISTEN", "127.0.0.1:2222")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:2222" || c.Auth.User != "fileuser" {
		t.Fatalf("got listen=%s user=%s", c.Listen, c.Auth.User)
	}
	if c.Files.TrashDir != filepath.Join(dir, "trash") {
		t.Fatalf("native trash default changed: %s", c.Files.TrashDir)
	}
}

func fakeHost(t *testing.T) string {
	t.Helper()
	r := t.TempDir()
	os.MkdirAll(filepath.Join(r, "etc"), 0o755)
	os.MkdirAll(filepath.Join(r, "home/alice"), 0o755)
	os.WriteFile(filepath.Join(r, "etc/passwd"), []byte("root:x:0:0::/root:/bin/bash\nalice:x:1000:1000:Alice:/home/alice:/bin/zsh\n"), 0o644)
	os.WriteFile(filepath.Join(r, "etc/group"), []byte("root:x:0:\nwheel:x:998:alice\nalice:x:1000:\n"), 0o644)
	t.Cleanup(host.SetRoot(r))
	return r
}

func TestContainerDefaults(t *testing.T) {
	fakeHost(t)
	t.Setenv("WARGO_USER", "")
	t.Setenv("WARGO_CONFIG", "")
	if DefaultPath() != ContainerConfigPath {
		t.Fatalf("config path %s", DefaultPath())
	}
	c := Default()
	if c.Auth.User != "alice" {
		t.Fatalf("admin %q", c.Auth.User)
	}
	if len(c.Files.Roots) != 1 || c.Files.Roots[0].Path != "/home/alice" {
		t.Fatalf("roots %+v", c.Files.Roots)
	}
	if c.Listen != "127.0.0.1:2207" {
		t.Fatalf("container default listen must stay loopback, got %s", c.Listen)
	}
	c.DataDir = ""
	if defaultDataDir() != ContainerDataDir {
		t.Fatal("data dir")
	}
	c.Auth.User = "root"
	if err := c.Validate(); err == nil {
		t.Fatal("root must be rejected in container mode")
	}
}

func TestContainerLoadNoTrashDefault(t *testing.T) {
	fakeHost(t)
	dir := t.TempDir()
	t.Setenv("WARGO_DATA_DIR", dir)
	c, err := Load(filepath.Join(dir, "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Files.TrashDir != "" || c.DataDir != dir {
		t.Fatalf("trash=%q data=%q", c.Files.TrashDir, c.DataDir)
	}
}

func TestValidateOrigins(t *testing.T) {
	for o, ok := range map[string]bool{
		"https://wargo.example.com":      true,
		"https://wargo.example.com/":     true,
		"http://127.0.0.1:5173":          true,
		"wargo.example.com":              false,
		"https://wargo.example.com/path": false,
		"ftp://wargo.example.com":        false,
		"https://*.example.com":          false,
		"https://user@wargo.example.com": false,
	} {
		c := Default()
		c.Auth.User = "x"
		c.Auth.Origins = []string{o}
		if err := c.Validate(); (err == nil) != ok {
			t.Errorf("%q: err=%v", o, err)
		}
	}
}
