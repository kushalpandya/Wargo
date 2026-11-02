// SPDX-License-Identifier: AGPL-3.0-only

package elevate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/kushalpandya/wargo/internal/store"
)

func TestBuildTrustStore(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	s := (&Service{}).WithStore(st)
	if _, _, err := s.buildTrustStore(ctx, []string{"a"}); err == nil {
		t.Fatal("expected error without user handle")
	}
	st.Set(ctx, "webauthn_user_id", "dXNlcg")
	for i, rp := range []string{"localhost", "wargo.example.com", "localhost"} {
		c, _ := json.Marshal(webauthn.Credential{ID: []byte{byte(i)}, PublicKey: []byte{1, 2}})
		st.DB.Exec(`INSERT INTO webauthn_credentials(id, name, data, rp_id, created_at) VALUES(?,?,?,?,0)`,
			string(rune('a'+i)), "key", string(c), rp)
	}
	cs, names, err := s.buildTrustStore(ctx, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if cs.UserHandle != "dXNlcg" || len(cs.Credentials) != 3 || len(cs.RPIDs) != 2 || len(names) != 3 {
		t.Fatalf("%+v", cs)
	}
	if _, _, err := s.buildTrustStore(ctx, []string{"zzz"}); err == nil {
		t.Fatal("unknown id accepted")
	}
}
