// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSealRoundTripAndKeyPersistence(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal("hunter2")
	if err != nil || sealed == "hunter2" {
		t.Fatalf("seal: %q %v", sealed, err)
	}
	s.Close()
	if st, _ := os.Stat(filepath.Join(dir, "secret.key")); st.Mode().Perm() != 0o600 {
		t.Fatalf("secret.key mode %v", st.Mode().Perm())
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	plain, err := s2.Unseal(sealed)
	if err != nil || plain != "hunter2" {
		t.Fatalf("unseal after reopen: %q %v", plain, err)
	}
	if _, err := s2.Unseal(sealed[:len(sealed)-4] + "AAAA"); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestSettings(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	var v map[string]int
	if ok, _ := s.Get(ctx, "k", &v); ok {
		t.Fatal("unexpected value")
	}
	s.Set(ctx, "k", map[string]int{"a": 1})
	s.Set(ctx, "k", map[string]int{"a": 2})
	if ok, _ := s.Get(ctx, "k", &v); !ok || v["a"] != 2 {
		t.Fatalf("got %v", v)
	}
}

func TestCorruptKeyIsNotReplaced(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(key, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("corrupt key silently accepted")
	}
	if b, _ := os.ReadFile(key); string(b) != "short" {
		t.Fatal("corrupt key was overwritten (sealed secrets would be lost)")
	}
}

func TestKeyAndDirPermissionsTightened(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	os.MkdirAll(dir, 0o755)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	os.Chmod(filepath.Join(dir, "secret.key"), 0o644)
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("data dir mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Join(dir, "secret.key")); st.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", st.Mode().Perm())
	}
}
