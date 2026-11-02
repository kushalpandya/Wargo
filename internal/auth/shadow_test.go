// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/host"
)

func hashWith(t *testing.T, prefix, password string) string {
	t.Helper()
	if !cryptAvailable {
		t.Skip("built without crypt")
	}
	setting, err := cryptGensalt(prefix)
	if err != nil {
		t.Skipf("libcrypt does not support %s: %v", prefix, err)
	}
	h, err := cryptFunc(password, setting)
	if err != nil || h == "" || h[0] == '*' {
		t.Skipf("crypt(%s) failed: %q %v", prefix, h, err)
	}
	return h
}

func TestVerifyShadowHashes(t *testing.T) {
	now := time.Now()
	for _, prefix := range []string{"$y$", "$6$", "$5$", "$2b$"} {
		t.Run(prefix, func(t *testing.T) {
			h := hashWith(t, prefix, "correct horse")
			sh := &host.Shadow{Name: "u", Hash: h, Expire: -1}
			if err := verifyShadow(sh, "correct horse", now, cryptFunc); err != nil {
				t.Fatalf("valid password rejected: %v", err)
			}
			if err := verifyShadow(sh, "wrong", now, cryptFunc); !errors.Is(err, errMismatch) {
				t.Fatalf("wrong password: %v", err)
			}
			if err := verifyShadow(sh, "", now, cryptFunc); err == nil {
				t.Fatal("empty password accepted")
			}
		})
	}
}

func TestVerifyShadowLockedAndExpired(t *testing.T) {
	h := hashWith(t, "$6$", "pw")
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	today := now.Unix() / 86400
	cases := []struct {
		name string
		sh   host.Shadow
		want error
	}{
		{"locked !", host.Shadow{Hash: "!" + h, Expire: -1}, errLocked},
		{"locked *", host.Shadow{Hash: "*", Expire: -1}, errLocked},
		{"locked !!", host.Shadow{Hash: "!!", Expire: -1}, errLocked},
		{"empty", host.Shadow{Hash: "", Expire: -1}, errLocked},
		{"expired", host.Shadow{Hash: h, Expire: today}, errExpired},
		{"expired long ago", host.Shadow{Hash: h, Expire: 1}, errExpired},
		{"expires tomorrow", host.Shadow{Hash: h, Expire: today + 1}, nil},
	}
	for _, c := range cases {
		err := verifyShadow(&c.sh, "pw", now, cryptFunc)
		if !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

func TestVerifyHostPasswordContainer(t *testing.T) {
	h := hashWith(t, "$y$", "s3cret")
	r := t.TempDir()
	os.MkdirAll(filepath.Join(r, "etc"), 0o755)
	os.WriteFile(filepath.Join(r, "etc/passwd"), []byte("root:x:0:0::/root:/bin/sh\nkushal:x:1000:1000::/home/kushal:/bin/zsh\n"), 0o644)
	os.WriteFile(filepath.Join(r, "etc/group"), []byte("wheel:x:998:kushal\nkushal:x:1000:\n"), 0o644)
	os.WriteFile(filepath.Join(r, "etc/shadow"), []byte("root:!:19000::::::\nkushal:"+h+":19000:0:99999:7:::\n"), 0o600)
	defer host.SetRoot(r)()
	if err := VerifyHostPassword("kushal", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyHostPassword("kushal", "nope"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if err := VerifyHostPassword("root", "anything"); err == nil {
		t.Fatal("locked root accepted")
	}
	if err := VerifyHostPassword("ghost", "x"); err == nil {
		t.Fatal("unknown user accepted")
	}
	s := &Service{}
	if err := s.userIsAdmin("kushal"); err != nil {
		t.Fatalf("kushal should be admin via host db: %v", err)
	}
	if err := s.userIsAdmin("root"); err == nil {
		t.Fatal("root must not be an allowed admin in container mode")
	}
}
