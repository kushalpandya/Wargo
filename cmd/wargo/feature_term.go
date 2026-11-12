// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"github.com/kushalpandya/wargo/internal/server"
	"github.com/kushalpandya/wargo/internal/term"
)

func init() {
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) {
		return term.New(d.Audit, d.Hub.OriginPatterns, d.Cfg.Docker.Socket), nil
	})
}
