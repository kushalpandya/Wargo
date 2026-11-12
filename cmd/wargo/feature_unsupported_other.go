// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package main

import "github.com/kushalpandya/wargo/internal/server"

// OS updates and System Events need systemd, the journal and a Linux
// package manager; elsewhere they are reported as unsupported.
func init() {
	server.RegisterFeature(func(*server.Deps) (server.Registrar, error) { return server.Unsupported("pkg"), nil })
	server.RegisterFeature(func(*server.Deps) (server.Registrar, error) { return server.Unsupported("events"), nil })
}
