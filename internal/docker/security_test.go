// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"strings"
	"testing"
)

func TestRedactURLCreds(t *testing.T) {
	l := redactLabels(map[string]string{
		"db":       "postgres://app:s3cret@db:5432/app",
		"homepage": "https://example.com/a@b",
		"authors":  "x",
	})
	if l["db"] != "postgres://[redacted]@db:5432/app" {
		t.Fatalf("db = %q", l["db"])
	}
	if l["homepage"] != "https://example.com/a@b" {
		t.Fatalf("homepage = %q", l["homepage"])
	}
	got := redactAll([]string{"CMD", "curl", "-f", "http://admin:pw@localhost/health"})
	if got[3] != "http://[redacted]@localhost/health" {
		t.Fatalf("got %q", got[3])
	}
}

func TestComposeLabelsTrusted(t *testing.T) {
	files := []string{"/srv/app/compose.yml"}
	real := map[string]string{
		labelProject: "app", labelService: "web", labelWorkingDir: "/srv/app", labelConfig: "/srv/app/compose.yml",
	}
	if !composeLabelsTrusted(real, map[string]string{"org.opencontainers.image.version": "1"}, "app", "web", "/srv/app", files) {
		t.Fatal("compose-created container should be trusted")
	}
	// Labels inherited from a (malicious) image are not.
	if composeLabelsTrusted(real, map[string]string{labelConfig: "/srv/app/compose.yml"}, "app", "web", "/srv/app", files) {
		t.Fatal("image-provided compose file label trusted")
	}
	if composeLabelsTrusted(real, map[string]string{labelProject: "app"}, "app", "web", "/srv/app", files) {
		t.Fatal("image-provided project label trusted")
	}
	// Flag-like or odd names and relative paths are rejected.
	for _, c := range []struct{ project, service, dir string }{
		{"-p", "web", "/srv/app"},
		{"app", "--file=/tmp/x.yml", "/srv/app"},
		{"app", "web", "srv/app"},
		{"a b", "web", "/srv/app"},
	} {
		if composeLabelsTrusted(map[string]string{}, nil, c.project, c.service, c.dir, files) {
			t.Errorf("trusted %+v", c)
		}
	}
	if composeLabelsTrusted(map[string]string{}, nil, "app", "web", "/srv/app", []string{"compose.yml"}) {
		t.Error("relative compose file trusted")
	}
}

// A container sharing the updated container's network that is Wargo itself
// must not be recreated (that would kill the running job and Wargo).
func TestRecreateSkipsSelfDependent(t *testing.T) {
	oldID := strings.Repeat("1", 64)
	selfID := strings.Repeat("2", 64)
	f := &fakeDocker{newImageID: "sha256:new", containers: map[string]*fakeContainer{
		oldID:  appContainer(oldID, "app", "p_default"),
		selfID: appContainer(selfID, "wargo", "container:"+oldID),
	}}
	s := startFake(t, f)
	s.selfID = selfID
	job := s.jobs.Start("t", "t", nil)
	if _, err := s.UpdateRecreate(context.Background(), job, "app"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if w := f.find("wargo"); w == nil || w.id != selfID {
		t.Fatalf("Wargo's own container was recreated: %+v", w)
	}
}
