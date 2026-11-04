// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/elevate"
	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/jobs"
	"github.com/kushalpandya/wargo/internal/schedule"
	"github.com/kushalpandya/wargo/internal/store"
)

const (
	TopicList  = "containers"
	TopicStats = "containers.stats"
)

// Service is the containers feature.
type Service struct {
	selfID  string // Wargo's own container id (container mode)
	ownIcon []byte // Wargo's logo (PNG) for containers running Wargo
	c       *Client
	hub     *events.Hub
	jobs    *jobs.Manager
	audit   *audit.Log
	elevate *elevate.Service

	// settle is how long a recreated container must stay up (tests shorten it).
	settle time.Duration
	// healthPoll is how often a health check is polled while waiting for
	// it; selfPoll and selfWait pace finishing a self update (tests shorten them).
	healthPoll time.Duration
	selfPoll   time.Duration
	selfWait   time.Duration
	version    string // Wargo's own version (for self update records)
	// composeCmd runs `docker compose ...`; replaced in tests.
	composeAvailable func() bool

	mu        sync.Mutex
	list      []Container
	listAt    time.Time
	updates   map[string]UpdateCheck // key: image ref + "|" + image id
	updating  map[string]bool        // container full ids being updated
	statsPrev map[string]statsPrev
	caps      *capsCache
	probes    *prober // Wargo's own reachability checks

	runCtx        context.Context // set by Run
	selfFinishing bool            // finishSelfUpdate is running

	composeOnce sync.Once
	composeOK   bool

	// Optional (see Configure).
	st     *store.Store
	icons  *iconStore
	raw    []apiContainer       // last raw list (for icon lookups)
	images map[string]imageMeta // image id -> labels

	// Update checks: interval 0 disables the background check.
	checkInterval time.Duration
	checkDelay    time.Duration // first background check after start
	checkedAt     int64         // unix ms of the last completed full check
	nextCheckAt   int64
	checkDone     chan struct{} // non-nil while a check runs
	checkErr      error
	retryAt       time.Time       // set after a failed background check
	sched         *schedule.Hours // interval chosen in Settings → General
}

// Options configures the optional parts of the service.
type Options struct {
	// Store persists update-check results (key "docker.updates").
	Store *store.Store
	// DataDir enables container logos, cached in <DataDir>/icons/containers.
	DataDir string
	// OwnIcon is Wargo's own logo (PNG), shown for containers running
	// Wargo without looking anywhere online.
	OwnIcon []byte
	// UpdateInterval is how often images are checked in the background
	// (0 disables; the first check runs ~2 minutes after start).
	UpdateInterval time.Duration
	// Version is Wargo's own version, recorded around self updates.
	Version string
}

// Configure applies opts and loads persisted update results.
func (s *Service) Configure(o Options) *Service {
	s.st = o.Store
	s.checkInterval = o.UpdateInterval
	s.sched.Store = o.Store
	if h, ok := s.sched.Saved(context.Background()); ok {
		s.checkInterval = time.Duration(h) * time.Hour
	}
	if o.DataDir != "" {
		s.icons = newIconStore(o.DataDir)
	}
	s.ownIcon = o.OwnIcon
	s.version = o.Version
	s.loadUpdates()
	return s
}

type capsCache struct {
	at  time.Time
	val map[string]any
}

// New creates the service for the configured socket.
func New(socket string, hub *events.Hub, jm *jobs.Manager, al *audit.Log, el *elevate.Service) *Service {
	if hub != nil {
		hub.Sticky(TopicList)
	}
	s := &Service{
		c: Shared(socket), hub: hub, jobs: jm, audit: al, elevate: el, settle: 3 * time.Second,
		healthPoll: time.Second, selfPoll: 2 * time.Second, selfWait: 10 * time.Minute,
		updates: map[string]UpdateCheck{}, updating: map[string]bool{}, statsPrev: map[string]statsPrev{},
		probes: newProber(),
		images: map[string]imageMeta{}, checkDelay: 2 * time.Minute,
	}
	s.sched = s.newSchedule()
	s.sched.Audit = al
	s.composeAvailable = s.detectCompose
	s.selfID = host.SelfContainerID()
	return s
}

// isSelf reports whether id (short/full id or name) is Wargo's own
// container.
func (s *Service) isSelf(ctx context.Context, id string) bool {
	if s.selfID == "" || id == "" {
		return false
	}
	if strings.HasPrefix(s.selfID, id) {
		return true
	}
	if !validRef(id) {
		return false
	}
	var v struct {
		ID string `json:"Id"`
	}
	if err := s.c.call(ctx, http.MethodGet, "/containers/"+id+"/json", nil, &v); err != nil {
		return false
	}
	return v.ID == s.selfID
}

// errSelfUpdate explains why Wargo can't update its own container here.
func errSelfUpdate(reason string) error {
	return httpx.Errorf(http.StatusConflict, "self_container",
		"Wargo can't update its own container here: "+reason+". Update it from the host instead: run `docker compose pull && docker compose up -d` in the folder of your Wargo compose file.")
}

// Client exposes the underlying Docker API client for other features
// (e.g. the terminal's `docker exec`).
func (s *Service) Client() *Client { return s.c }

func (s *Service) originPatterns() []string {
	if s.hub == nil {
		return nil
	}
	return s.hub.OriginPatterns
}

func (s *Service) detectCompose() bool {
	s.composeOnce.Do(func() {
		if _, err := host.LookPath("docker"); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.composeOK = host.UserCommand(ctx, "docker", "compose", "version").Run() == nil
	})
	return s.composeOK
}

func casaosPresent() bool {
	st, err := host.Stat(casaosDir)
	return err == nil && st.IsDir()
}

// Capabilities reports Docker availability (cached for 15s).
func (s *Service) Capabilities(ctx context.Context) (string, any) {
	s.mu.Lock()
	if s.caps != nil && time.Since(s.caps.at) < 15*time.Second {
		v := s.caps.val
		s.mu.Unlock()
		return "docker", v
	}
	s.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out := map[string]any{"available": false, "casaos": casaosPresent()}
	if v, err := s.c.Ping(cctx); err != nil {
		out["error"] = friendlyErr(err)
	} else {
		out["available"] = true
		out["version"] = v.Version
		out["apiVersion"] = s.c.APIVersion()
		out["compose"] = s.composeAvailable()
		out["updateCheckIntervalMs"] = s.checkInterval.Milliseconds()
		out["icons"] = s.icons != nil
		out["selfUpdate"] = s.selfUpdatePossible(cctx)
	}
	s.mu.Lock()
	s.caps = &capsCache{at: time.Now(), val: out}
	s.mu.Unlock()
	return "docker", out
}

func friendlyErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "permission denied"):
		return "Permission denied on the Docker socket (add the Wargo user to the docker group)"
	case strings.Contains(msg, "no such file"), strings.Contains(msg, "connection refused"):
		return "Docker is not running or the socket was not found"
	}
	return msg
}

// dockerErr maps client errors to API errors.
func dockerErr(err error) error {
	if err == nil {
		return nil
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusNotFound:
			return httpx.Errorf(http.StatusNotFound, "not_found", ae.Message)
		case http.StatusConflict:
			return httpx.Errorf(http.StatusConflict, "conflict", ae.Message)
		case http.StatusBadRequest:
			return httpx.BadRequest(ae.Message)
		}
		return httpx.Errorf(http.StatusBadGateway, "docker_error", ae.Message)
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		return he
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return httpx.Errorf(http.StatusGatewayTimeout, "timeout", "Docker did not answer in time")
	}
	return httpx.Errorf(http.StatusServiceUnavailable, "docker_unavailable", friendlyErr(err))
}

func pathID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !validRef(id) {
		return "", httpx.BadRequest("invalid container id")
	}
	return id, nil
}

// List returns all containers (cached for 1s unless fresh is set).
func (s *Service) List(ctx context.Context, fresh bool) ([]Container, error) {
	s.mu.Lock()
	if !fresh && s.list != nil && time.Since(s.listAt) < time.Second {
		l := s.list
		s.mu.Unlock()
		return l, nil
	}
	s.mu.Unlock()
	var raw []apiContainer
	if err := s.c.call(ctx, http.MethodGet, "/containers/json?all=1", nil, &raw); err != nil {
		return nil, err
	}
	l := convertList(raw)
	s.enrich(ctx, raw, l)
	for i := range l {
		l[i].Self = s.selfID != "" && l[i].FullID == s.selfID
	}
	s.mu.Lock()
	for i := range l {
		if u, ok := s.updates[l[i].Image+"|"+l[i].ImageID]; ok {
			if u.UpdateAvailable != nil {
				v := *u.UpdateAvailable
				l[i].UpdateAvailable = &v
			}
			l[i].UpdateCheckedAt = u.CheckedAt
		}
	}
	s.mu.Unlock()
	for i := range l {
		if l[i].Health == "" && s.probes != nil {
			l[i].Probe = s.probes.result(l[i].FullID)
		}
	}
	s.mu.Lock()
	s.list, s.listAt, s.raw = l, time.Now(), raw
	s.mu.Unlock()
	return l, nil
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.list = nil
	s.mu.Unlock()
}

func (s *Service) publishList(ctx context.Context) {
	if s.hub == nil {
		return
	}
	l, err := s.List(ctx, true)
	if err != nil {
		return
	}
	s.hub.Publish(TopicList, l)
}

// Register mounts the containers API.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/containers", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		l, err := s.List(ctx, false)
		if err != nil {
			return dockerErr(err)
		}
		s.probes.viewed()
		httpx.OK(w, l)
		return nil
	}))
	mux.HandleFunc("GET /api/containers/updates", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		l, err := s.List(r.Context(), false)
		if err != nil {
			return dockerErr(err)
		}
		httpx.OK(w, s.updatesStatus(l))
		return nil
	}))
	mux.HandleFunc("POST /api/containers/check-updates", httpx.Handler(s.handleCheckUpdates))
	mux.HandleFunc("GET /api/containers/self-update", httpx.Handler(s.handleSelfUpdateStatus))
	mux.HandleFunc("GET /api/containers/settings", httpx.Handler(s.sched.HandleGet))
	mux.HandleFunc("PUT /api/containers/settings", httpx.Handler(s.sched.HandlePut))
	mux.HandleFunc("GET /api/containers/{id}/icon", httpx.Handler(s.handleIcon))
	mux.HandleFunc("GET /api/containers/{id}", httpx.Handler(s.handleDetails))
	mux.HandleFunc("GET /api/containers/{id}/logs", httpx.Handler(s.handleLogs))
	mux.HandleFunc("GET /ws/containers/{id}/logs", s.handleLogsWS)
	mux.HandleFunc("POST /api/containers/{id}/{action}", httpx.Handler(s.handleAction))
}

// --- details ---

type Details struct {
	Container
	Created    string   `json:"createdAt"`
	Path       string   `json:"path"`
	Args       []string `json:"args"`
	Entrypoint []string `json:"entrypoint"`
	Cmd        []string `json:"cmd"`
	WorkingDir string   `json:"workingDir,omitempty"`
	User       string   `json:"user,omitempty"`
	Hostname   string   `json:"hostname,omitempty"`
	Tty        bool     `json:"tty"`
	Env        []string `json:"env"` // names only
	State      struct {
		Status       string `json:"status"`
		Running      bool   `json:"running"`
		Paused       bool   `json:"paused"`
		Restarting   bool   `json:"restarting"`
		OOMKilled    bool   `json:"oomKilled"`
		Pid          int    `json:"pid"`
		ExitCode     int    `json:"exitCode"`
		Error        string `json:"error,omitempty"`
		StartedAt    string `json:"startedAt,omitempty"`
		FinishedAt   string `json:"finishedAt,omitempty"`
		RestartCount int    `json:"restartCount"`
	} `json:"stateInfo"`
	Mounts   []Mount           `json:"mounts"`
	Networks []Network         `json:"networks"`
	Restart  RestartPolicy     `json:"restartPolicy"`
	Labels   map[string]string `json:"labels"`
	Limits   Limits            `json:"limits"`
	Health   *Health           `json:"healthInfo,omitempty"`
	ImageInf *ImageInfo        `json:"imageInfo,omitempty"`
	Security struct {
		Privileged bool     `json:"privileged"`
		CapAdd     []string `json:"capAdd,omitempty"`
		Devices    int      `json:"devices"`
	} `json:"security"`
	LogDriver string `json:"logDriver"`
	Platform  string `json:"platform,omitempty"`
}

type Mount struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Mode        string `json:"mode,omitempty"`
	RW          bool   `json:"rw"`
}

type Network struct {
	Name     string   `json:"name"`
	IP       string   `json:"ip,omitempty"`
	Prefix   int      `json:"prefix,omitempty"`
	IPv6     string   `json:"ipv6,omitempty"`
	Gateway  string   `json:"gateway,omitempty"`
	Mac      string   `json:"mac,omitempty"`
	Aliases  []string `json:"aliases,omitempty"`
	DNSNames []string `json:"dnsNames,omitempty"`
}

type RestartPolicy struct {
	Name     string `json:"name"`
	MaxRetry int    `json:"maxRetry"`
}

type Limits struct {
	NanoCPUs   int64  `json:"nanoCpus"`
	CPUShares  int64  `json:"cpuShares"`
	CpusetCPUs string `json:"cpusetCpus,omitempty"`
	Memory     int64  `json:"memory"`
	MemorySwap int64  `json:"memorySwap"`
	PidsLimit  int64  `json:"pidsLimit"`
}

type Health struct {
	Status        string         `json:"status"`
	FailingStreak int            `json:"failingStreak"`
	Test          []string       `json:"test,omitempty"`
	IntervalNs    int64          `json:"intervalNs,omitempty"`
	TimeoutNs     int64          `json:"timeoutNs,omitempty"`
	Retries       int            `json:"retries,omitempty"`
	Log           []HealthResult `json:"log"`
}

type HealthResult struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
}

type ImageInfo struct {
	ID           string   `json:"id"`
	Tags         []string `json:"tags"`
	Digests      []string `json:"digests"`
	Created      string   `json:"created"`
	Size         int64    `json:"size"`
	Architecture string   `json:"architecture"`
	Os           string   `json:"os"`
}

type apiImage struct {
	ID           string   `json:"Id"`
	RepoTags     []string `json:"RepoTags"`
	RepoDigests  []string `json:"RepoDigests"`
	Created      string   `json:"Created"`
	Size         int64    `json:"Size"`
	Architecture string   `json:"Architecture"`
	Os           string   `json:"Os"`
	Descriptor   *struct {
		Digest string `json:"digest"`
	} `json:"Descriptor"`
	Config json.RawMessage `json:"Config"`
}

func buildDetails(in *inspect, summary *Container, img *apiImage) *Details {
	d := &Details{Created: in.Created, Path: in.Path, Args: redactAll(in.Args), Entrypoint: redactAll(in.Config.Entrypoint), Cmd: redactAll(in.Config.Cmd),
		WorkingDir: in.Config.WorkingDir, User: in.Config.User, Hostname: in.Config.Hostname, Tty: in.Config.Tty,
		Env: envNames(in.Config.Env), Labels: redactLabels(in.Config.Labels), LogDriver: in.HostConfig.LogConfig.Type, Platform: in.Platform}
	if summary != nil {
		d.Container = *summary
	} else {
		d.Container = Container{ID: shortID(in.ID), FullID: in.ID, Name: strings.TrimPrefix(in.Name, "/"), Image: in.Config.Image,
			ImageID: in.Image, State: in.State.Status, NetworkMode: in.HostConfig.NetworkMode, CasaOS: isCasaOS(in.Config.Labels)}
		d.Ports = portsFromBindings(in.NetworkSettings.Ports)
	}
	st := in.State
	d.State.Status, d.State.Running, d.State.Paused, d.State.Restarting, d.State.OOMKilled = st.Status, st.Running, st.Paused, st.Restarting, st.OOMKilled
	d.State.Pid, d.State.ExitCode, d.State.Error, d.State.RestartCount = st.Pid, st.ExitCode, st.Error, in.RestartCount
	if !strings.HasPrefix(st.StartedAt, "0001") {
		d.State.StartedAt = st.StartedAt
	}
	if !strings.HasPrefix(st.FinishedAt, "0001") {
		d.State.FinishedAt = st.FinishedAt
	}
	if st.Health != nil {
		d.Health = &Health{Status: st.Health.Status, FailingStreak: st.Health.FailingStreak, Log: []HealthResult{}}
		d.Container.Health = st.Health.Status
		logs := st.Health.Log
		if len(logs) > 5 {
			logs = logs[len(logs)-5:]
		}
		for _, l := range logs {
			d.Health.Log = append(d.Health.Log, HealthResult{Start: l.Start, End: l.End, ExitCode: l.ExitCode, Output: redactURLCreds(truncate(strings.TrimSpace(l.Output), 1024))})
		}
		if hc := in.Config.Healthcheck; hc != nil {
			d.Health.Test, d.Health.IntervalNs, d.Health.TimeoutNs, d.Health.Retries = redactAll(hc.Test), hc.Interval, hc.Timeout, hc.Retries
		}
	}
	d.Mounts = []Mount{}
	for _, m := range in.Mounts {
		d.Mounts = append(d.Mounts, Mount{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination, Mode: m.Mode, RW: m.RW})
	}
	sort.Slice(d.Mounts, func(i, j int) bool { return d.Mounts[i].Destination < d.Mounts[j].Destination })
	d.Networks = []Network{}
	for name, n := range in.NetworkSettings.Networks {
		d.Networks = append(d.Networks, Network{Name: name, IP: n.IPAddress, Prefix: n.IPPrefixLen, IPv6: n.GlobalIPv6Address,
			Gateway: n.Gateway, Mac: n.MacAddress, Aliases: n.Aliases, DNSNames: n.DNSNames})
	}
	sort.Slice(d.Networks, func(i, j int) bool { return d.Networks[i].Name < d.Networks[j].Name })
	d.Restart = RestartPolicy{Name: in.HostConfig.RestartPolicy.Name, MaxRetry: in.HostConfig.RestartPolicy.MaximumRetryCount}
	hc := in.HostConfig
	d.Limits = Limits{NanoCPUs: hc.NanoCpus, CPUShares: hc.CPUShares, CpusetCPUs: hc.CpusetCpus, Memory: hc.Memory, MemorySwap: hc.MemorySwap}
	if hc.PidsLimit != nil {
		d.Limits.PidsLimit = *hc.PidsLimit
	}
	d.Security.Privileged, d.Security.CapAdd, d.Security.Devices = hc.Privileged, hc.CapAdd, len(hc.Devices)
	if img != nil {
		d.ImageInf = &ImageInfo{ID: img.ID, Tags: img.RepoTags, Digests: img.RepoDigests, Created: img.Created, Size: img.Size, Architecture: img.Architecture, Os: img.Os}
	}
	return d
}

func (s *Service) handleDetails(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var in inspect
	if err := s.c.call(ctx, http.MethodGet, "/containers/"+id+"/json", nil, &in); err != nil {
		return dockerErr(err)
	}
	var summary *Container
	if l, err := s.List(ctx, false); err == nil {
		if i := findContainer(l, in.ID); i >= 0 {
			c := l[i]
			summary = &c
		}
	}
	var img *apiImage
	var im apiImage
	if s.c.call(ctx, http.MethodGet, "/images/"+in.Image+"/json", nil, &im) == nil {
		img = &im
	}
	httpx.OK(w, buildDetails(&in, summary, img))
	return nil
}

// --- actions ---

func (s *Service) handleAction(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	action := r.PathValue("action")
	switch action {
	case "start", "stop", "restart":
	case "update":
		return s.handleUpdate(w, r, id)
	default:
		return httpx.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if action != "start" && s.isSelf(ctx, id) {
		// Stopping Wargo's own container ends this session (and, for stop,
		// Wargo itself). Require an explicit confirmation.
		var req struct {
			ConfirmSelf bool `json:"confirmSelf"`
		}
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &req); err != nil {
				return err
			}
		}
		if !req.ConfirmSelf {
			msg := map[string]string{
				"stop":    "This is Wargo's own container. Stopping it disconnects you, and Wargo stays down until you start it again from the host.",
				"restart": "This is Wargo's own container. Restarting it disconnects you until it is back.",
			}[action]
			return httpx.Errorf(http.StatusConflict, "self_container", msg).With(map[string]any{"action": action, "self": true})
		}
	}
	name, _ := s.c.ContainerName(ctx, id)
	if name == "" {
		name = id
	}
	err = s.c.call(ctx, http.MethodPost, "/containers/"+id+"/"+action, nil, nil)
	s.audit.Record(r, "containers."+action, name, err, map[string]any{"id": id})
	if err != nil {
		return dockerErr(err)
	}
	s.invalidate()
	go s.publishList(context.Background())
	httpx.OK(w, map[string]any{"ok": true})
	return nil
}

// --- background ---

// Run watches Docker events and polls while someone is subscribed.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.runCtx = ctx
	s.mu.Unlock()
	go s.watchEvents(ctx)
	go s.pollStats(ctx)
	go s.autoCheck(ctx)
	go s.finishSelfUpdate(ctx)
	if s.hub != nil {
		go s.probeLoop(ctx)
	}
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.hub.Subscribers(TopicList) > 0 {
				s.publishList(ctx)
			}
		}
	}
}

func (s *Service) watchEvents(ctx context.Context) {
	backoff := time.Second
	filters := url.QueryEscape(`{"type":["container"]}`)
	for ctx.Err() == nil {
		start := time.Now()
		err := s.followEvents(ctx, "/events?filters="+filters)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		slog.Debug("docker events stream ended", "err", err, "retry", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 60*time.Second)
	}
}

func (s *Service) followEvents(ctx context.Context, path string) error {
	body, err := s.c.stream(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer body.Close()
	evc := make(chan struct{}, 1)
	go func() {
		// Debounce bursts (compose up emits many events).
		var timer <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-evc:
				if !ok {
					return
				}
				if timer == nil {
					timer = time.After(500 * time.Millisecond)
				}
			case <-timer:
				timer = nil
				s.invalidate()
				s.publishList(ctx)
			}
		}
	}()
	defer close(evc)
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var ev struct {
			Action string `json:"Action"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		// exec_* and top events don't change the list.
		if strings.HasPrefix(ev.Action, "exec_") || ev.Action == "top" {
			continue
		}
		select {
		case evc <- struct{}{}:
		default:
		}
	}
	return sc.Err()
}
