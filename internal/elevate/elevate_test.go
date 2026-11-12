// SPDX-License-Identifier: AGPL-3.0-only

package elevate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSudo puts a "sudo" script first on PATH that records its stdin and argv.
func fakeSudo(t *testing.T) string {
	t.Helper()
	bin, out := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\ncat > \"$OUT/stdin\"\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > \"$OUT/argv\"\nwhile [ $# -gt 0 ]; do if [ \"$1\" = --stdin-file ]; then cp \"$2\" \"$OUT/payload\"; fi; shift; done\n"
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OUT", out)
	return out
}

func TestRunSudoPayloadNotOnStdin(t *testing.T) {
	out := fakeSudo(t)
	tmp := filepath.Join(t.TempDir(), "tmp")
	s := (&Service{exe: "/usr/bin/wargo"}).WithTempDir(tmp)

	if _, err := s.runSudo(context.Background(), "pw", s.Helper("fs", "write", "/r", "f"), strings.NewReader("CONTENT")); err != nil {
		t.Fatal(err)
	}
	in, _ := os.ReadFile(filepath.Join(out, "stdin"))
	if string(in) != "pw\n" {
		t.Fatalf("sudo stdin = %q, want only the password", in)
	}
	argv, _ := os.ReadFile(filepath.Join(out, "argv"))
	lines := strings.Split(strings.TrimSpace(string(argv)), "\n")
	idx := -1
	for i, l := range lines {
		if l == "--stdin-file" {
			idx = i
		}
	}
	if idx < 1 || lines[idx-1] != "helper" || idx+2 >= len(lines) || lines[idx+2] != "fs" {
		t.Fatalf("argv = %q", lines)
	}
	if p, _ := os.ReadFile(filepath.Join(out, "payload")); string(p) != "CONTENT" {
		t.Errorf("payload file = %q", p)
	}
	path := lines[idx+1]
	if filepath.Dir(path) != tmp {
		t.Fatalf("spool file %q not in %q", path, tmp)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("spool file still exists: %v", err)
	}
	if fi, err := os.Stat(tmp); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("temp dir: %v %v", fi, err)
	}
}

func TestRunSudoNoPayloadNoStdinFile(t *testing.T) {
	out := fakeSudo(t)
	s := (&Service{exe: "/usr/bin/wargo"}).WithTempDir(filepath.Join(t.TempDir(), "tmp"))
	if _, err := s.runSudo(context.Background(), "pw", s.Helper("kill", "TERM", "1"), nil); err != nil {
		t.Fatal(err)
	}
	argv, _ := os.ReadFile(filepath.Join(out, "argv"))
	if strings.Contains(string(argv), "--stdin-file") {
		t.Fatalf("unexpected --stdin-file: %s", argv)
	}
}

func TestTempDirTightenedAndSymlinkRefused(t *testing.T) {
	base := t.TempDir()
	loose := filepath.Join(base, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	(&Service{}).WithTempDir(loose)
	if fi, err := os.Stat(loose); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("pre-existing dir not tightened: %v %v", fi, err)
	}

	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	s := (&Service{}).WithTempDir(link)
	if _, err := s.spool(strings.NewReader("x")); err == nil {
		t.Error("symlinked temp dir accepted")
	}
	if ents, _ := os.ReadDir(real); len(ents) != 0 {
		t.Errorf("wrote through symlink: %v", ents)
	}
}

func TestRunSudoBadArgvWritesNothing(t *testing.T) {
	fakeSudo(t)
	tmp := filepath.Join(t.TempDir(), "tmp")
	s := (&Service{}).WithTempDir(tmp)
	if _, err := s.runSudo(context.Background(), "pw", []string{"/bin/sh"}, strings.NewReader("x")); err == nil {
		t.Fatal("non-helper argv with payload accepted")
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Errorf("spooled before argv check: %v", ents)
	}
}
