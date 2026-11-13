// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/netx"
	"github.com/kushalpandya/wargo/internal/store"
)

// Web Push registrations and sending. A registration belongs to the
// session that made it: it is only sent to while that session exists and
// has neither expired nor timed out, and is deleted once it has.

const (
	// vapidKey is the settings key holding the sealed VAPID private key.
	vapidKey = "notify.vapid"
	// pushTimeout bounds each request to a push service.
	pushTimeout = 10 * time.Second
	// pushTTL is how long a push service keeps an undelivered message.
	pushTTL = "86400"
	// maxEndpoint bounds a stored endpoint URL.
	maxEndpoint = 2048
)

// pushKeys and pushSubscription are the browser's PushSubscription JSON.
type pushKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type pushSubscription struct {
	Endpoint string   `json:"endpoint"`
	Keys     pushKeys `json:"keys"`
}

// pushPayload is what the service worker receives, once decrypted.
type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Link  string `json:"link"`
	Tag   string `json:"tag"`
	Badge int    `json:"badge"`
}

// loadVAPID returns the VAPID key, creating and storing it (sealed) the
// first time. If the stored key can't be unsealed (the instance key was
// replaced), a new one is made and every registration is dropped: they
// were made for the old public key and push services would refuse them.
func loadVAPID(ctx context.Context, st *store.Store) (*ecdsa.PrivateKey, error) {
	var sealed string
	ok, err := st.Get(ctx, vapidKey, &sealed)
	if err != nil {
		return nil, fmt.Errorf("VAPID key: %w", err)
	}
	if ok {
		key, err := unsealVAPID(st, sealed)
		if err == nil {
			return key, nil
		}
		slog.Warn("notifications: stored VAPID key is unreadable, making a new one; devices must enable push again", "err", err)
		if _, err := st.DB.ExecContext(ctx, `DELETE FROM notify_subs`); err != nil {
			return nil, err
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	raw, err := key.Bytes()
	if err != nil {
		return nil, err
	}
	if sealed, err = st.Seal(base64.RawURLEncoding.EncodeToString(raw)); err != nil {
		return nil, err
	}
	if err := st.Set(ctx, vapidKey, sealed); err != nil {
		return nil, fmt.Errorf("VAPID key: %w", err)
	}
	return key, nil
}

func unsealVAPID(st *store.Store, sealed string) (*ecdsa.PrivateKey, error) {
	plain, err := st.Unseal(sealed)
	if err != nil {
		return nil, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(plain)
	if err != nil {
		return nil, err
	}
	return ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
}

// newPushClient is the client for push services: bounded in time, and it
// never follows redirects (a push service answers directly).
func newPushClient() *http.Client {
	return &http.Client{Timeout: pushTimeout, Transport: netx.NewTransport(pushTimeout), CheckRedirect: netx.NoRedirects}
}

// publicKey is the VAPID public key as the browser's applicationServerKey.
func (c *Center) publicKey() (string, error) {
	pub, err := c.vapid.PublicKey.Bytes()
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(pub), nil
}

// decodeKey decodes a base64url key, with or without padding.
func decodeKey(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// subscribe stores s for session, taking over any row with the same
// endpoint (the browser re-registered, perhaps under a new session).
func (c *Center) subscribe(ctx context.Context, session string, s pushSubscription) error {
	if len(s.Endpoint) > maxEndpoint {
		return httpx.BadRequest("push endpoint is too long")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return httpx.BadRequest("push endpoint must be an https URL")
	}
	p256dh, err := decodeKey(s.Keys.P256dh)
	if err == nil {
		_, err = ecdh.P256().NewPublicKey(p256dh)
	}
	if err != nil {
		return httpx.BadRequest("keys.p256dh is not a P-256 public key")
	}
	auth, err := decodeKey(s.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return httpx.BadRequest("keys.auth must be 16 bytes")
	}
	_, err = c.st.DB.ExecContext(ctx, `INSERT INTO notify_subs(id, session, endpoint, p256dh, auth, created_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET session = excluded.session, p256dh = excluded.p256dh,
			auth = excluded.auth, created_at = excluded.created_at`,
		rand.Text(), session, s.Endpoint, base64.RawURLEncoding.EncodeToString(p256dh),
		base64.RawURLEncoding.EncodeToString(auth), c.now().UnixMilli())
	return err
}

// unsubscribe removes every registration of session.
func (c *Center) unsubscribe(ctx context.Context, session string) error {
	_, err := c.st.DB.ExecContext(ctx, `DELETE FROM notify_subs WHERE session = ?`, session)
	return err
}

// subscribed reports whether session has a registration.
func (c *Center) subscribed(ctx context.Context, session string) (bool, error) {
	var n int
	err := c.st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notify_subs WHERE session = ?`, session).Scan(&n)
	return n > 0, err
}

type sub struct {
	id, endpoint string
	p256dh, auth []byte
}

// liveSession is the condition on sessions s for a session that is still
// signed in: not past expires_at, and if not remembered, used within the
// idle limit (as internal/auth decides on each request).
const liveSession = `s.expires_at > ? AND (s.remember = 1 OR s.last_seen >= ?)`

// liveSubs deletes the registrations whose session is gone, expired or
// timed out and returns the rest.
func (c *Center) liveSubs(ctx context.Context, now time.Time) ([]sub, error) {
	exp, idle := now.Unix(), c.idleCutoff(now).Unix()
	if _, err := c.st.DB.ExecContext(ctx, `DELETE FROM notify_subs WHERE session NOT IN (
		SELECT s.id_hash FROM sessions s WHERE `+liveSession+`)`, exp, idle); err != nil {
		return nil, err
	}
	rows, err := c.st.DB.QueryContext(ctx, `SELECT n.id, n.endpoint, n.p256dh, n.auth FROM notify_subs n
		JOIN sessions s ON s.id_hash = n.session AND `+liveSession, exp, idle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sub
	for rows.Next() {
		var s sub
		var p256dh, auth string
		if err := rows.Scan(&s.id, &s.endpoint, &p256dh, &auth); err != nil {
			return nil, err
		}
		if s.p256dh, err = decodeKey(p256dh); err != nil {
			return nil, err
		}
		if s.auth, err = decodeKey(auth); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// sendPush is the Center's PushFunc: it sends in the background, so Raise
// never waits on a push service. After Close it sends nothing.
func (c *Center) sendPush(ctx context.Context, cat Category, key, title, body, link string) {
	ctx = context.WithoutCancel(ctx)
	p := pushPayload{Title: clip(title, 200), Body: clip(body, 300), Link: clip(link, 300), Tag: string(cat) + ":" + key}
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closed {
		return
	}
	c.sending.Go(func() { c.deliver(ctx, p) })
}

// closeWait is how long Close waits for sends still running.
var closeWait = 5 * time.Second

// Close stops sending and waits up to closeWait for the sends still
// running, so the store can be closed after it.
func (c *Center) Close() {
	c.closeMu.Lock()
	c.closed = true
	c.closeMu.Unlock()
	done := make(chan struct{})
	go func() {
		c.sending.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeWait):
		slog.Warn("notifications: push still sending at shutdown")
	}
}

func (c *Center) deliver(ctx context.Context, p pushPayload) {
	now := c.now()
	subs, err := c.liveSubs(ctx, now)
	if err != nil {
		slog.Error("notifications: push registrations", "err", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	snap, err := c.Snapshot(ctx)
	if err != nil {
		slog.Error("notifications: push badge", "err", err)
	}
	p.Badge = snap.Badge
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		slog.Error("notifications: push payload", "err", err)
		return
	}
	msg := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	var wg sync.WaitGroup
	for _, s := range subs {
		wg.Go(func() { c.sendOne(ctx, s, msg, now) })
	}
	wg.Wait()
}

// sendOne posts one encrypted message. Logs name the push service host
// only: the endpoint path identifies the device, and the payload and keys
// are never logged.
func (c *Center) sendOne(parent context.Context, s sub, msg []byte, now time.Time) {
	host := "?"
	if u, err := url.Parse(s.endpoint); err == nil {
		host = u.Host
	}
	ctx, cancel := context.WithTimeout(parent, pushTimeout)
	defer cancel()
	body, err := encryptPayload(s.p256dh, s.auth, msg)
	if err != nil {
		slog.Error("notifications: push encryption", "host", host, "err", err)
		return
	}
	auth, err := vapidHeader(s.endpoint, vapidSubject, c.vapid, now)
	if err != nil {
		slog.Error("notifications: push signature", "host", host, "err", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		slog.Error("notifications: push request", "host", host, "err", err)
		return
	}
	req.Header.Set("TTL", pushTTL)
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", auth)
	resp, err := c.pushClient.Do(req)
	if err != nil {
		// url.Error would print the whole endpoint.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		slog.Warn("notifications: push failed", "host", host, "err", err)
		return
	}
	defer netx.Drain(resp)
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		if _, err := c.st.DB.ExecContext(parent, `DELETE FROM notify_subs WHERE id = ?`, s.id); err != nil {
			slog.Error("notifications: delete push registration", "host", host, "err", err)
		}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		slog.Warn("notifications: push refused", "host", host, "status", resp.StatusCode)
	}
}

// clip shortens s to at most n runes. With the limits sendPush uses, the
// payload stays under maxPlaintext for any UTF-8 text.
func clip(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j] + "…"
		}
		i++
	}
	return s
}
