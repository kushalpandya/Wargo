// SPDX-License-Identifier: AGPL-3.0-only

// Package metrics samples host metrics once and fans them out to every
// subscriber, keeping a short in-memory history for charts.
package metrics

import (
	"context"
	"math"
	"net/http"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/notify"

	hostfs "github.com/kushalpandya/wargo/internal/host"
)

const Topic = "metrics"

type CPU struct {
	Total   float64    `json:"total"`
	PerCore []float64  `json:"perCore"`
	Load    [3]float64 `json:"load"`
	FreqMHz float64    `json:"freqMHz"`
}

type Memory struct {
	Total     uint64  `json:"total"`
	Used      uint64  `json:"used"`
	Available uint64  `json:"available"`
	Cached    uint64  `json:"cached"`
	Buffers   uint64  `json:"buffers"`
	SwapTotal uint64  `json:"swapTotal"`
	SwapUsed  uint64  `json:"swapUsed"`
	UsedPct   float64 `json:"usedPct"`
}

type NetIf struct {
	Name    string   `json:"name"`
	RxRate  float64  `json:"rxRate"` // bytes/s
	TxRate  float64  `json:"txRate"`
	RxTotal uint64   `json:"rxTotal"`
	TxTotal uint64   `json:"txTotal"`
	Addrs   []string `json:"addrs,omitempty"`
	Link    *Link    `json:"link,omitempty"`
}

type DiskIO struct {
	Name      string  `json:"name"`
	ReadRate  float64 `json:"readRate"`
	WriteRate float64 `json:"writeRate"`
	Busy      float64 `json:"busy"` // 0..100
}

type Snapshot struct {
	TS     int64   `json:"ts"` // unix ms
	CPU    CPU     `json:"cpu"`
	Memory Memory  `json:"memory"`
	Temps  []Temp  `json:"temps"`
	Fans   []Fan   `json:"fans"`
	Net    []NetIf `json:"net"`
	NetRx  float64 `json:"netRx"`
	NetTx  float64 `json:"netTx"`
	// PrimaryIf is the interface carrying the default route.
	PrimaryIf string   `json:"primaryIf,omitempty"`
	Disks     []DiskIO `json:"disks"`
	DiskR     float64  `json:"diskRead"`
	DiskW     float64  `json:"diskWrite"`
	Uptime    uint64   `json:"uptime"`
	Procs     uint64   `json:"procs"`
	CPUTemp   float64  `json:"cpuTemp"`
}

// Point is a compact history sample.
type Point struct {
	TS      int64   `json:"t"`
	CPU     float64 `json:"cpu"`
	Mem     float64 `json:"mem"`
	Swap    float64 `json:"swap"`
	NetRx   float64 `json:"rx"`
	NetTx   float64 `json:"tx"`
	DiskR   float64 `json:"dr"`
	DiskW   float64 `json:"dw"`
	CPUTemp float64 `json:"temp"`
	Load1   float64 `json:"load"`
	// Cores is per-core usage, rounded to 0.1 to keep history small.
	Cores []float64 `json:"cores,omitempty"`
}

type Collector struct {
	hub      *events.Hub
	interval time.Duration
	capacity int

	// ReportDisks, when set before Run, receives how full each mounted
	// filesystem is every diskReportEvery (notify.Center.ReportDisks).
	ReportDisks func(ctx context.Context, list []notify.Disk)

	mu      sync.RWMutex
	latest  *Snapshot
	history []Point
	fs      []Filesystem
	fsAt    time.Time

	// Sampler-only state (sample runs on one goroutine).
	hwmon   hwmonSensors
	hwmonAt time.Time
	cpuFreq []string // scaling_cur_freq files
	freqAt  time.Time

	prevNet  map[string]net.IOCountersStat
	prevDisk map[string]disk.IOCountersStat
	prevAt   time.Time

	publicIP publicIP
}

// Capabilities reports what the metrics page can show on this host:
// temperature and fan sensors come from Linux hwmon only.
func (c *Collector) Capabilities(context.Context) (string, any) {
	return "metrics", map[string]any{"sensors": runtime.GOOS == "linux"}
}

func NewCollector(hub *events.Hub, interval, history time.Duration) *Collector {
	hub.Sticky(Topic)
	return &Collector{hub: hub, interval: interval, capacity: int(history / interval)}
}

// diskReportEvery is how often ReportDisks is called.
const diskReportEvery = 5 * time.Minute

// Run samples until ctx is done. It slows down when nobody is watching.
func (c *Collector) Run(ctx context.Context) {
	if c.ReportDisks != nil {
		go c.reportDisks(ctx)
	}
	cpu.PercentWithContext(ctx, 0, true) // prime
	c.sample(ctx)
	for {
		d := c.interval
		if c.hub.Subscribers(Topic) == 0 {
			d = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
			c.sample(ctx)
		}
	}
}

// reportDisks calls ReportDisks now and every diskReportEvery with the
// writable mounted filesystems. A listing that found nothing is skipped:
// it would mark every full disk as recovered.
func (c *Collector) reportDisks(ctx context.Context) {
	t := time.NewTicker(diskReportEvery)
	defer t.Stop()
	for {
		var list []notify.Disk
		for _, f := range filesystems(ctx) {
			if f.ReadOnly || f.Total == 0 {
				continue
			}
			list = append(list, notify.Disk{Mount: f.Mount, UsedPct: f.UsedPct})
		}
		if len(list) > 0 && ctx.Err() == nil {
			c.ReportDisks(ctx, list)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func interestingIf(name string) bool {
	if name == "lo" {
		return false
	}
	for _, p := range ifSkipPrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

func (c *Collector) sample(ctx context.Context) {
	now := time.Now()
	s := &Snapshot{TS: now.UnixMilli()}

	if per, err := cpu.PercentWithContext(ctx, 0, true); err == nil && len(per) > 0 {
		s.CPU.PerCore = per
		var sum float64
		for _, v := range per {
			sum += v
		}
		s.CPU.Total = sum / float64(len(per))
	}
	if l, err := load.AvgWithContext(ctx); err == nil {
		s.CPU.Load = [3]float64{l.Load1, l.Load5, l.Load15}
	}
	// Sensor and cpufreq file lists are rescanned once a minute (hotplug,
	// module loads) instead of globbing sysfs every second.
	if now.Sub(c.freqAt) > time.Minute {
		c.cpuFreq, c.freqAt = cpuFreqFiles(), now
	}
	s.CPU.FreqMHz = avgFreq(c.cpuFreq)

	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		s.Memory = Memory{Total: vm.Total, Used: vm.Used, Available: vm.Available, Cached: vm.Cached,
			Buffers: vm.Buffers, SwapTotal: vm.SwapTotal, SwapUsed: vm.SwapTotal - vm.SwapFree, UsedPct: vm.UsedPercent}
	}

	if now.Sub(c.hwmonAt) > time.Minute {
		c.hwmon, c.hwmonAt = scanHwmon(hostfs.Path("/sys/class/hwmon")), now
	}
	s.Temps, s.Fans = c.hwmon.read()
	for _, t := range s.Temps {
		if t.Kind == "cpu" {
			s.CPUTemp = t.Value
			break
		}
	}

	dt := now.Sub(c.prevAt).Seconds()
	if ios, err := net.IOCountersWithContext(ctx, true); err == nil {
		cur := map[string]net.IOCountersStat{}
		addrs := ifAddrs()
		for _, io := range ios {
			if !interestingIf(io.Name) {
				continue
			}
			cur[io.Name] = io
			n := NetIf{Name: io.Name, RxTotal: io.BytesRecv, TxTotal: io.BytesSent, Addrs: addrs[io.Name]}
			if p, ok := c.prevNet[io.Name]; ok && dt > 0 && io.BytesRecv >= p.BytesRecv && io.BytesSent >= p.BytesSent {
				n.RxRate = float64(io.BytesRecv-p.BytesRecv) / dt
				n.TxRate = float64(io.BytesSent-p.BytesSent) / dt
			}
			s.NetRx += n.RxRate
			s.NetTx += n.TxRate
			s.Net = append(s.Net, n)
		}
		slices.SortFunc(s.Net, func(a, b NetIf) int { return strings.Compare(a.Name, b.Name) })
		c.prevNet = cur
		names := make([]string, len(s.Net))
		for i, n := range s.Net {
			names[i] = n.Name
		}
		lk := GetLinks(names)
		for i := range s.Net {
			s.Net[i].Link = lk[s.Net[i].Name]
		}
		s.PrimaryIf = primaryIf()
	}

	if ios, err := disk.IOCountersWithContext(ctx); err == nil {
		cur := map[string]disk.IOCountersStat{}
		for name, io := range ios {
			if !physicalDisk(name) {
				continue
			}
			cur[name] = io
			d := DiskIO{Name: name}
			if p, ok := c.prevDisk[name]; ok && dt > 0 && io.ReadBytes >= p.ReadBytes && io.WriteBytes >= p.WriteBytes {
				d.ReadRate = float64(io.ReadBytes-p.ReadBytes) / dt
				d.WriteRate = float64(io.WriteBytes-p.WriteBytes) / dt
				d.Busy = min(100, float64(io.IoTime-p.IoTime)/(dt*10))
			}
			s.DiskR += d.ReadRate
			s.DiskW += d.WriteRate
			s.Disks = append(s.Disks, d)
		}
		slices.SortFunc(s.Disks, func(a, b DiskIO) int { return strings.Compare(a.Name, b.Name) })
		c.prevDisk = cur
	}
	c.prevAt = now

	if up, err := host.UptimeWithContext(ctx); err == nil {
		s.Uptime = up
	}
	s.Procs = procCount(ctx)

	swapPct := 0.0
	if s.Memory.SwapTotal > 0 {
		swapPct = float64(s.Memory.SwapUsed) / float64(s.Memory.SwapTotal) * 100
	}
	p := Point{TS: s.TS, CPU: s.CPU.Total, Mem: s.Memory.UsedPct, Swap: swapPct, NetRx: s.NetRx, NetTx: s.NetTx,
		DiskR: s.DiskR, DiskW: s.DiskW, CPUTemp: s.CPUTemp, Load1: s.CPU.Load[0]}
	if len(s.CPU.PerCore) > 0 {
		p.Cores = make([]float64, len(s.CPU.PerCore))
		for i, v := range s.CPU.PerCore {
			p.Cores[i] = math.Round(v*10) / 10
		}
	}

	c.mu.Lock()
	c.latest = s
	c.history = append(c.history, p)
	if over := len(c.history) - c.capacity; over > 0 {
		c.history = slices.Delete(c.history, 0, over)
	}
	c.mu.Unlock()
	c.hub.Publish(Topic, s)
}

// cpuMaxMHz is the highest cpuinfo_max_freq across cores: on hybrid CPUs
// (performance and efficiency cores) cpu0 isn't always the fastest.
func cpuMaxMHz() float64 {
	files, _ := filepath.Glob(hostfs.Path("/sys/devices/system/cpu") + "/cpu[0-9]*/cpufreq/cpuinfo_max_freq")
	var best int64
	for _, f := range files {
		if v, ok := readInt(f); ok && v > best {
			best = v
		}
	}
	return float64(best) / 1000
}

func cpuFreqFiles() []string {
	files, _ := filepath.Glob(hostfs.Path("/sys/devices/system/cpu") + "/cpu[0-9]*/cpufreq/scaling_cur_freq")
	return files
}

func avgFreq(files []string) float64 {
	if len(files) == 0 {
		return 0
	}
	var sum float64
	var n int
	for _, f := range files {
		if v, ok := readInt(f); ok {
			sum += float64(v)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n) / 1000
}

func ifAddrs() map[string][]string {
	out := map[string][]string{}
	ifs, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, i := range ifs {
		for _, a := range i.Addrs {
			if !strings.HasPrefix(a.Addr, "fe80") {
				out[i.Name] = append(out[i.Name], a.Addr)
			}
		}
	}
	return out
}

func (c *Collector) Latest() *Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest
}

func (c *Collector) History() []Point {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.history)
}

// Info is static-ish host information.
type Info struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	OSID     string `json:"osId"`
	Kernel   string `json:"kernel"`
	Arch     string `json:"arch"`
	CPUModel string `json:"cpuModel"`
	// CPUMaxMHz is the highest maximum clock of any core (0 if unknown).
	CPUMaxMHz      float64 `json:"cpuMaxMHz,omitempty"`
	Cores          int     `json:"cores"`
	Threads        int     `json:"threads"`
	MemTotal       uint64  `json:"memTotal"`
	BootTime       uint64  `json:"bootTime"`
	Virtualization string  `json:"virtualization,omitempty"`
	GoVersion      string  `json:"goVersion"`
	Specs          Specs   `json:"specs"`
}

func osRelease() (pretty, id string) {
	b, err := hostfs.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS, runtime.GOOS
	}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "PRETTY_NAME":
			pretty = v
		case "ID":
			id = v
		}
	}
	return
}

func GetInfo(ctx context.Context) Info {
	i := Info{Arch: runtime.GOARCH, GoVersion: runtime.Version()}
	if h, err := host.InfoWithContext(ctx); err == nil {
		i.Hostname, i.Kernel, i.BootTime = h.Hostname, h.KernelVersion, h.BootTime
		if h.VirtualizationRole == "guest" {
			i.Virtualization = h.VirtualizationSystem
		}
	}
	if pretty, id, ok := platformOSRelease(); ok {
		i.OS, i.OSID = pretty, id
	} else {
		i.OS, i.OSID = osRelease()
	}
	if ci, err := cpu.InfoWithContext(ctx); err == nil && len(ci) > 0 {
		i.CPUModel = strings.TrimSpace(ci[0].ModelName)
		// Apple silicon reports a bogus 4 MHz, so ignore implausible values.
		if runtime.GOOS == "darwin" && ci[0].Mhz >= 100 {
			i.CPUMaxMHz = ci[0].Mhz
		}
	}
	if i.CPUModel == "" {
		i.CPUModel = fallbackCPUModel()
	}
	if i.CPUMaxMHz == 0 {
		i.CPUMaxMHz = cpuMaxMHz()
	}
	i.Threads, _ = cpu.CountsWithContext(ctx, true)
	i.Cores, _ = cpu.CountsWithContext(ctx, false)
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		i.MemTotal = vm.Total
	}
	i.Specs = GetSpecs()
	return i
}

func (c *Collector) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system/info", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, GetInfo(r.Context()))
	})
	mux.HandleFunc("GET /api/system/logo", serveLogo)
	mux.HandleFunc("GET /api/system/metrics", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, c.Latest())
	})
	mux.HandleFunc("GET /api/system/metrics/history", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, map[string]any{"interval": c.interval.Milliseconds(), "points": c.History()})
	})
	mux.HandleFunc("GET /api/system/public-ip", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		httpx.OK(w, c.publicIP.get(r.Context()))
		return nil
	}))
	mux.HandleFunc("GET /api/system/storage", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		httpx.OK(w, c.Storage(r.Context()))
		return nil
	}))
}
