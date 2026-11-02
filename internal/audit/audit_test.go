// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

func newLog(t *testing.T) (*Log, *store.Store) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, httpx.NewProxyTrust(nil)), st
}

func TestRecordTruncatesAttackerControlledFields(t *testing.T) {
	l, st := newLog(t)
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.Header.Set("User-Agent", strings.Repeat("u", 60_000))
	l.Record(r, "auth.password", strings.Repeat("x", 1<<20), nil, map[string]string{"big": strings.Repeat("d", 1<<20)})
	var target, ua, details string
	st.DB.QueryRow(`SELECT target, ua, details FROM audit_log`).Scan(&target, &ua, &details)
	if len(target) > maxTarget+4 || len(ua) > maxUA+4 || len(details) > maxDetails+64 {
		t.Fatalf("not truncated: target %d ua %d details %d", len(target), len(ua), len(details))
	}
}

func TestPruneRetention(t *testing.T) {
	l, st := newLog(t)
	old := time.Now().Add(-MaxAge - time.Hour).Unix()
	st.DB.Exec(`INSERT INTO audit_log(ts, action, result) VALUES(?, 'old', 'ok')`, old)
	for i := 0; i < 3; i++ {
		st.DB.Exec(`INSERT INTO audit_log(ts, action, result) VALUES(?, 'new', 'ok')`, time.Now().Unix())
	}
	l.Prune()
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'old'`).Scan(&n)
	if n != 0 {
		t.Fatal("entry older than MaxAge kept")
	}
	st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n)
	if n != 3 {
		t.Fatalf("recent entries: %d", n)
	}
}

func TestPruneRowCap(t *testing.T) {
	l, st := newLog(t)
	old := maxRows
	maxRows = 100
	defer func() { maxRows = old }()
	tx, _ := st.DB.Begin()
	now := time.Now().Unix()
	for i := 0; i < maxRows+10; i++ {
		tx.Exec(`INSERT INTO audit_log(ts, action, result) VALUES(?, 'x', 'ok')`, now)
	}
	tx.Commit()
	l.Prune()
	var n, minID int
	st.DB.QueryRow(`SELECT COUNT(*), MIN(id) FROM audit_log`).Scan(&n, &minID)
	if n != maxRows || minID != 11 {
		t.Fatalf("rows %d min id %d", n, minID)
	}
}
