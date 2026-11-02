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
