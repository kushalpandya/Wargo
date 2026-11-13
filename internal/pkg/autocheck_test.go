// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/notify"
	"github.com/kushalpandya/wargo/internal/server"
	"github.com/kushalpandya/wargo/internal/store"
)

func testService(t *testing.T) *Service {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Service{d: &server.Deps{Store: st, Audit: audit.New(st, httpx.NewProxyTrust(nil))}, sched: newSchedule()}
	s.sched.Store, s.sched.Audit = st, s.d.Audit
	return s
}

func TestCheckSettings(t *testing.T) {
	s := testService(t)
	if got := s.sched.Load(context.Background()); got != 24 {
		t.Fatalf("default %d", got)
	}
	put := func(body string) int {
		w := httptest.NewRecorder()
		httpx.Handler(s.sched.HandlePut)(w, httptest.NewRequest(http.MethodPut, "/api/pkg/settings", strings.NewReader(body)))
		return w.Code
	}
	if c := put(`{"checkHours":2}`); c != 400 {
		t.Fatalf("odd interval accepted: %d", c)
	}
	if c := put(`{"checkHours":0}`); c != 200 || s.sched.Load(context.Background()) != 0 {
		t.Fatalf("off: %d", c)
	}
	select {
	case <-s.sched.Wake():
	default:
		t.Fatal("background check was not woken")
	}
}

// failBackend fails every check (e.g. the host is offline).
type failBackend struct{ *aptBackend }

func (failBackend) Check(context.Context, Runner) ([]Update, []Update, error) {
	return nil, nil, errors.New("mirror unreachable")
}

func TestBackgroundFailureKeepsList(t *testing.T) {
	s := testService(t)
	s.be = failBackend{&aptBackend{}}
	s.run = func(context.Context, ...string) ([]byte, []byte, int, error) { return nil, nil, 1, errors.New("no") }
	at := time.Now().Add(-time.Hour)
	s.last = &CheckResult{At: at, Updates: []Update{{Name: "bash"}}, Extras: []Update{}}
	res, err := s.list(context.Background(), true)
	if err == nil || len(res.Updates) != 1 || !res.At.Equal(at) || res.Error == "" {
		t.Fatalf("background failure replaced list: %+v %v", res, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.list(ctx, true); err == nil || s.last.Error != res.Error {
		t.Fatal("canceled run should not save")
	}
	if res, err := s.list(context.Background(), false); err == nil || len(res.Updates) != 1 || res.Error == "" {
		t.Fatalf("manual failure replaced list: %+v %v", res, err)
	}
	s.last = nil
	if res, _ := s.list(context.Background(), false); len(res.Updates) != 0 || res.Error == "" {
		t.Fatalf("first failure: %+v", res)
	}
}

// shBackend fails every check and is installed everywhere.
type shBackend struct{ failBackend }

func (shBackend) Binary() string { return "sh" }

// A saved failed check is no baseline: after a restart, the automatic
// check that finds the packages pending before it does not announce them
// again.
func TestStartupSkipsFailedCheck(t *testing.T) {
	s := testService(t)
	s.be = shBackend{failBackend{&aptBackend{}}}
	ctx := context.Background()
	idle := func(now time.Time) time.Time { return now }
	hub := events.NewHub()
	// Before the restart: bash was pending and the owner cleared the item.
	n, err := notify.New(s.d.Store, hub, idle)
	if err != nil {
		t.Fatal(err)
	}
	n.ReportUpdates(ctx, "containers", nil, false)
	n.ReportUpdates(ctx, "homebrew", nil, false)
	n.ReportUpdates(ctx, "system", []string{"bash"}, false)
	if err := n.ClearAll(ctx); err != nil {
		t.Fatal(err)
	}
	n.Close()
	// A manual check then failed, and Wargo restarted.
	s.last = &CheckResult{At: time.Now(), Updates: []Update{}, Extras: []Update{}, Error: "mirror unreachable"}
	if s.d.Notify, err = notify.New(s.d.Store, hub, idle); err != nil {
		t.Fatal(err)
	}
	defer s.d.Notify.Close()
	s.reportSaved(ctx)
	s.d.Notify.ReportUpdates(ctx, "containers", nil, false)
	s.d.Notify.ReportUpdates(ctx, "homebrew", nil, false)
	s.reportUpdates(ctx, &CheckResult{Updates: []Update{{Name: "bash"}}}, true)
	snap, err := s.d.Notify.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range snap.Recent {
		if it.Category == notify.UpdatesFound {
			t.Fatalf("announced again: %+v", it)
		}
	}
}
