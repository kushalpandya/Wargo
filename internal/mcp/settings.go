// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

const keySettings = "mcp.settings"

// Where MCP requests may come from.
const (
	NetworkLocal    = "local"
	NetworkAnywhere = "anywhere"
)

// Settings is what the owner chose about MCP. It is off until turned on.
type Settings struct {
	Enabled bool `json:"enabled"`
	// Network is "local" (the private network) or "anywhere".
	Network string `json:"network"`
	// ApprovalMinutes is how long an agent waits for the owner to approve
	// an action before it is declined.
	ApprovalMinutes int `json:"approvalMinutes"`
}

// DefaultSettings is MCP as it is before the owner touches it.
func DefaultSettings() Settings {
	return Settings{Enabled: false, Network: NetworkLocal, ApprovalMinutes: 15}
}

func (s Settings) Validate() error {
	if s.Network != NetworkLocal && s.Network != NetworkAnywhere {
		return httpx.BadRequest(`The network must be "local" or "anywhere"`)
	}
	if s.ApprovalMinutes < 1 || s.ApprovalMinutes > 60 {
		return httpx.BadRequest("The approval time must be 1 to 60 minutes")
	}
	return nil
}

// LoadSettings returns the stored settings, or the defaults when none are.
func LoadSettings(ctx context.Context, st *store.Store) (Settings, error) {
	s := DefaultSettings()
	if _, err := st.Get(ctx, keySettings, &s); err != nil {
		return DefaultSettings(), err
	}
	return s, nil
}

// SaveSettings validates and stores s.
func SaveSettings(ctx context.Context, st *store.Store, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	return st.Set(ctx, keySettings, s)
}
