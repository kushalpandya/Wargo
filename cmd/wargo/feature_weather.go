// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"github.com/kushalpandya/wargo/internal/server"
	"github.com/kushalpandya/wargo/internal/weather"
)

func init() {
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) { return weather.New(d.Store, d.Audit), nil })
}
