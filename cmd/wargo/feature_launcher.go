// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"github.com/kushalpandya/wargo/internal/about"
	"github.com/kushalpandya/wargo/internal/launcher"
	"github.com/kushalpandya/wargo/internal/server"
)

func init() {
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) {
		return launcher.New(d.Store, d.Audit, d.Cfg.DataDir, d.Cfg.Docker.Socket), nil
	})
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) {
		return about.New(server.Version, d.Cfg.DataDir), nil
	})
}
