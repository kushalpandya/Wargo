// SPDX-License-Identifier: AGPL-3.0-only

package privd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/kushalpandya/wargo/internal/privproto"
)

const testOrigin = "https://wargo.example.com"

var b64 = base64.RawURLEncoding

// softAuth is a minimal software WebAuthn authenticator (ES256).
type softAuth struct {
	key   *ecdsa.PrivateKey
	id    []byte
	count uint32
	rpID  string
}

func newSoftAuth(t *testing.T, rpID string) *softAuth {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	rand.Read(id)
	return &softAuth{key: k, id: id, rpID: rpID}
}

func (a *softAuth) credential(t *testing.T) webauthn.Credential {
	pub, err := a.key.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	raw := pub.Bytes() // 0x04 || X || Y
	cose := webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: int64(webauthncose.EllipticKey), Algorithm: int64(webauthncose.AlgES256)},
		Curve:         int64(webauthncose.P256), XCoord: raw[1:33], YCoord: raw[33:],
	}
	pk, err := webauthncbor.Marshal(cose)
	if err != nil {
		t.Fatal(err)
	}
	return webauthn.Credential{ID: a.id, PublicKey: pk, Flags: webauthn.CredentialFlags{UserPresent: true, UserVerified: true}}
}

func (a *softAuth) assert(t *testing.T, challenge, origin string, uv bool, userHandle []byte) json.RawMessage {
	rpHash := sha256.Sum256([]byte(a.rpID))
	flags := byte(0x01)
	if uv {
		flags |= 0x04
	}
	a.count++
	authData := append(append(rpHash[:], flags), binary.BigEndian.AppendUint32(nil, a.count)...)
	cd, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": challenge, "origin": origin, "crossOrigin": false})
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, authData...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.id), "rawId": b64.EncodeToString(a.id), "type": "public-key",
		"response": map[string]string{
			"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(authData),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(userHandle),
		},
	})
	return out
}

type fixture struct {
	s      *Server
	auth   *softAuth
	handle []byte
	ran    [][]string
	cfg    Config
}

func newFixture(t *testing.T) *fixture {
	dir := t.TempDir()
	bin := filepath.Join(dir, "wargo")
	os.WriteFile(bin, []byte("#!/bin/true\n"), 0o755)
	cfg := Config{
		AllowedUID: os.Getuid(), Origins: []string{testOrigin}, WargoBinary: bin,
		Credentials: filepath.Join(dir, "credentials.json"), Socket: filepath.Join(dir, "privd.sock"),
		SystemdRun: "/usr/bin/systemd-run", Systemctl: "/usr/bin/systemctl",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	f := &fixture{auth: newSoftAuth(t, "wargo.example.com"), handle: []byte("user-handle-0123456789"), cfg: cfg}
	cs := CredentialStore{UserHandle: b64.EncodeToString(f.handle), RPIDs: []string{"wargo.example.com"}, Credentials: []webauthn.Credential{f.auth.credential(t)}}
	b, _ := json.Marshal(cs)
	os.WriteFile(cfg.Credentials, b, 0o600)
	f.s = New(cfg)
	f.s.run = func(_ context.Context, argv []string, stdin []byte) ([]byte, error) {
		f.ran = append(f.ran, argv)
		return []byte("done:" + string(stdin)), nil
	}
	return f
}

func (f *fixture) challenge(t *testing.T, action, target string) (id, challenge string) {
	r := f.s.Handle(context.Background(), privproto.Request{Op: "challenge", Action: action, Target: target, Origin: testOrigin})
	if !r.OK {
		t.Fatalf("challenge: %s", r.Error)
	}
	var opts struct {
		PublicKey struct {
			Challenge        string `json:"challenge"`
			UserVerification string `json:"userVerification"`
			RPID             string `json:"rpId"`
		} `json:"publicKey"`
	}
	json.Unmarshal(r.Options, &opts)
	if opts.PublicKey.UserVerification != "required" || opts.PublicKey.RPID != "wargo.example.com" {
		t.Fatalf("options: %s", r.Options)
	}
	return r.ChallengeID, opts.PublicKey.Challenge
}

func (f *fixture) helperArgv() []string {
	return []string{f.cfg.WargoBinary, "helper", "power", "reboot"}
}

func TestFullApproval(t *testing.T) {
	f := newFixture(t)
	id, ch := f.challenge(t, "system.power", "reboot")
	r := f.s.Handle(context.Background(), privproto.Request{
		Op: "run", ChallengeID: id, Action: "system.power", Target: "reboot", Origin: testOrigin,
		Assertion: f.auth.assert(t, ch, testOrigin, true, f.handle), Argv: f.helperArgv(), Stdin: []byte("in"),
	})
	if !r.OK || string(r.Output) != "done:in" || len(f.ran) != 1 {
		t.Fatalf("run: %+v", r)
	}
	// sign count persisted
	cs, _ := f.s.loadCreds()
	if cs.Credentials[0].Authenticator.SignCount != 1 {
		t.Fatalf("sign count %d", cs.Credentials[0].Authenticator.SignCount)
	}
	// replay of same challenge fails
	r = f.s.Handle(context.Background(), privproto.Request{
		Op: "run", ChallengeID: id, Action: "system.power", Target: "reboot", Origin: testOrigin,
		Assertion: f.auth.assert(t, ch, testOrigin, true, f.handle), Argv: f.helperArgv(),
	})
	if r.OK || !r.Rejected {
		t.Fatalf("replay accepted: %+v", r)
	}
}

func TestRejections(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *fixture, req *privproto.Request, ch string)
	}{
		{"wrong action", func(f *fixture, req *privproto.Request, ch string) { req.Action = "files.op" }},
		{"wrong target", func(f *fixture, req *privproto.Request, ch string) { req.Target = "poweroff" }},
		{"wrong origin", func(f *fixture, req *privproto.Request, ch string) { req.Origin = "https://evil.example.com" }},
		{"no uv", func(f *fixture, req *privproto.Request, ch string) {
			req.Assertion = f.auth.assert(t, ch, testOrigin, false, f.handle)
		}},
		{"wrong challenge", func(f *fixture, req *privproto.Request, ch string) {
			req.Assertion = f.auth.assert(t, b64.EncodeToString([]byte("0123456789abcdef0123456789abcdef")), testOrigin, true, f.handle)
		}},
		{"client origin mismatch", func(f *fixture, req *privproto.Request, ch string) {
			req.Assertion = f.auth.assert(t, ch, "https://evil.example.com", true, f.handle)
		}},
		{"other key", func(f *fixture, req *privproto.Request, ch string) {
			other := newSoftAuth(t, "wargo.example.com")
			other.id = f.auth.id
			req.Assertion = other.assert(t, ch, testOrigin, true, f.handle)
		}},
		{"wrong rp", func(f *fixture, req *privproto.Request, ch string) {
			f.auth.rpID = "evil.example.com"
			req.Assertion = f.auth.assert(t, ch, testOrigin, true, f.handle)
		}},
		{"garbage", func(f *fixture, req *privproto.Request, ch string) { req.Assertion = json.RawMessage(`{"id":1}`) }},
		{"unknown challenge", func(f *fixture, req *privproto.Request, ch string) { req.ChallengeID = "nope" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			id, ch := f.challenge(t, "system.power", "reboot")
			req := privproto.Request{Op: "run", ChallengeID: id, Action: "system.power", Target: "reboot", Origin: testOrigin,
				Assertion: f.auth.assert(t, ch, testOrigin, true, f.handle), Argv: f.helperArgv()}
			f.auth.count = 0
			c.mutate(f, &req, ch)
			r := f.s.Handle(context.Background(), req)
			if r.OK || !r.Rejected || len(f.ran) != 0 {
				t.Fatalf("not rejected: %+v", r)
			}
		})
	}
}

func TestCounterRegression(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 2; i++ {
		id, ch := f.challenge(t, "a", "b")
		if i == 1 {
			f.auth.count = 0 // cloned authenticator replays an old counter
		}
		r := f.s.Handle(context.Background(), privproto.Request{Op: "run", ChallengeID: id, Action: "a", Target: "b", Origin: testOrigin,
			Assertion: f.auth.assert(t, ch, testOrigin, true, f.handle), Argv: f.helperArgv()})
		if (i == 0) != r.OK {
			t.Fatalf("iteration %d: %+v", i, r)
		}
	}
}

func TestExpiry(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.s.now = func() time.Time { return now }
	id, ch := f.challenge(t, "a", "b")
	now = now.Add(challengeTTL + time.Second)
	r := f.s.Handle(context.Background(), privproto.Request{Op: "run", ChallengeID: id, Action: "a", Target: "b", Origin: testOrigin,
		Assertion: f.auth.assert(t, ch, testOrigin, true, f.handle), Argv: f.helperArgv()})
	if r.OK || !r.Rejected {
		t.Fatalf("expired accepted: %+v", r)
	}
}

func TestChallengeOriginNotConfigured(t *testing.T) {
	f := newFixture(t)
	r := f.s.Handle(context.Background(), privproto.Request{Op: "challenge", Action: "a", Origin: "https://other.example.com"})
	if r.OK {
		t.Fatal("accepted unknown origin")
	}
}

func TestPendingCap(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < maxPending+10; i++ {
		f.challenge(t, "a", "b")
	}
	if len(f.s.pending) > maxPending {
		t.Fatalf("pending grew to %d", len(f.s.pending))
	}
}

func TestDisallowedArgvAfterValidAssertion(t *testing.T) {
	f := newFixture(t)
	id, ch := f.challenge(t, "a", "b")
	r := f.s.Handle(context.Background(), privproto.Request{Op: "run", ChallengeID: id, Action: "a", Target: "b", Origin: testOrigin,
		Assertion: f.auth.assert(t, ch, testOrigin, true, f.handle), Argv: []string{"/bin/sh", "-c", "id"}})
	if r.OK || r.Rejected || len(f.ran) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestCheckArgv(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "wargo")
	os.WriteFile(bin, []byte("x"), 0o755)
	sysDir := fakeSystemBinDir(t, "pacman", "apt-get")
	c := Config{WargoBinary: bin, SystemdRun: "/usr/bin/systemd-run", Systemctl: "/usr/bin/systemctl"}
	ok := [][]string{
		{bin, "helper", "kill", "TERM", "123"},
		{"systemctl", "reboot"},
		{"/usr/bin/systemctl", "poweroff"},
		{"systemd-run", "--unit=wargo-upgrade-abc123", "--collect", "--quiet", "--setenv=DEBIAN_FRONTEND=noninteractive",
			"--property=StandardOutput=append:/var/log/wargo/upgrade-abc123.log", "--", sysDir + "/apt-get", "-y", "full-upgrade"},
		{"/usr/bin/systemd-run", "--unit=wargo-upgrade-1", sysDir + "/pacman", "-Syu", "--noconfirm"},
		{"systemd-run", "--unit=wargo-upgrade-1", bin, "helper", "fs", "x"},
		// Exactly what internal/pkg builds for an upgrade.
		{"systemd-run", "--unit=wargo-upgrade-0123abcd", "--collect", "--description=Wargo system upgrade (pamac)",
			"--property=Type=exec", "--property=StandardOutput=append:/var/lib/wargo/pkg-0123abcd.log",
			"--property=StandardError=append:/var/lib/wargo/pkg-0123abcd.log", "--setenv=DEBIAN_FRONTEND=noninteractive",
			"--setenv=LC_ALL=C", "--", bin, "helper", "pkg-run", "--", "pacman", "-Syu", "--noconfirm"},
		// Package database refresh.
		{bin, "helper", "pkg-run", "--", "apt-get", "update"},
	}
	bad := [][]string{
		{},
		{"wargo", "helper", "kill"},
		{bin, "serve"},
		{bin, "helper"},
		{"/tmp/wargo", "helper", "kill", "TERM", "1"},
		{"/bin/sh", "-c", "id"},
		{"systemctl", "start", "sshd"},
		{"systemctl", "reboot", "--force"},
		{"systemd-run", "/bin/sh"},
		{"systemd-run", "--unit=other", "pacman", "-Syu"},
		{"systemd-run", "--unit=wargo-upgrade-1", "pacman", "-Syu"}, // bare names are searched on systemd's PATH
		{"systemd-run", "--unit=wargo-upgrade-1", "/bin/sh", "-c", "id"},
		{"systemd-run", "--unit=wargo-upgrade-1", "--property=ExecStartPre=/bin/sh", "pacman", "-Syu"},
		{"systemd-run", "--unit=wargo-upgrade-1", "--property=StandardOutput=file:/etc/shadow", "pacman"},
		{"systemd-run", "--unit=wargo-upgrade-1", "--property=StandardOutput=file:/var/log/wargo/../../../etc/shadow", "pacman"},
		{"systemd-run", "--unit=wargo-upgrade-1", "--setenv=LD_PRELOAD=/tmp/x.so", "pacman"},
		{"systemd-run", "--unit=wargo-upgrade-1", "-p", "ExecStartPre=/bin/sh", "pacman"},
		{"systemd-run", "--unit=wargo-upgrade-1", "--uid=0", "pacman"},
		{"systemd-run", "--unit=wargo-upgrade-1", "/tmp/pacman"},
		{"systemd-run", "--unit=wargo-upgrade-1"},
		{"systemd-run", "--unit=wargo-upgrade-1", bin, "serve"},
		{bin, "helper", "kill\x00"},
	}
	for _, a := range ok {
		out, err := c.checkArgv(a)
		if err != nil {
			t.Errorf("%q rejected: %v", a, err)
			continue
		}
		if !strings.HasPrefix(out[0], "/") {
			t.Errorf("%q: not absolute %q", a, out[0])
		}
	}
	for _, a := range bad {
		if _, err := c.checkArgv(a); err == nil {
			t.Errorf("%q accepted", a)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	base := func() Config {
		c := defaults()
		c.AllowedUID = 1000
		c.Origins = []string{"https://wargo.example.com/"}
		return c
	}
	c := base()
	if err := c.Validate(); err != nil || c.Origins[0] != "https://wargo.example.com" {
		t.Fatal(err, c.Origins)
	}
	for name, mut := range map[string]func(*Config){
		"no uid":       func(c *Config) { c.AllowedUID = -1 },
		"root uid":     func(c *Config) { c.AllowedUID = 0 },
		"http origin":  func(c *Config) { c.Origins = []string{"http://nas.lan"} },
		"path origin":  func(c *Config) { c.Origins = []string{"https://nas.lan/x"} },
		"no origins":   func(c *Config) { c.Origins = nil },
		"rel binary":   func(c *Config) { c.WargoBinary = "wargo" },
		"dirty binary": func(c *Config) { c.WargoBinary = "/usr/../tmp/wargo" },
	} {
		c := base()
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c = base()
	c.Origins = []string{"http://localhost:5186"}
	if err := c.Validate(); err != nil {
		t.Error("localhost rejected", err)
	}
}

func TestCheckTrustedFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bin")
	os.WriteFile(p, []byte("x"), 0o755)
	if err := checkTrustedFile(p); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, 0o777)
	if err := checkTrustedFile(p); err == nil {
		t.Fatal("world-writable accepted")
	}
	if err := checkTrustedFile(dir); err == nil {
		t.Fatal("directory accepted")
	}
}

// TestSocketPeerUID runs the server on a real unix socket under a temp dir.
func TestSocketPeerUID(t *testing.T) {
	f := newFixture(t)
	// A short path keeps the socket under the 108-byte sun_path limit.
	dir, err := os.MkdirTemp("", "privd-")
	if err != nil {
		dir = t.TempDir()
	} else {
		t.Cleanup(func() { os.RemoveAll(dir) })
	}
	f.cfg.Socket = filepath.Join(dir, "privd.sock")
	ln, err := Listen(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	st, _ := os.Stat(f.cfg.Socket)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v", st.Mode().Perm())
	}
	go f.s.Serve(ln)

	call := func() privproto.Response { return dialStatus(t, f.cfg.Socket) }
	if r := call(); !r.OK || r.Credentials != 1 {
		t.Fatalf("status: %+v", r)
	}
	// A server with a different allowed uid must reject us. (A separate
	// server: mutating the running one's config would be a data race.)
	cfg2 := f.cfg
	cfg2.AllowedUID = os.Getuid() + 1
	cfg2.Socket = filepath.Join(dir, "privd2.sock")
	ln2, err := Listen(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	s2 := New(cfg2)
	go s2.Serve(ln2)
	if r := dialStatus(t, cfg2.Socket); r.OK || r.Error != "permission denied" {
		t.Fatalf("foreign uid accepted: %+v", r)
	}
}

func dialStatus(t *testing.T, socket string) privproto.Response {
	t.Helper()
	{
		c, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		json.NewEncoder(c).Encode(privproto.Request{Op: "status"})
		var r privproto.Response
		json.NewDecoder(c).Decode(&r)
		return r
	}
}

func TestLoadConfigRequiresRootOwned(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "privd.toml")
	body := "allowed_uid = 1000\norigins = [\"https://wargo.example.com\"]\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err != nil {
		t.Fatalf("unprivileged load: %v", err)
	}
	// As root, a file owned by this (non-root) test user is refused.
	old := requireRootOwner
	requireRootOwner = true
	t.Cleanup(func() { requireRootOwner = old })
	if _, err := LoadConfig(p); err == nil || !strings.Contains(err.Error(), "not owned by root") {
		t.Fatalf("user-owned config accepted: %v", err)
	}
	requireRootOwner = false
	os.Chmod(p, 0o666)
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("world-writable config accepted")
	}
}

func TestSystemdRunCommandMustBeTrusted(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "wargo")
	os.WriteFile(bin, []byte("x"), 0o755)
	c := Config{WargoBinary: bin, SystemdRun: "/usr/bin/systemd-run", Systemctl: "/usr/bin/systemctl"}
	argv := []string{"systemd-run", "--unit=wargo-upgrade-1", "--", bin, "helper", "pkg-run", "--", "pacman", "-Syu"}
	if _, err := c.checkArgv(argv); err != nil {
		t.Fatalf("trusted binary rejected: %v", err)
	}
	old := requireRootOwner
	requireRootOwner = true
	t.Cleanup(func() { requireRootOwner = old })
	if _, err := c.checkArgv(argv); err == nil || !strings.Contains(err.Error(), "not owned by root") {
		t.Fatalf("user-owned wargo binary accepted for systemd-run: %v", err)
	}
	requireRootOwner = false
	os.Chmod(bin, 0o777)
	if _, err := c.checkArgv(argv); err == nil {
		t.Fatal("writable wargo binary accepted for systemd-run")
	}
}

// fakeSystemBinDir points systemBinDirs at a temp dir holding empty
// executables with the given names.
func fakeSystemBinDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := systemBinDirs
	systemBinDirs = []string{dir}
	t.Cleanup(func() { systemBinDirs = old })
	return dir
}

func TestSystemdRunPackageManagerMustBeTrusted(t *testing.T) {
	// Two system dirs: one is user-writable (like /usr/local/bin) and holds
	// a planted pacman, the other holds the trusted one.
	local, usr := t.TempDir(), t.TempDir()
	for _, f := range []string{local + "/pacman", usr + "/pacman"} {
		os.WriteFile(f, []byte("x"), 0o755)
	}
	os.Chmod(local+"/pacman", 0o777)
	old := systemBinDirs
	systemBinDirs = []string{usr, local}
	t.Cleanup(func() { systemBinDirs = old })
	c := Config{WargoBinary: "/usr/local/bin/wargo", SystemdRun: "/usr/bin/systemd-run", Systemctl: "/usr/bin/systemctl"}
	run := func(cmd string) error {
		_, err := c.checkArgv([]string{"systemd-run", "--unit=wargo-upgrade-1", "--", cmd, "-Syu"})
		return err
	}
	if err := run("pacman"); err == nil {
		t.Error("bare package manager name accepted")
	}
	if err := run(usr + "/pacman"); err != nil {
		t.Errorf("trusted absolute path rejected: %v", err)
	}
	if err := run(local + "/pacman"); err == nil {
		t.Error("planted (writable) package manager accepted")
	}
	// Ownership: as root, a user-owned binary is refused.
	oldReq := requireRootOwner
	requireRootOwner = true
	t.Cleanup(func() { requireRootOwner = oldReq })
	if err := run(usr + "/pacman"); err == nil || !strings.Contains(err.Error(), "not owned by root") {
		t.Errorf("user-owned package manager accepted: %v", err)
	}
}
