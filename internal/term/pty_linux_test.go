// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package term

import (
	"testing"

	"github.com/kushalpandya/wargo/internal/host"
)

func TestPtyPathsContainerMode(t *testing.T) {
	ptmx, dir := ptyPaths()
	if ptmx != "/dev/ptmx" || dir != "/dev/pts" {
		t.Fatalf("native: %q %q", ptmx, dir)
	}
	t.Cleanup(host.SetRoot("/host"))
	ptmx, dir = ptyPaths()
	if ptmx != "/host/dev/pts/ptmx" || dir != "/host/dev/pts" {
		t.Fatalf("container: %q %q", ptmx, dir)
	}
}
