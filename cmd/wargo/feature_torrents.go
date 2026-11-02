// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"github.com/kushalpandya/wargo/internal/integrations"
	"github.com/kushalpandya/wargo/internal/server"
)

// One shared service backs two features so capabilities are reported under
// both "prowlarr" and "transmission".
var torrentsSvc *integrations.Service

func torrentsService(d *server.Deps) *integrations.Service {
	if torrentsSvc == nil {
		torrentsSvc = integrations.New(d.Cfg, d.Store, d.Hub, d.Audit)
	}
	return torrentsSvc
}

func init() {
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) {
		return torrentsService(d).Prowlarr(), nil
	})
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) {
		return torrentsService(d).Transmission(), nil
	})
}
