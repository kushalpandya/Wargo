// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"

	"github.com/kushalpandya/wargo/internal/docker"
	"github.com/kushalpandya/wargo/internal/procs"
	"github.com/kushalpandya/wargo/internal/server"
)

func init() {
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) {
		dc := docker.Shared(d.Cfg.Docker.Socket)
		resolve := func(ctx context.Context, id string) string {
			name, _ := dc.ContainerName(ctx, id)
			return name
		}
		return procs.New(d.Hub, d.Audit, d.Elevate, resolve), nil
	})
}
