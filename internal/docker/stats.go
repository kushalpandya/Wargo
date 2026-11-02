// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Stats is published on "containers.stats" keyed by short id.
type Stats struct {
	CPU      float64 `json:"cpu"`      // percent of total host CPU capacity
	Mem      uint64  `json:"mem"`      // usage minus page cache
	MemLimit uint64  `json:"memLimit"` // container limit or host memory
	NetRx    float64 `json:"netRx"`    // bytes/s
	NetTx    float64 `json:"netTx"`
}

type apiStats struct {
	Read     time.Time `json:"read"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  int    `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
}

type statsPrev struct {
	at          time.Time
	cpu, system uint64
	rx, tx      uint64
}

// computeStats derives rates from two one-shot samples (one-shot stats do
// not include precpu values).
func computeStats(a *apiStats, prev *statsPrev, now time.Time) (Stats, statsPrev) {
	var rx, tx uint64
	for _, n := range a.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}
	cur := statsPrev{at: now, cpu: a.CPUStats.CPUUsage.TotalUsage, system: a.CPUStats.SystemUsage, rx: rx, tx: tx}
	mem := a.MemoryStats.Usage
	inactive, ok := a.MemoryStats.Stats["inactive_file"] // cgroup v2
	if !ok {
		inactive = a.MemoryStats.Stats["total_inactive_file"] // cgroup v1
	}
	if inactive < mem {
		mem -= inactive
	}
	st := Stats{Mem: mem, MemLimit: a.MemoryStats.Limit}
	if prev != nil {
		if dc, ds := int64(cur.cpu-prev.cpu), int64(cur.system-prev.system); cur.cpu >= prev.cpu && cur.system > prev.system && ds > 0 {
			st.CPU = float64(dc) / float64(ds) * 100
		}
		if dt := now.Sub(prev.at).Seconds(); dt > 0 {
			if rx >= prev.rx {
				st.NetRx = float64(rx-prev.rx) / dt
			}
			if tx >= prev.tx {
				st.NetTx = float64(tx-prev.tx) / dt
			}
		}
	}
	return st, cur
}

func (s *Service) pollStats(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.hub.Subscribers(TopicStats) == 0 {
				s.mu.Lock()
				clear(s.statsPrev)
				s.mu.Unlock()
				continue
			}
			s.hub.Publish(TopicStats, s.sampleStats(ctx))
		}
	}
}

func (s *Service) sampleStats(ctx context.Context) map[string]Stats {
	out := map[string]Stats{}
	l, err := s.List(ctx, false)
	if err != nil {
		return out
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	next := map[string]statsPrev{}
	for _, c := range l {
		if c.State != "running" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(c Container) {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			var a apiStats
			if err := s.c.call(cctx, http.MethodGet, "/containers/"+c.FullID+"/stats?stream=false&one-shot=true", nil, &a); err != nil {
				return
			}
			s.mu.Lock()
			p, ok := s.statsPrev[c.FullID]
			s.mu.Unlock()
			var pp *statsPrev
			if ok {
				pp = &p
			}
			st, cur := computeStats(&a, pp, time.Now())
			mu.Lock()
			out[c.ID] = st
			next[c.FullID] = cur
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	s.mu.Lock()
	s.statsPrev = next
	s.mu.Unlock()
	return out
}
