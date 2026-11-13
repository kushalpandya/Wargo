// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package main

import (
	"context"

	"github.com/kushalpandya/wargo/internal/server"
)

// OS updates and System Events need systemd, the journal and a Linux
// package manager; elsewhere they are reported as unsupported.
func init() {
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) {
		// No system updates here: tell notifications so it does not wait
		// for them.
		if d.Notify != nil {
			d.Notify.ReportUpdates(context.Background(), "system", nil, false)
		}
		return server.Unsupported("pkg"), nil
	})
	server.RegisterFeature(func(*server.Deps) (server.Registrar, error) { return server.Unsupported("events"), nil })
}
