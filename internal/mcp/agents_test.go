// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kushalpandya/wargo/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCreateReturnsTokenOnceAndStoresHash(t *testing.T) {
	st := testStore(t)
	a := NewAgents(st)
	ctx := context.Background()
	ag, token, err := a.Create(ctx, "  OpenClaw ", LevelRead)
	if err != nil {
		t.Fatal(err)
	}
	rest, ok := strings.CutPrefix(token, "wargo_")
	if !ok || len(rest) != 43 || strings.Trim(rest, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		t.Fatalf("token %q", token)
	}
	if ag.Name != "OpenClaw" || ag.Level != LevelRead || ag.ID == "" {
		t.Fatalf("agent %+v", ag)
	}
	sum := sha256.Sum256([]byte(token))
	var hash string
	if err := st.DB.QueryRow(`SELECT token_hash FROM mcp_agents WHERE id = ?`, ag.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash %q", hash)
	}
	var n int
	st.DB.QueryRow(`SELECT count(*) FROM mcp_agents WHERE token_hash = ? OR id = ? OR name = ?`, token, token, token).Scan(&n)
	if n != 0 {
		t.Fatal("token stored in the table")
	}
	b, _ := json.Marshal(ag)
	if strings.Contains(string(b), hash) || strings.Contains(string(b), token) {
		t.Fatalf("agent JSON leaks a secret: %s", b)
	}
	got, ok, err := a.ByToken(ctx, token)
	if err != nil || !ok || got.ID != ag.ID {
		t.Fatalf("ByToken %+v %v %v", got, ok, err)
	}
	if _, ok, err := a.ByToken(ctx, token+"x"); ok || err != nil {
		t.Fatalf("wrong token: %v %v", ok, err)
	}
}

func TestCreateValidatesNameAndLevel(t *testing.T) {
	a := NewAgents(testStore(t))
	ctx := context.Background()
	for _, name := range []string{"", "   ", strings.Repeat("x", 65)} {
		if _, _, err := a.Create(ctx, name, LevelRead); err == nil {
			t.Fatalf("name %q accepted", name)
		}
	}
	if _, _, err := a.Create(ctx, strings.Repeat("x", 64), LevelRead); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Create(ctx, "ok", Level("root")); err == nil {
		t.Fatal("bad level accepted")
	}
}

func TestByTokenAfterRevoke(t *testing.T) {
	a := NewAgents(testStore(t))
	ctx := context.Background()
	ag, token, _ := a.Create(ctx, "x", LevelAdmin)
	if err := a.Revoke(ctx, ag.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := a.ByToken(ctx, token); ok || err != nil {
		t.Fatalf("revoked token: %v %v", ok, err)
	}
	if err := a.Revoke(ctx, ag.ID); err == nil {
		t.Fatal("revoking twice should fail")
	}
}

func TestLevelAllows(t *testing.T) {
	cases := []struct {
		have, need Level
		want       bool
	}{
		{LevelRead, LevelRead, true}, {LevelRead, LevelOperate, false}, {LevelRead, LevelAdmin, false},
		{LevelOperate, LevelRead, true}, {LevelOperate, LevelOperate, true}, {LevelOperate, LevelAdmin, false},
		{LevelAdmin, LevelRead, true}, {LevelAdmin, LevelOperate, true}, {LevelAdmin, LevelAdmin, true},
		{Level("bogus"), LevelRead, false},
	}
	for _, c := range cases {
		if got := c.have.Allows(c.need); got != c.want {
			t.Errorf("%s.Allows(%s) = %v", c.have, c.need, got)
		}
	}
}

func TestUpdateAndList(t *testing.T) {
	a := NewAgents(testStore(t))
	ctx := context.Background()
	ag, _, _ := a.Create(ctx, "one", LevelRead)
	operate, bad := LevelOperate, Level("x")
	if err := a.Update(ctx, ag.ID, nil, &operate); err != nil {
		t.Fatal(err)
	}
	two, empty, x := " two ", "", "x"
	if err := a.Update(ctx, ag.ID, &two, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Update(ctx, ag.ID, nil, &bad); err == nil {
		t.Fatal("bad level accepted")
	}
	if err := a.Update(ctx, ag.ID, &empty, nil); err == nil {
		t.Fatal("empty name accepted")
	}
	if err := a.Update(ctx, "nope", &x, nil); err == nil {
		t.Fatal("unknown id accepted")
	}
	list, err := a.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "two" || list[0].Level != LevelOperate {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestTouchIsThrottled(t *testing.T) {
	a := NewAgents(testStore(t))
	ctx := context.Background()
	ag, _, _ := a.Create(ctx, "x", LevelRead)
	a.Touch(ctx, ag.ID, "10.0.0.1")
	a.Touch(ctx, ag.ID, "10.0.0.2")
	list, _ := a.List(ctx)
	if list[0].LastIP != "10.0.0.1" || list[0].LastUsedAt.IsZero() {
		t.Fatalf("%+v", list[0])
	}
}

func TestSettingsDefaultsAndValidation(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	got, err := LoadSettings(ctx, st)
	if err != nil || got != (Settings{Enabled: false, Network: "local", ApprovalMinutes: 15}) {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []Settings{
		{Network: "local", ApprovalMinutes: 0},
		{Network: "local", ApprovalMinutes: 61},
		{Network: "lan", ApprovalMinutes: 15},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	want := Settings{Enabled: true, Network: "anywhere", ApprovalMinutes: 60}
	if err := SaveSettings(ctx, st, want); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadSettings(ctx, st); got != want {
		t.Fatalf("%+v", got)
	}
}
