// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"github.com/kushalpandya/wargo/internal/pkg"
	"github.com/kushalpandya/wargo/internal/server"
)

func init() {
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) { return pkg.New(d), nil })
}
