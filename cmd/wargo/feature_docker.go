// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"io/fs"

	"github.com/kushalpandya/wargo/internal/docker"
	"github.com/kushalpandya/wargo/internal/server"
	"github.com/kushalpandya/wargo/web"
)

func init() {
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) {
		return docker.New(d.Cfg.Docker.Socket, d.Hub, d.Jobs, d.Audit, d.Elevate).Configure(docker.Options{
			Store:          d.Store,
			DataDir:        d.Cfg.DataDir,
			UpdateInterval: d.Cfg.Docker.UpdateCheckInterval.Duration,
			OwnIcon:        ownIcon(),
			Notify:         d.Notify,
			Version:        server.Version,
		}), nil
	})
}

// ownIcon is Wargo's app icon from the embedded web UI, used as the logo of
// containers running Wargo (nil if the UI was built without it).
func ownIcon() []byte {
	b, _ := fs.ReadFile(web.Dist(), "icon-192.png")
	return b
}
