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

	"github.com/kushalpandya/wargo/internal/host"
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
	// Container mode: Wargo runs in Docker (as root) and acts as User.
	Container   bool   `json:"container,omitempty"`
	HostRoot    string `json:"hostRoot,omitempty"`
	ContainerID string `json:"containerId,omitempty"`
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
		u := os.Getenv("USER")
		if host.Container() {
			u = host.AdminName()
		}
		out := Self{
			Version: s.version, GoVersion: runtime.Version(), PID: os.Getpid(),
			RSSBytes: selfRSS(), HeapBytes: ms.HeapAlloc, Goroutines: runtime.NumGoroutine(),
			UptimeSec: int64(time.Since(s.started).Seconds()), DataDir: s.dataDir, User: u,
		}
		if host.Container() {
			out.Container, out.HostRoot = true, host.Root()
			if id := host.SelfContainerID(); len(id) >= 12 {
				out.ContainerID = id[:12]
			}
		}
		httpx.OK(w, out)
	})
}
