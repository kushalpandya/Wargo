// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConfigFileHostile(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.xml")
	os.WriteFile(secret, []byte("<Config><ApiKey>leakedkey0123456789</ApiKey></Config>"), 0o600)

	// A symlinked config.xml (the container controls the folder) is refused.
	os.Symlink(secret, filepath.Join(dir, "config.xml"))
	if k, err := readConfigSecret(dir, "config.xml", parseAPIKey); err == nil {
		t.Fatalf("followed symlink: %q", k)
	}
	if readable(dir, "config.xml") {
		t.Fatal("symlink reported readable")
	}

	// A FIFO neither blocks nor is accepted.
	fifoDir := t.TempDir()
	fifo := filepath.Join(fifoDir, "config.xml")
	if err := syscall.Mkfifo(fifo, 0o644); err == nil {
		done := make(chan error, 1)
		go func() { _, err := readConfigSecret(fifoDir, "config.xml", parseAPIKey); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("FIFO accepted")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("blocked on FIFO")
		}
	}

	// A regular file works.
	okDir := t.TempDir()
	os.WriteFile(filepath.Join(okDir, "config.xml"), []byte("<Config><ApiKey>goodkey0123456789</ApiKey></Config>"), 0o600)
	if k, err := readConfigSecret(okDir, "config.xml", parseAPIKey); err != nil || k != "goodkey0123456789" {
		t.Fatal(k, err)
	}
	if _, err := readConfigSecret(okDir, "../x/config.xml", parseAPIKey); err == nil {
		t.Fatal(".. accepted")
	}
}

// A folder below the bind source swapped for a symlink is not followed,
// however deep (natively nothing else resolves it inside a root).
func TestConfigFileSymlinkedFolder(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	inner := filepath.Join(elsewhere, "Plex Media Server")
	os.MkdirAll(inner, 0o700)
	os.WriteFile(filepath.Join(inner, "Preferences.xml"), []byte(`<Preferences PlexOnlineToken="`+plexTok+`"/>`), 0o600)
	lib := filepath.Join(root, "Library", "Application Support")
	os.MkdirAll(filepath.Dir(lib), 0o700)
	if err := os.Symlink(elsewhere, lib); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("Library", "Application Support", "Plex Media Server", "Preferences.xml")
	if tok, err := readConfigSecret(root, rel, parsePlexToken); err == nil || !strings.Contains(err.Error(), "Application Support is a symbolic link") {
		t.Fatalf("followed symlinked folder: %q %v", tok, err)
	}
	if readable(root, rel) {
		t.Fatal("symlinked folder reported readable")
	}
	// The same layout with real folders works.
	os.Remove(lib)
	os.Rename(elsewhere, lib)
	if tok, err := readConfigSecret(root, rel, parsePlexToken); err != nil || tok != plexTok {
		t.Fatal(tok, err)
	}
}
