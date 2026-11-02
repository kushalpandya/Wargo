// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestProbeTarget(t *testing.T) {
	cases := []struct {
		name string
		c    Container
		want string
	}{
		{"own healthcheck is never probed", Container{State: "running", Health: "healthy", Image: "x", Ports: []Port{{Private: 80, Public: 8080, Type: "tcp"}}}, ""},
		{"stopped", Container{State: "exited", Image: "x", Ports: []Port{{Private: 80, Public: 8080, Type: "tcp"}}}, ""},
		{"self", Container{State: "running", Self: true, Image: "wargo", Ports: []Port{{Private: 80, Public: 8080, Type: "tcp"}}}, ""},
		{"known app port wins", Container{State: "running", Image: "lscr.io/linuxserver/prowlarr", Ports: []Port{{Private: 9696, Public: 9696, Type: "tcp"}}}, "127.0.0.1:9696"},
		{"web port preferred over others", Container{State: "running", Image: "acme/app", Ports: []Port{{Private: 5432, Public: 5432, Type: "tcp"}, {Private: 80, Public: 8081, Type: "tcp"}}}, "127.0.0.1:8081"},
		{"non-web ports are skipped", Container{State: "running", Image: "mariadb", Ports: []Port{{Private: 3306, Public: 3306, Type: "tcp"}}}, ""},
		{"specific bind address kept", Container{State: "running", Image: "nextcloud", Ports: []Port{{IP: "10.0.0.5", Private: 80, Public: 8000, Type: "tcp"}}}, "10.0.0.5:8000"},
		{"host network uses known port", Container{State: "running", Image: "lscr.io/linuxserver/plex", NetworkMode: "host"}, "127.0.0.1:32400"},
		{"host network unknown app", Container{State: "running", Image: "acme/app", NetworkMode: "host"}, ""},
		{"shared network uses own known port only", Container{State: "running", Image: "linuxserver/transmission", NetworkContainer: "gluetun",
			NetworkPorts: []Port{{Private: 8888, Public: 8888, Type: "tcp"}, {Private: 9091, Public: 9091, Type: "tcp"}}}, "127.0.0.1:9091"},
	}
	for _, tc := range cases {
		if got, _ := probeTarget(tc.c); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestProbeCheck(t *testing.T) {
	p := newProber()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("probe should identify itself")
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	r := p.check(context.Background(), addr, 1)
	if r.Status != "up" || r.HTTPStatus != http.StatusFound {
		t.Fatalf("redirect should count as up without being followed: %+v", r)
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	ln.Close()
	if r := p.check(context.Background(), closed, 1); r.Status != "down" || r.Error != "connection refused" {
		t.Fatalf("closed port: %+v", r)
	}
}

func TestProbeNeedsTwoFailures(t *testing.T) {
	p := newProber()
	now := time.Unix(1_800_000_000, 0)
	p.now = func() time.Time { return now }
	c := Container{FullID: "abc", State: "running", Image: "acme/app", Ports: []Port{{Private: 80, Public: 8080, Type: "tcp"}}}
	if due := p.sync([]Container{c}); len(due) != 0 {
		t.Fatal("first probe must wait for the start delay")
	}
	now = now.Add(2 * probeFirstDelay)
	if due := p.sync([]Container{c}); len(due) != 1 {
		t.Fatal("probe due after delay")
	}
	if !p.record("abc", Probe{Status: "up"}) || p.result("abc").Status != "up" {
		t.Fatal("first success should be reported")
	}
	if p.record("abc", Probe{Status: "down"}) || p.result("abc").Status != "up" {
		t.Fatal("a single failure must not flip the badge")
	}
	if !p.record("abc", Probe{Status: "down"}) || p.result("abc").Status != "down" {
		t.Fatal("second consecutive failure reports down")
	}
	// Not due again until the interval passes: no hammering.
	now = now.Add(probeInterval / 2)
	if due := p.sync([]Container{c}); len(due) != 0 {
		t.Fatal("probed again too soon")
	}
	// Stopping the container forgets its state.
	c.State = "exited"
	p.sync([]Container{c})
	if p.result("abc") != nil {
		t.Fatal("state kept for a stopped container")
	}
}

func TestProbeIntervalAdvertised(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go http.Serve(ln, http.NotFoundHandler())
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(port)
	r := newProber().check(context.Background(), ln.Addr().String(), n)
	if r.Status != "up" || r.HTTPStatus != 404 || r.IntervalS != int(probeInterval/time.Second) {
		t.Fatalf("%+v", r)
	}
}
