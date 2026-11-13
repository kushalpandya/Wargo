// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/netx"
	"github.com/kushalpandya/wargo/internal/store"
)

// testIdleCutoff is auth's idle cutoff with the default 30 minute limit.
func testIdleCutoff(now time.Time) time.Time { return now.Add(-31 * time.Minute) }

// b64 decodes the RFC's base64url values, which are shown with whitespace.
func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

// TestEncryptRFC8291Vector checks encrypt against the example in RFC 8291
// Section 5, with the inputs listed in Appendix A.
func TestEncryptRFC8291Vector(t *testing.T) {
	plaintext := b64(t, `V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24`)
	if string(plaintext) != "When I grow up, I want to be a watermelon" {
		t.Fatalf("plaintext = %q", plaintext)
	}
	asPublic := b64(t, `BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIg
		Dll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8`)
	asPrivate := b64(t, `yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw`)
	uaPublic := b64(t, `BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-
		JvLexhqUzORcx aOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4`)
	salt := b64(t, `DGv6ra1nlYgDCS1FRnbzlw`)
	authSecret := b64(t, `BTBZMqHH6r4Tts7J_aSIgg`)
	want := b64(t, `DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml
		mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT
		pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN`)

	priv, err := ecdh.P256().NewPrivateKey(asPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(priv.PublicKey().Bytes(), asPublic) {
		t.Fatal("application server key pair does not match the RFC")
	}
	got, err := encrypt(uaPublic, authSecret, plaintext, salt, priv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encrypt:\n got %s\nwant %s", base64.RawURLEncoding.EncodeToString(got), base64.RawURLEncoding.EncodeToString(want))
	}
}

func TestEncryptRejectsBadInput(t *testing.T) {
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	salt, auth := make([]byte, 16), make([]byte, 16)
	if _, err := encrypt([]byte{4, 1, 2, 3}, auth, []byte("x"), salt, priv); err == nil {
		t.Error("accepted a public key that is not on P-256")
	}
	if _, err := encrypt(ua.PublicKey().Bytes(), auth[:8], []byte("x"), salt, priv); err == nil {
		t.Error("accepted an 8 byte auth secret")
	}
	if _, err := encrypt(ua.PublicKey().Bytes(), auth, make([]byte, maxPlaintext+1), salt, priv); err == nil {
		t.Error("accepted a plaintext that does not fit in one push message")
	}
}

func TestVAPIDHeader(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	h, err := vapidHeader("https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV", vapidSubject, key, now)
	if err != nil {
		t.Fatal(err)
	}
	tok, k := parseVAPID(t, h)
	pub, _ := key.PublicKey.Bytes()
	if !bytes.Equal(b64(t, k), pub) {
		t.Fatal("k is not the VAPID public key")
	}
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(tok, claims, func(*jwt.Token) (any, error) { return &key.PublicKey, nil },
		jwt.WithValidMethods([]string{"ES256"}), jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil || !parsed.Valid {
		t.Fatalf("parse JWT: %v", err)
	}
	if aud, _ := claims.GetAudience(); len(aud) != 1 || aud[0] != "https://push.example.net" {
		t.Errorf("aud = %v", aud)
	}
	if exp, _ := claims.GetExpirationTime(); exp == nil || !exp.Equal(now.Add(12*time.Hour)) {
		t.Errorf("exp = %v, want %v", exp, now.Add(12*time.Hour))
	}
	if sub, _ := claims.GetSubject(); sub != "https://github.com/kushalpandya/wargo" {
		t.Errorf("sub = %q", sub)
	}
}

func parseVAPID(t *testing.T, h string) (tok, k string) {
	t.Helper()
	rest, ok := strings.CutPrefix(h, "vapid t=")
	if !ok {
		t.Fatalf("header %q", h)
	}
	tok, k, ok = strings.Cut(rest, ", k=")
	if !ok {
		t.Fatalf("header %q", h)
	}
	return tok, k
}

func TestVAPIDKeyKeptSealed(t *testing.T) {
	st := openStore(t)
	c1, err := New(st, events.NewHub(), testIdleCutoff)
	if err != nil {
		t.Fatal(err)
	}
	var sealed string
	if ok, err := st.Get(context.Background(), vapidKey, &sealed); err != nil || !ok {
		t.Fatalf("notify.vapid not stored: %v", err)
	}
	raw, err := c1.vapid.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, base64.RawURLEncoding.EncodeToString(raw)) || strings.Contains(sealed, base64.StdEncoding.EncodeToString(raw)) {
		t.Fatal("private key stored in the clear")
	}
	c2, err := New(st, events.NewHub(), testIdleCutoff)
	if err != nil {
		t.Fatal(err)
	}
	if !c1.vapid.Equal(c2.vapid) {
		t.Fatal("VAPID key changed across a restart")
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// device is a browser's push subscription, with the private key to read
// what it receives.
type device struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newDevice(t *testing.T) device {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return device{priv, auth}
}

func (d device) keys() pushKeys {
	return pushKeys{
		P256dh: base64.RawURLEncoding.EncodeToString(d.priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(d.auth),
	}
}

// decrypt is the receiving side of RFC 8291, written from the RFC so the
// test does not reuse encrypt's derivation.
func (d device) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	if len(body) < 86 || body[20] != 65 {
		t.Fatalf("bad header in %d byte body", len(body))
	}
	salt, rs, asPub := body[:16], binary.BigEndian.Uint32(body[16:20]), body[21:86]
	if rs != 4096 {
		t.Fatalf("rs = %d", rs)
	}
	pub, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := d.priv.ECDH(pub)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), d.priv.PublicKey().Bytes()...), asPub...)
	ikm, _ := hkdf.Key(sha256.New, secret, d.auth, string(info), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	blk, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(blk)
	plain, err := gcm.Open(nil, nonce, body[86:], nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatal("missing last record delimiter")
	}
	return plain[:len(plain)-1]
}

type received struct {
	path   string
	header http.Header
	body   []byte
}

// pushEnv is a Center with real Web Push, sending to a TLS test server.
type pushEnv struct {
	c      *Center
	st     *store.Store
	clock  *fakeClock
	srv    *httptest.Server
	status int

	mu  sync.Mutex
	got []received
}

func newPushEnv(t *testing.T) *pushEnv {
	t.Helper()
	st := openStore(t)
	c, err := New(st, events.NewHub(), testIdleCutoff)
	if err != nil {
		t.Fatal(err)
	}
	e := &pushEnv{c: c, st: st, clock: &fakeClock{t: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}, status: http.StatusCreated}
	c.now = e.clock.now
	e.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.got = append(e.got, received{r.URL.Path, r.Header.Clone(), b})
		status := e.status
		e.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(e.srv.Close)
	client := e.srv.Client()
	client.Timeout = pushTimeout
	client.CheckRedirect = netx.NoRedirects
	c.pushClient = client
	return e
}

// session adds a sessions row, used just now, expiring at exp.
func (e *pushEnv) session(t *testing.T, id string, exp time.Time) {
	t.Helper()
	e.sessionSeen(t, id, exp, e.clock.t, false)
}

// sessionSeen adds a sessions row last used at seen.
func (e *pushEnv) sessionSeen(t *testing.T, id string, exp, seen time.Time, remember bool) {
	t.Helper()
	if _, err := e.st.DB.Exec(`INSERT INTO sessions(id_hash, csrf, created_at, last_seen, expires_at, method, remember) VALUES(?,?,?,?,?,?,?)`,
		id, "csrf", seen.Unix(), seen.Unix(), exp.Unix(), "password", remember); err != nil {
		t.Fatal(err)
	}
}

func (e *pushEnv) subscribe(t *testing.T, session, path string, d device) {
	t.Helper()
	if err := e.c.subscribe(context.Background(), session, pushSubscription{Endpoint: e.srv.URL + path, Keys: d.keys()}); err != nil {
		t.Fatal(err)
	}
}

func (e *pushEnv) subCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.st.DB.QueryRow(`SELECT COUNT(*) FROM notify_subs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *pushEnv) raise(o Occurrence) {
	e.c.Raise(context.Background(), o)
	e.c.sending.Wait()
}

func TestPushSkipsDeadSessions(t *testing.T) {
	e := newPushEnv(t)
	live, dead := newDevice(t), newDevice(t)
	e.session(t, "live", e.clock.t.Add(time.Hour))
	e.session(t, "dead", e.clock.t.Add(-time.Minute))
	e.subscribe(t, "live", "/push/live", live)
	e.subscribe(t, "dead", "/push/dead", dead)
	e.subscribe(t, "gone", "/push/gone", newDevice(t)) // no session row at all

	e.raise(unhealthy("gluetun"))

	if len(e.got) != 1 {
		t.Fatalf("sent %d requests, want 1", len(e.got))
	}
	r := e.got[0]
	if r.path != "/push/live" {
		t.Fatalf("sent to %s", r.path)
	}
	for k, want := range map[string]string{"TTL": "86400", "Urgency": "normal", "Content-Encoding": "aes128gcm"} {
		if got := r.header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	tok, _ := parseVAPID(t, r.header.Get("Authorization"))
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(tok, claims, func(*jwt.Token) (any, error) { return &e.c.vapid.PublicKey, nil },
		jwt.WithValidMethods([]string{"ES256"}), jwt.WithTimeFunc(e.clock.now)); err != nil {
		t.Fatalf("VAPID JWT: %v", err)
	}
	if aud, _ := claims.GetAudience(); len(aud) != 1 || aud[0] != e.srv.URL {
		t.Errorf("aud = %v, want %s", aud, e.srv.URL)
	}

	var p struct {
		Title, Body, Link, Tag string
		Badge                  int
	}
	if err := json.Unmarshal(live.decrypt(t, r.body), &p); err != nil {
		t.Fatal(err)
	}
	if p.Title != "gluetun is unhealthy" || p.Link != "/containers/gluetun" || p.Tag != "container.unhealthy:gluetun" || p.Badge != 1 {
		t.Fatalf("payload = %+v", p)
	}

	var n int
	e.st.DB.QueryRow(`SELECT COUNT(*) FROM notify_subs WHERE session = 'live'`).Scan(&n)
	if n != 1 || e.subCount(t) != 1 {
		t.Fatalf("subs left: %d total, %d live", e.subCount(t), n)
	}
}

// TestPushSkipsIdleSessions: a session that is not remembered dies after
// the idle limit even though its expires_at is still ahead; a remembered
// one lives until expires_at however long ago it was used.
func TestPushSkipsIdleSessions(t *testing.T) {
	e := newPushEnv(t)
	now, longAgo := e.clock.t, e.clock.t.Add(-2*time.Hour)
	e.sessionSeen(t, "idle", now.Add(10*time.Hour), longAgo, false)
	e.sessionSeen(t, "remembered", now.Add(10*24*time.Hour), longAgo, true)
	e.subscribe(t, "idle", "/push/idle", newDevice(t))
	e.subscribe(t, "remembered", "/push/remembered", newDevice(t))

	e.raise(unhealthy("gluetun"))

	if len(e.got) != 1 || e.got[0].path != "/push/remembered" {
		paths := []string{}
		for _, r := range e.got {
			paths = append(paths, r.path)
		}
		t.Fatalf("sent to %v, want only /push/remembered", paths)
	}
	if on, _ := e.c.subscribed(context.Background(), "idle"); on {
		t.Fatal("idle session's registration was kept")
	}
	if n := e.subCount(t); n != 1 {
		t.Fatalf("%d subs left, want 1", n)
	}
}

func TestPushGoneDeletes(t *testing.T) {
	e := newPushEnv(t)
	e.status = http.StatusGone
	e.session(t, "s", e.clock.t.Add(time.Hour))
	e.subscribe(t, "s", "/push/a", newDevice(t))
	e.raise(unhealthy("gluetun"))
	if len(e.got) != 1 {
		t.Fatalf("sent %d requests, want 1", len(e.got))
	}
	if n := e.subCount(t); n != 0 {
		t.Fatalf("%d subs left after 410", n)
	}
}

func TestPushKeepsRowOnServerError(t *testing.T) {
	e := newPushEnv(t)
	e.status = http.StatusInternalServerError
	e.session(t, "s", e.clock.t.Add(time.Hour))
	e.subscribe(t, "s", "/push/a", newDevice(t))
	e.raise(unhealthy("gluetun"))
	if n := e.subCount(t); n != 1 {
		t.Fatalf("%d subs left after 500, want 1", n)
	}
}

func TestPushOnlyInPushMode(t *testing.T) {
	e := newPushEnv(t)
	e.session(t, "s", e.clock.t.Add(time.Hour))
	e.subscribe(t, "s", "/push/a", newDevice(t))
	if err := e.c.SetModes(context.Background(), map[Category]Mode{ContainerUnhealthy: ModeApp}); err != nil {
		t.Fatal(err)
	}
	e.raise(unhealthy("gluetun"))
	if len(e.got) != 0 {
		t.Fatalf("sent %d requests in app mode", len(e.got))
	}
}

func TestSubscribeReplacesEndpoint(t *testing.T) {
	e := newPushEnv(t)
	ctx := context.Background()
	d := newDevice(t)
	e.subscribe(t, "old", "/push/a", d)
	e.subscribe(t, "new", "/push/a", d)
	if n := e.subCount(t); n != 1 {
		t.Fatalf("%d rows for one endpoint", n)
	}
	if on, _ := e.c.subscribed(ctx, "old"); on {
		t.Fatal("old session still owns the endpoint")
	}
	if on, _ := e.c.subscribed(ctx, "new"); !on {
		t.Fatal("new session does not own the endpoint")
	}
	if err := e.c.unsubscribe(ctx, "new"); err != nil {
		t.Fatal(err)
	}
	if n := e.subCount(t); n != 0 {
		t.Fatalf("%d rows after unsubscribe", n)
	}
}

func TestSubscribeValidates(t *testing.T) {
	e := newPushEnv(t)
	good := newDevice(t).keys()
	cases := map[string]pushSubscription{
		"http endpoint":   {Endpoint: "http://push.example.net/a", Keys: good},
		"no host":         {Endpoint: "https:///a", Keys: good},
		"bad p256dh":      {Endpoint: "https://push.example.net/a", Keys: pushKeys{P256dh: "BAAA", Auth: good.Auth}},
		"short auth":      {Endpoint: "https://push.example.net/a", Keys: pushKeys{P256dh: good.P256dh, Auth: "AAAA"}},
		"long endpoint":   {Endpoint: "https://push.example.net/" + strings.Repeat("a", 4096), Keys: good},
		"userinfo in url": {Endpoint: "https://u:p@push.example.net/a", Keys: good},
	}
	for name, s := range cases {
		if err := e.c.subscribe(context.Background(), "s", s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPushRoutesNeedSession(t *testing.T) {
	e := newPushEnv(t)
	mux := http.NewServeMux()
	e.c.Register(mux)
	for _, m := range []string{"GET", "POST", "DELETE"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(m, "/api/notifications/push", strings.NewReader(`{}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session: %d", m, rec.Code)
		}
	}
}

// Close waits for sends still running, up to closeWait, and later pushes
// are dropped.
func TestCloseWaitsForSends(t *testing.T) {
	e := newPushEnv(t)
	release := make(chan struct{})
	var finished atomic.Bool
	e.c.sending.Go(func() {
		<-release
		finished.Store(true)
	})
	closed := make(chan struct{})
	go func() {
		e.c.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a send was running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-closed
	if !finished.Load() {
		t.Fatal("send did not finish")
	}
	d := newDevice(t)
	e.session(t, "s", e.clock.t.Add(time.Hour))
	e.subscribe(t, "s", "/a", d)
	e.raise(unhealthy("app"))
	if len(e.got) != 0 {
		t.Fatalf("pushed after Close: %d", len(e.got))
	}

	// A send that never ends holds Close for closeWait only.
	e2 := newPushEnv(t)
	defer func(w time.Duration) { closeWait = w }(closeWait)
	closeWait = 20 * time.Millisecond
	stuck := make(chan struct{})
	defer close(stuck)
	e2.c.sending.Go(func() { <-stuck })
	start := time.Now()
	e2.c.Close()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Close waited %v", d)
	}
}
