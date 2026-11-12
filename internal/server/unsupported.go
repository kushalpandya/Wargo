// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"net/http"
)

// Unsupported stands in for a feature this platform doesn't have (for
// example OS updates or System Events on macOS). It registers no routes and
// reports the feature as unsupported, so the UI hides it instead of showing
// a broken page.
func Unsupported(key string) Registrar { return unsupported{key} }

type unsupported struct{ key string }

func (unsupported) Register(*http.ServeMux) {}

func (u unsupported) Capabilities(context.Context) (string, any) {
	return u.key, map[string]any{"available": false, "supported": false}
}
