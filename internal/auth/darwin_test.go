// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin && cgo

package auth

import (
	"os/exec"
	"os/user"
	"slices"
	"strings"
	"testing"

	"github.com/kushalpandya/wargo/internal/config"
)

func TestAdminGroupPrefersAdminOnDarwin(t *testing.T) {
	s := &Service{cfg: config.Default()}
	if g := s.AdminGroup(); g != "admin" {
		t.Fatalf("admin group %q", g)
	}
	u, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	out, _ := exec.Command("id", "-Gn").Output()
	if !slices.Contains(strings.Fields(string(out)), "admin") {
		t.Skip("current user is not an admin")
	}
	if err := s.userIsAdmin(u.Username); err != nil {
		t.Fatal(err)
	}
}

func TestPAMAvailableDarwin(t *testing.T) {
	if !pamAvailable {
		t.Fatal("PAM must be built in on darwin with cgo")
	}
	err := pamAuthenticate("chkpasswd", "wargo-no-such-user", "x")
	if err == nil || strings.Contains(err.Error(), "without PAM") {
		t.Fatalf("err = %v", err)
	}
}
