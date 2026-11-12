// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package metrics

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestGetInfoDarwin(t *testing.T) {
	i := GetInfo(context.Background())
	if !strings.HasPrefix(i.OS, "macOS ") || i.OSID != "macos" || i.Specs.Init != "launchd" || i.Specs.Model == "" || i.Specs.Shell == "" {
		t.Fatalf("%+v", i)
	}
	if runtime.GOARCH == "arm64" && !strings.HasPrefix(i.CPUModel, "Apple M") {
		t.Fatal(i.CPUModel)
	}
	if !strings.Contains(i.Specs.Timezone, "/") {
		t.Fatal(i.Specs.Timezone)
	}
}
