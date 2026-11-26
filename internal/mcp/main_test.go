// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"os"
	"testing"

	"github.com/kushalpandya/wargo/internal/brew"
)

// The test binary doubles as `wargo brew-askpass` for the simulated
// Homebrew upgrade's askpass script, as in the brew package's tests.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "brew-askpass" {
		os.Exit(brew.AskpassMain(os.Args[2:]))
	}
	os.Exit(m.Run())
}
