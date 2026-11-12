// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"github.com/kushalpandya/wargo/internal/brew"
	"github.com/kushalpandya/wargo/internal/server"
)

// Homebrew updates, on macOS and Linux alike.
func init() {
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) { return brew.New(d), nil })
}
