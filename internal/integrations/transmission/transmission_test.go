// SPDX-License-Identifier: AGPL-3.0-only

package transmission_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kushalpandya/wargo/internal/integrations/transmission"
	"github.com/kushalpandya/wargo/internal/integrations/transmission/transmissiontest"
)

func TestHandshakeAndAuth(t *testing.T) {
	f := transmissiontest.New("kushal", "s3cret")
	defer f.Close()
	ctx := context.Background()

	c := transmission.New(f.RPCURL(), "kushal", "s3cret")
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Version == "" || s.DownloadDir != "/downloads/complete" {
		t.Fatalf("session %+v", s)
	}
	if f.Handshakes != 1 {
		t.Fatalf("handshakes = %d, want 1", f.Handshakes)
	}
	// second call reuses the session id
	if _, err := c.Stats(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Handshakes != 1 {
		t.Fatalf("session id not reused: %d handshakes", f.Handshakes)
	}
	// rotated session id → transparent retry
	f.RotateSession("sid-2")
	if _, err := c.Torrents(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Handshakes != 2 {
		t.Fatalf("handshakes = %d, want 2", f.Handshakes)
	}

	bad := transmission.New(f.RPCURL(), "kushal", "wrong")
	if _, err := bad.Session(ctx); !errors.Is(err, transmission.ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestStartStopRemove(t *testing.T) {
	f := transmissiontest.New("", "")
	defer f.Close()
	ctx := context.Background()
	a := f.AddTorrent("debian-12.iso", 4, 0.4)
	b := f.AddTorrent("ubuntu-24.04.iso", 6, 1)
	c := transmission.New(f.RPCURL(), "", "")

	list, err := c.Torrents(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "debian-12.iso" || transmission.StatusName(list[1].Status) != "seeding" {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := c.Stop(ctx, transmission.ID{Num: a.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Get(a.ID); got.Status != 0 {
		t.Fatalf("not stopped: %+v", got)
	}
	if err := c.Start(ctx, transmission.ID{Hash: a.HashString}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Get(a.ID); got.Status != 4 {
		t.Fatalf("not started: %+v", got)
	}
	if err := c.Remove(ctx, transmission.ID{Num: a.ID}, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Get(a.ID); !got.Removed || got.DataDeleted {
		t.Fatalf("remove keep data: %+v", got)
	}
	if err := c.Remove(ctx, transmission.ID{Num: b.ID}, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Get(b.ID); !got.Removed || !got.DataDeleted {
		t.Fatalf("remove with data: %+v", got)
	}
}

func TestAdd(t *testing.T) {
	f := transmissiontest.New("", "")
	defer f.Close()
	ctx := context.Background()
	c := transmission.New(f.RPCURL(), "", "")
	m := "magnet:?xt=urn:btih:fe3f0564c4c5015a479ab7470319c3b866116932"
	a, err := c.Add(ctx, m, "")
	if err != nil || a.ID == 0 || a.Duplicate {
		t.Fatalf("add %+v %v", a, err)
	}
	d, err := c.Add(ctx, m, "")
	if err != nil || !d.Duplicate || d.ID != a.ID {
		t.Fatalf("duplicate %+v %v", d, err)
	}
	if _, err := c.Add(ctx, "", ""); err == nil {
		t.Fatal("expected error for empty add")
	}
	var rpcErr *transmission.RPCError
	if err := c.Call(ctx, "nope", nil, nil); !errors.As(err, &rpcErr) {
		t.Fatalf("want RPCError, got %v", err)
	}
}

func TestStatusName(t *testing.T) {
	want := []string{"stopped", "check-wait", "checking", "download-wait", "downloading", "seed-wait", "seeding"}
	for i, w := range want {
		if got := transmission.StatusName(i); got != w {
			t.Errorf("%d: %s", i, got)
		}
	}
}
