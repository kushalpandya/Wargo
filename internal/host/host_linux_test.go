// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAsUserContainerUnprivileged(t *testing.T) {
	fakeRoot(t)
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	// No admin configured: refuse.
	if err := AsUser(func() error { t.Fatal("fn must not run"); return nil }); err == nil {
		t.Fatal("expected error without an admin")
	}
	if _, err := SetAdmin("kushal"); err != nil {
		t.Fatal(err)
	}
	ran := false
	err := AsUser(func() error { ran = true; return nil })
	if !errors.Is(err, ErrNotRoot) || ran {
		t.Fatalf("non-root container mode must refuse: err=%v ran=%v", err, ran)
	}
	// AsRoot still runs fn (it never switches anything itself).
	if err := AsRoot(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	// The thread identity is untouched.
	if getfsuid() != os.Getuid() {
		t.Fatal("fsuid changed")
	}
}

func TestAsUserRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	r := fakeRoot(t)
	if _, err := SetAdmin("kushal"); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(r, "secret")
	os.WriteFile(secret, []byte("x"), 0o600)
	err := AsUser(func() error {
		if getfsuid() != 1000 {
			t.Error("fsuid not switched")
		}
		_, err := os.ReadFile(secret)
		return err
	})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected permission error as user, got %v", err)
	}
	if getfsuid() != 0 {
		t.Fatal("identity not restored")
	}
}
