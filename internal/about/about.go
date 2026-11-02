// SPDX-License-Identifier: AGPL-3.0-only

// Package about reports facts about the running Wargo process itself.
package about

import (
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
)

type Service struct {
	version string
	dataDir string
	started time.Time
}

func New(version, dataDir string) *Service {
	return &Service{version: version, dataDir: dataDir, started: time.Now()}
}

// Self describes the Wargo process.
type Self struct {
	Version    string `json:"version"`
	GoVersion  string `json:"goVersion"`
	PID        int    `json:"pid"`
	RSSBytes   uint64 `json:"rssBytes"`
	HeapBytes  uint64 `json:"heapBytes"`
	Goroutines int    `json:"goroutines"`
	UptimeSec  int64  `json:"uptimeSec"`
	DataDir    string `json:"dataDir"`
	User       string `json:"user"`
}

// parseVmRSS extracts VmRSS (bytes) from /proc/<pid>/status contents.
func parseVmRSS(status string) uint64 {
	for _, l := range strings.Split(status, "\n") {
		if v, ok := strings.CutPrefix(l, "VmRSS:"); ok {
			f := strings.Fields(v)
			if len(f) == 0 {
				return 0
			}
			n, _ := strconv.ParseUint(f[0], 10, 64)
			if len(f) > 1 && strings.EqualFold(f[1], "kB") {
				n *= 1024
			}
			return n
		}
	}
	return 0
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system/self", func(w http.ResponseWriter, r *http.Request) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		b, _ := os.ReadFile("/proc/self/status")
		u := os.Getenv("USER")
		httpx.OK(w, Self{
			Version: s.version, GoVersion: runtime.Version(), PID: os.Getpid(),
			RSSBytes: parseVmRSS(string(b)), HeapBytes: ms.HeapAlloc, Goroutines: runtime.NumGoroutine(),
			UptimeSec: int64(time.Since(s.started).Seconds()), DataDir: s.dataDir, User: u,
		})
	})
}
