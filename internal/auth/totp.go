// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"image/png"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/kushalpandya/wargo/internal/httpx"
)

type totpState struct {
	Secret  string `json:"secret"` // sealed
	Enabled bool   `json:"enabled"`
	// LastStep prevents reuse of a code within its window.
	LastStep int64 `json:"lastStep"`
}

var totpMu sync.Mutex

func (s *Service) loadTOTP(ctx context.Context) (totpState, bool) {
	var t totpState
	ok, err := s.st.Get(ctx, "totp", &t)
	return t, ok && err == nil
}

func (s *Service) totpEnabled(ctx context.Context) bool {
	t, ok := s.loadTOTP(ctx)
	return ok && t.Enabled
}

func (s *Service) verifyTOTP(ctx context.Context, code string) error {
	totpMu.Lock()
	defer totpMu.Unlock()
	t, ok := s.loadTOTP(ctx)
	if !ok || t.Secret == "" {
		return errors.New("totp not configured")
	}
	secret, err := s.st.Unseal(t.Secret)
	if err != nil {
		return err
	}
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	now := time.Now()
	for _, skew := range []int64{0, -1, 1} {
		at := now.Add(time.Duration(skew) * 30 * time.Second)
		step := at.Unix() / 30
		if step <= t.LastStep {
			continue
		}
		valid, _ := totp.ValidateCustom(code, secret, at, totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if valid {
			t.LastStep = step
			return s.st.Set(ctx, "totp", t)
		}
	}
	return errors.New("invalid totp code")
}

func (s *Service) handleTOTPSetup(w http.ResponseWriter, r *http.Request) error {
	if s.totpEnabled(r.Context()) {
		return httpx.Errorf(http.StatusConflict, "totp_enabled", "Two-factor authentication is already enabled")
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Wargo", AccountName: s.cfg.Auth.User})
	if err != nil {
		return err
	}
	sealed, err := s.st.Seal(key.Secret())
	if err != nil {
		return err
	}
	if err := s.st.Set(r.Context(), "totp", totpState{Secret: sealed}); err != nil {
		return err
	}
	img, err := key.Image(240, 240)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	httpx.OK(w, map[string]string{
		"secret": key.Secret(),
		"url":    key.URL(),
		"qr":     "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
	})
	return nil
}

func (s *Service) handleTOTPEnable(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Code string `json:"code"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if err := s.verifyTOTP(r.Context(), req.Code); err != nil {
		return httpx.Errorf(http.StatusBadRequest, "invalid_code", "That code didn't match. Check your device clock and try again.")
	}
	t, _ := s.loadTOTP(r.Context())
	t.Enabled = true
	if err := s.st.Set(r.Context(), "totp", t); err != nil {
		return err
	}
	codes, err := s.newRecoveryCodes(r.Context())
	if err != nil {
		return err
	}
	// Upgrade the current session: it has now proven a second factor.
	if sess := FromContext(r.Context()); sess != nil && sess.Method == MethodPassword {
		s.st.DB.ExecContext(r.Context(), `UPDATE sessions SET method = ? WHERE id_hash = ?`, MethodPasswordTOTP, sess.IDHash)
	}
	s.audit.Record(r, "auth.totp_enable", "", nil, nil)
	httpx.OK(w, map[string]any{"recoveryCodes": codes})
	return nil
}

func (s *Service) handleTOTPDisable(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Password string `json:"password"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if err := s.reauth(r, req.Password); err != nil {
		return err
	}
	if s.cfg.Auth.RequireTOTP {
		var n int
		s.st.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM webauthn_credentials`).Scan(&n)
		if n == 0 {
			return httpx.Errorf(http.StatusConflict, "totp_required", "Two-factor authentication is required. Add a passkey first.")
		}
	}
	err := s.st.Delete(r.Context(), "totp")
	s.audit.Record(r, "auth.totp_disable", "", err, nil)
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]bool{"ok": true})
	return nil
}

// ---- recovery codes ----

func (s *Service) newRecoveryCodes(ctx context.Context) ([]string, error) {
	tx, err := s.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM recovery_codes`); err != nil {
		return nil, err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	codes := make([]string, 10)
	for i := range codes {
		b := make([]byte, 8)
		rand.Read(b)
		c := strings.ToLower(enc.EncodeToString(b))[:12]
		codes[i] = c[:4] + "-" + c[4:8] + "-" + c[8:]
		if _, err := tx.Exec(`INSERT INTO recovery_codes(hash) VALUES(?)`, hashToken(c)); err != nil {
			return nil, err
		}
	}
	return codes, tx.Commit()
}

func (s *Service) useRecoveryCode(ctx context.Context, code string) error {
	c := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(code))
	res, err := s.st.DB.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ? WHERE hash = ? AND used_at IS NULL`,
		time.Now().Unix(), hashToken(c))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Service) handleRecoveryRegenerate(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Password string `json:"password"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if err := s.reauth(r, req.Password); err != nil {
		return err
	}
	codes, err := s.newRecoveryCodes(r.Context())
	s.audit.Record(r, "auth.recovery_regenerate", "", err, nil)
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"recoveryCodes": codes})
	return nil
}
