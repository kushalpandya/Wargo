// SPDX-License-Identifier: AGPL-3.0-only

// Package dashicons knows about the dashboard-icons project: slugs for
// common self-hosted apps, the CDN they are served from, and which image
// formats Wargo accepts as icons. Shared by the launcher and container
// logos.
package dashicons

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"
)

// Base is the CDN prefix for dashboard-icons PNGs (append "<slug>.png").
const Base = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/"

// MaxSize is the largest icon accepted (512 KB).
const MaxSize = 512 << 10

var slugRe = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// ValidSlug reports whether s is a well-formed dashboard-icons slug.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// types maps Sniff extensions to MIME types.
var types = map[string]string{
	"png": "image/png", "svg": "image/svg+xml", "webp": "image/webp", "jpg": "image/jpeg",
}

// ContentType maps a Sniff extension to its MIME type.
func ContentType(ext string) string { return types[ext] }

// Sniff returns "png", "jpg", "webp" or "svg" for an accepted image, or "".
func Sniff(b []byte) string {
	switch http.DetectContentType(b) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	}
	head := b
	if len(head) > 1024 {
		head = head[:1024]
	}
	t := strings.ToLower(strings.TrimSpace(string(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")))))
	if (strings.HasPrefix(t, "<?xml") || strings.HasPrefix(t, "<svg") || strings.HasPrefix(t, "<!--") ||
		strings.HasPrefix(t, "<!doctype svg")) && bytes.Contains(bytes.ToLower(b), []byte("<svg")) {
		return "svg"
	}
	return ""
}

var slugAliases = map[string]string{
	"pms-docker":        "plex",
	"pms":               "plex",
	"plex-media-server": "plex",
	"adguardhome":       "adguard-home",
	"pihole":            "pi-hole",
	"homeassistant":     "home-assistant",
	"portainer-ce":      "portainer",
	"portainer-ee":      "portainer",
	"qbittorrent-nox":   "qbittorrent",
	"code-server":       "code-server",
	"casaos":            "casaos",
	"gitea":             "gitea",
	"syncthing":         "syncthing",
	"filebrowser":       "filebrowser",
	"uptime-kuma":       "uptime-kuma",
	"server":            "",
	"app":               "",
}

// GuessSlug derives a dashboard-icons slug from an image reference, e.g.
// "lscr.io/linuxserver/prowlarr:latest" -> "prowlarr".
func GuessSlug(image string) string {
	ref := image
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	parts := strings.Split(strings.ToLower(ref), "/")
	name := parts[len(parts)-1]
	if v, ok := slugAliases[name]; ok {
		name = v
	}
	if name == "" && len(parts) > 1 {
		// Generic repository name (e.g. vaultwarden/server): use the namespace.
		name = parts[len(parts)-2]
	}
	name = strings.TrimPrefix(name, "docker-")
	name = strings.TrimSuffix(name, "-docker")
	if v, ok := slugAliases[name]; ok && v != "" {
		name = v
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == '_' || r == '.':
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if !slugRe.MatchString(s) {
		return ""
	}
	return s
}
