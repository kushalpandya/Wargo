// SPDX-License-Identifier: AGPL-3.0-only

package dashicons

import "testing"

func TestGuessSlug(t *testing.T) {
	for in, want := range map[string]string{
		"lscr.io/linuxserver/prowlarr:latest": "prowlarr",
		"plexinc/pms-docker":                  "plex",
		"nextcloud:28":                        "nextcloud",
		"ghcr.io/foo/bar@sha256:abc":          "bar",
		"":                                    "",
	} {
		if got := GuessSlug(in); got != want {
			t.Errorf("GuessSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSniff(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	if Sniff(png) != "png" || ContentType("png") != "image/png" {
		t.Fatal("png")
	}
	if Sniff([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)) != "svg" {
		t.Fatal("svg")
	}
	if Sniff([]byte("<html><script>alert(1)</script></html>")) != "" {
		t.Fatal("html must be rejected")
	}
	if !ValidSlug("home-assistant") || ValidSlug("../etc") {
		t.Fatal("slug validation")
	}
}
