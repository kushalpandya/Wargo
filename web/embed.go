// SPDX-License-Identifier: AGPL-3.0-only

// Package web embeds the built single-page app.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built frontend (web/dist).
func Dist() fs.FS {
	sub, _ := fs.Sub(dist, "dist")
	return sub
}
