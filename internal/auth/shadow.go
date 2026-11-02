// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/kushalpandya/wargo/internal/host"
)

// Container mode cannot use PAM: the container's PAM stack only knows the
// container's accounts. Instead the host's /etc/shadow (readable because
// Wargo is root in the container) is checked with libxcrypt's crypt_r,
// which supports every hash the host may use (yescrypt, sha512crypt,
// bcrypt, ...).

var (
	errLocked    = errors.New("account is locked or has no password")
	errExpired   = errors.New("account has expired")
	errMismatch  = errors.New("password mismatch")
	errNoCrypt   = errors.New("wargo was built without crypt support")
	errBadHash   = errors.New("unsupported password hash")
	cryptForTest func(password, setting string) (string, error)
)

// verifyShadow checks password against a shadow entry. Locked accounts
// ("!" / "*" prefixes, empty hashes) and expired accounts (shadow field 8)
// are rejected; password ageing is not a reason to block.
func verifyShadow(sh *host.Shadow, password string, now time.Time, crypt func(string, string) (string, error)) error {
	h := sh.Hash
	if h == "" || strings.HasPrefix(h, "!") || strings.HasPrefix(h, "*") || !strings.HasPrefix(h, "$") && len(h) < 13 {
		return errLocked
	}
	if sh.Expire >= 0 && now.Unix()/86400 >= sh.Expire {
		return errExpired
	}
	if password == "" {
		return errMismatch
	}
	got, err := crypt(password, h)
	if err != nil {
		return err
	}
	if got == "" || got[0] == '*' {
		return errBadHash
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(h)) != 1 {
		return errMismatch
	}
	return nil
}

// VerifyHostPassword checks the host account's password against the host's
// shadow file (container mode only; no throttling — callers throttle).
func VerifyHostPassword(user, password string) error {
	sh, err := host.LookupShadow(user)
	if err != nil {
		return err
	}
	c := cryptFunc
	if cryptForTest != nil {
		c = cryptForTest
	}
	return verifyShadow(sh, password, time.Now(), c)
}
