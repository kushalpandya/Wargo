// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"github.com/kushalpandya/wargo/internal/integrations"
	"github.com/kushalpandya/wargo/internal/server"
)

// One shared service backs three features so capabilities are reported
// under "prowlarr", "transmission" and "plex".
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
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) {
		return torrentsService(d).Plex(), nil
	})
}
