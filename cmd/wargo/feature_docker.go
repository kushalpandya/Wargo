// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"github.com/kushalpandya/wargo/internal/docker"
	"github.com/kushalpandya/wargo/internal/server"
)

func init() {
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) {
		return docker.New(d.Cfg.Docker.Socket, d.Hub, d.Jobs, d.Audit, d.Elevate).Configure(docker.Options{
			Store:          d.Store,
			DataDir:        d.Cfg.DataDir,
			UpdateInterval: d.Cfg.Docker.UpdateCheckInterval.Duration,
		}), nil
	})
}
