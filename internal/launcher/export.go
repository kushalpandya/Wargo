// SPDX-License-Identifier: AGPL-3.0-only

package launcher

// Helpers shared with other features (e.g. container logos in
// internal/docker). They wrap the launcher's internals without changing
// launcher behaviour.

// DashboardIconsBase is the CDN prefix for dashboard-icons PNGs
// (append "<slug>.png").
const DashboardIconsBase = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/"

// MaxIconSize is the largest icon accepted (512 KB).
const MaxIconSize = maxIconSize

// GuessSlug derives a dashboard-icons slug from an image reference or a
// plain name, e.g. "lscr.io/linuxserver/prowlarr:latest" -> "prowlarr".
// It returns "" when nothing usable remains.
func GuessSlug(image string) string { return guessSlug(image) }

// ValidSlug reports whether s is a well-formed dashboard-icons slug.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// SniffIcon returns "png", "jpg", "webp" or "svg" for an accepted image, or "".
func SniffIcon(b []byte) string { return sniffIcon(b) }

// IconContentType maps a SniffIcon extension to its MIME type.
func IconContentType(ext string) string { return iconTypes[ext] }
