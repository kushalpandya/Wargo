// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"github.com/kushalpandya/wargo/internal/mcp"
	"github.com/kushalpandya/wargo/internal/server"
)

// Agents using Wargo over MCP, off until the owner turns it on.
func init() {
	server.RegisterFeature(func(d *server.Deps) (server.Registrar, error) { return mcp.New(d) })
}
