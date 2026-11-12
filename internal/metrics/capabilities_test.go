// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/events"
)

func TestMetricsCapabilities(t *testing.T) {
	k, v := NewCollector(events.NewHub(), time.Second, time.Minute).Capabilities(context.Background())
	if k != "metrics" || v.(map[string]any)["sensors"] != (runtime.GOOS == "linux") {
		t.Fatal(k, v)
	}
}
