// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/jobs"
)

// UpdateCheck is the result of comparing a local image with its registry.
type UpdateCheck struct {
	Image           string `json:"image"`
	UpdateAvailable *bool  `json:"updateAvailable"`
	LocalDigest     string `json:"localDigest,omitempty"`
	RemoteDigest    string `json:"remoteDigest,omitempty"`
	Error           string `json:"error,omitempty"`
	CheckedAt       int64  `json:"checkedAt"`
}

func digestOf(repoDigest string) string {
	if _, d, ok := strings.Cut(repoDigest, "@"); ok {
		return d
	}
	return repoDigest
}

// checkImage compares local RepoDigests with the registry's manifest
// digest via GET /distribution/{ref}/json (read-only; nothing is pulled).
func (s *Service) checkImage(ctx context.Context, ref, imageID string) UpdateCheck {
	u := UpdateCheck{Image: ref, CheckedAt: time.Now().UnixMilli()}
	if strings.HasPrefix(ref, "sha256:") || !validImage(ref) {
		u.Error = "container was created from an image ID, not a tag"
		return u
	}
	if strings.Contains(ref, "@sha256:") {
		f := false
		u.UpdateAvailable, u.Error = &f, "pinned by digest"
		return u
	}
	var img apiImage
	if err := s.c.call(ctx, http.MethodGet, "/images/"+imageID+"/json", nil, &img); err != nil {
		u.Error = err.Error()
		return u
	}
	local := map[string]bool{}
	for _, d := range img.RepoDigests {
		local[digestOf(d)] = true
		if u.LocalDigest == "" {
			u.LocalDigest = digestOf(d)
		}
	}
	if img.Descriptor != nil && img.Descriptor.Digest != "" {
		local[img.Descriptor.Digest] = true
		if u.LocalDigest == "" {
			u.LocalDigest = img.Descriptor.Digest
		}
	}
	if len(local) == 0 {
		u.Error = "image was built locally (no registry digest)"
		return u
	}
	var dist struct {
		Descriptor struct {
			Digest string `json:"digest"`
		} `json:"Descriptor"`
	}
	if err := s.c.call(ctx, http.MethodGet, "/distribution/"+ref+"/json", nil, &dist); err != nil {
		u.Error = registryErr(err)
		return u
	}
	u.RemoteDigest = dist.Descriptor.Digest
	avail := u.RemoteDigest != "" && !local[u.RemoteDigest]
	u.UpdateAvailable = &avail
	return u
}

// --- update ---

// inspected holds both the typed and raw inspect answers.
type inspected struct {
	inspect
	raw struct {
		Config          map[string]any `json:"Config"`
		HostConfig      map[string]any `json:"HostConfig"`
		NetworkSettings struct {
			Networks map[string]map[string]any `json:"Networks"`
		} `json:"NetworkSettings"`
	}
}

func (s *Service) inspectFull(ctx context.Context, id string) (*inspected, error) {
	resp, err := s.c.Do(ctx, http.MethodGet, "/containers/"+id+"/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, readError(resp)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var in inspected
	if err := json.Unmarshal(b, &in.inspect); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &in.raw); err != nil {
		return nil, err
	}
	return &in, nil
}

func composeFiles(labels map[string]string) []string {
	var out []string
	for _, f := range strings.Split(labels[labelConfig], ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func filesReadable(files []string) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return false
		}
		fh.Close()
	}
	return true
}

func (s *Service) handleUpdate(w http.ResponseWriter, r *http.Request, id string) error {
	var req struct {
		Strategy string `json:"strategy"`
	}
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &req); err != nil {
			return err
		}
	}
	if req.Strategy == "" || req.Strategy == "api" {
		req.Strategy = "auto"
	}
	if req.Strategy != "auto" && req.Strategy != "recreate" && req.Strategy != "compose" {
		return httpx.BadRequest("strategy must be auto, recreate or compose")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	in, err := s.inspectFull(ctx, id)
	cancel()
	if err != nil {
		return dockerErr(err)
	}
	name := strings.TrimPrefix(in.Name, "/")
	labels := in.Config.Labels
	project, service, dir := labels[labelProject], labels[labelService], labels[labelWorkingDir]
	files := composeFiles(labels)
	readable := filesReadable(files)

	strategy := req.Strategy
	elevated := false
	if strategy == "auto" {
		strategy = "recreate"
		if project != "" && service != "" && readable && s.composeAvailable() {
			strategy = "compose"
		}
	}
	if strategy == "compose" {
		if project == "" || dir == "" || len(files) == 0 {
			return httpx.BadRequest("container is not part of a compose project")
		}
		if !s.composeAvailable() {
			return httpx.Errorf(http.StatusConflict, "compose_unavailable", "docker compose is not installed on this host")
		}
		if !readable {
			if len(files) != 1 {
				return httpx.BadRequest("elevated compose updates support a single compose file only")
			}
			elevated = true
			if s.elevate == nil {
				return httpx.ErrForbidden
			}
			if r.Header.Get("X-Wargo-Elevation") == "" {
				return s.elevate.ErrRequired("containers.update", fmt.Sprintf("docker compose pull && up -d (project %s)", project))
			}
		}
	}
	if strategy == "recreate" {
		ref := in.Config.Image
		if strings.HasPrefix(ref, "sha256:") || !validImage(ref) {
			return httpx.BadRequest("container was created from an image ID and cannot be updated")
		}
	}

	s.mu.Lock()
	if s.updating[in.ID] {
		s.mu.Unlock()
		return httpx.Errorf(http.StatusConflict, "busy", "An update for this container is already running")
	}
	s.updating[in.ID] = true
	s.mu.Unlock()

	jctx, jcancel := context.WithCancel(context.Background())
	job := s.jobs.Start("containers.update", "Update "+name, jcancel)
	details := map[string]any{"id": in.ID, "strategy": strategy, "image": in.Config.Image, "job": job.ID()}
	// Detach from the request but keep its headers (elevation credential).
	rr := r.Clone(context.WithoutCancel(r.Context()))
	go func() {
		defer jcancel()
		defer func() {
			s.mu.Lock()
			delete(s.updating, in.ID)
			s.list = nil
			s.mu.Unlock()
			s.publishList(context.Background())
		}()
		var result map[string]any
		var err error
		switch {
		case strategy == "compose" && elevated:
			job.Progress("Updating via docker compose (elevated)", -1, "")
			var out []byte
			out, err = s.elevate.Run(rr, "containers.update", fmt.Sprintf("docker compose pull && up -d (project %s)", project),
				s.elevate.Helper("compose-update", dir, project, files[0]), nil)
			if len(out) > 0 {
				job.Log(string(out))
			}
			result = map[string]any{"strategy": "compose"}
		case strategy == "compose":
			err = s.composeUpdate(jctx, job, dir, project, service, files)
			result = map[string]any{"strategy": "compose"}
		default:
			result, err = s.UpdateRecreate(jctx, job, in.ID)
			s.audit.Record(rr, "containers.update", name, err, details)
		}
		if strategy == "compose" && !elevated {
			s.audit.Record(rr, "containers.update", name, err, details)
		}
		job.Finish(err, result)
		if err == nil {
			go func() {
				s.recheckContainer(context.Background(), name)
				s.publishList(context.Background())
			}()
		}
	}()
	httpx.OK(w, map[string]any{"jobId": job.ID(), "strategy": strategy})
	return nil
}

func (s *Service) composeUpdate(ctx context.Context, job *jobs.Job, dir, project, service string, files []string) error {
	base := []string{"compose", "-p", project}
	for _, f := range files {
		base = append(base, "-f", f)
	}
	steps := [][]string{
		append(append([]string{}, base...), "pull", service),
		append(append([]string{}, base...), "up", "-d", service),
	}
	for i, args := range steps {
		job.Progress(fmt.Sprintf("docker %s", strings.Join(args[len(base):], " ")), float64(i)/2, "")
		job.Log("$ docker " + strings.Join(args, " "))
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Dir = dir
		pr, pw := io.Pipe()
		cmd.Stdout, cmd.Stderr = pw, pw
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			sc := bufio.NewScanner(pr)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for sc.Scan() {
				job.Log(sc.Text())
			}
		}()
		err := cmd.Wait()
		pw.Close()
		<-done
		if err != nil {
			return fmt.Errorf("docker %s: %w", strings.Join(args[len(base):], " "), err)
		}
	}
	return nil
}

// pull pulls ref and streams progress into job.
func (s *Service) pull(ctx context.Context, job *jobs.Job, ref string) error {
	body, err := s.c.stream(ctx, http.MethodPost, "/images/create?fromImage="+url.QueryEscape(ref), nil)
	if err != nil {
		return err
	}
	defer body.Close()
	type layer struct{ cur, total int64 }
	layers := map[string]*layer{}
	logged := map[string]string{}
	dec := json.NewDecoder(body)
	for {
		var m struct {
			Status         string                   `json:"status"`
			ID             string                   `json:"id"`
			Error          string                   `json:"error"`
			ErrorDetail    struct{ Message string } `json:"errorDetail"`
			ProgressDetail struct {
				Current int64 `json:"current"`
				Total   int64 `json:"total"`
			} `json:"progressDetail"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return errors.New(m.Error)
		}
		switch m.Status {
		case "Downloading":
			l := layers[m.ID]
			if l == nil {
				l = &layer{}
				layers[m.ID] = l
			}
			l.cur, l.total = m.ProgressDetail.Current, m.ProgressDetail.Total
			var cur, total int64
			for _, l := range layers {
				cur, total = cur+l.cur, total+l.total
			}
			frac := -1.0
			if total > 0 {
				frac = float64(cur) / float64(total)
			}
			job.Progress("Pulling "+ref, frac, fmt.Sprintf("%d layer(s) downloading", len(layers)))
		case "Extracting", "Waiting", "Verifying Checksum":
			// noisy; progress only
		default:
			line := m.Status
			if m.ID != "" {
				line = m.ID + ": " + m.Status
			}
			if m.ID == "" || logged[m.ID] != m.Status {
				logged[m.ID] = m.Status
				job.Log(line)
			}
			if l := layers[m.ID]; l != nil && (m.Status == "Download complete" || m.Status == "Pull complete") {
				l.cur = l.total
			}
		}
	}
}

// UpdateRecreate pulls the container's image and, if it changed, recreates
// the container with identical configuration (Watchtower-style), rolling
// back on failure. Containers sharing its network namespace are recreated
// to point at the new container.
func (s *Service) UpdateRecreate(ctx context.Context, job *jobs.Job, id string) (map[string]any, error) {
	old, err := s.inspectFull(ctx, id)
	if err != nil {
		return nil, err
	}
	ref := old.Config.Image
	job.Progress("Pulling "+ref, -1, "")
	job.Log("Pulling " + ref)
	if err := s.pull(ctx, job, ref); err != nil {
		return nil, fmt.Errorf("pull %s: %w", ref, err)
	}
	var img apiImage
	if err := s.c.call(ctx, http.MethodGet, "/images/"+ref+"/json", nil, &img); err != nil {
		return nil, fmt.Errorf("inspect pulled image: %w", err)
	}
	res := map[string]any{"oldImage": old.Image, "newImage": img.ID}
	if img.ID == old.Image {
		job.Log("Image unchanged: already up to date")
		res["updated"], res["message"] = false, "already up to date"
		s.markUpToDate(ref, img.ID)
		return res, nil
	}
	job.Log(fmt.Sprintf("New image %s (was %s)", shortID(img.ID), shortID(old.Image)))
	job.Progress("Recreating container", -1, strings.TrimPrefix(old.Name, "/"))
	newID, err := s.recreate(ctx, job, old, "")
	if err != nil {
		return nil, err
	}
	res["updated"], res["newId"] = true, newID
	s.markUpToDate(ref, img.ID)

	// Containers using network_mode container:<old> lost their network.
	if l, err := s.List(ctx, true); err == nil {
		name := strings.TrimPrefix(old.Name, "/")
		for _, c := range l {
			m := strings.TrimPrefix(c.NetworkMode, "container:")
			if c.NetworkMode == m || (m != old.ID && m != name && !(len(m) >= 12 && strings.HasPrefix(old.ID, m))) {
				continue
			}
			job.Log(fmt.Sprintf("Recreating %s, which shares %s's network", c.Name, name))
			dep, err := s.inspectFull(ctx, c.FullID)
			if err == nil {
				_, err = s.recreate(ctx, job, dep, "container:"+newID)
			}
			if err != nil {
				job.Log(fmt.Sprintf("warning: could not recreate %s: %v", c.Name, err))
			}
		}
	}
	job.Log("Done")
	return res, nil
}

func sliceEqual(a, b any) bool {
	return reflect.DeepEqual(normalizeJSON(a), normalizeJSON(b))
}

func normalizeJSON(v any) any {
	if v == nil {
		return nil
	}
	if a, ok := v.([]any); ok && len(a) == 0 {
		return nil
	}
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return nil
	}
	return v
}

// stripImageDefaults removes values the container inherited from its old
// image so the new image's defaults apply (e.g. a changed PATH or CMD).
func stripImageDefaults(cfg, imgCfg map[string]any) {
	if imgCfg == nil {
		return
	}
	if env, ok := cfg["Env"].([]any); ok {
		base := map[string]bool{}
		if ie, ok := imgCfg["Env"].([]any); ok {
			for _, e := range ie {
				if s, ok := e.(string); ok {
					base[s] = true
				}
			}
		}
		var keep []any
		for _, e := range env {
			if s, ok := e.(string); ok && base[s] {
				continue
			}
			keep = append(keep, e)
		}
		cfg["Env"] = keep
	}
	for _, k := range []string{"Labels", "ExposedPorts", "Volumes"} {
		m, ok := cfg[k].(map[string]any)
		if !ok {
			continue
		}
		im, _ := imgCfg[k].(map[string]any)
		for kk, v := range m {
			if iv, ok := im[kk]; ok && reflect.DeepEqual(iv, v) {
				delete(m, kk)
			}
		}
	}
	if sliceEqual(cfg["Entrypoint"], imgCfg["Entrypoint"]) {
		delete(cfg, "Entrypoint")
		if sliceEqual(cfg["Cmd"], imgCfg["Cmd"]) {
			delete(cfg, "Cmd")
		}
	}
	for _, k := range []string{"WorkingDir", "User", "Healthcheck", "StopSignal", "Shell", "OnBuild"} {
		if v, ok := cfg[k]; ok && sliceEqual(v, imgCfg[k]) {
			delete(cfg, k)
		}
	}
}

// bindTargets returns destinations covered by HostConfig Binds/Mounts.
func bindTargets(hc map[string]any) map[string]bool {
	out := map[string]bool{}
	if binds, ok := hc["Binds"].([]any); ok {
		for _, b := range binds {
			if s, ok := b.(string); ok {
				parts := strings.Split(s, ":")
				if len(parts) >= 2 {
					out[parts[1]] = true
				}
			}
		}
	}
	if mounts, ok := hc["Mounts"].([]any); ok {
		for _, m := range mounts {
			if mm, ok := m.(map[string]any); ok {
				if t, ok := mm["Target"].(string); ok {
					out[t] = true
				}
			}
		}
	}
	return out
}

// createBody builds the /containers/create payload from an inspect.
func createBody(old *inspected, imgCfg map[string]any, netMode string, multiEndpoint bool) (body map[string]any, extraNets map[string]any) {
	cfg := map[string]any{}
	for k, v := range old.raw.Config {
		cfg[k] = v
	}
	stripImageDefaults(cfg, imgCfg)
	hc := map[string]any{}
	for k, v := range old.raw.HostConfig {
		hc[k] = v
	}
	if netMode != "" {
		hc["NetworkMode"] = netMode
	}
	mode, _ := hc["NetworkMode"].(string)
	if h, _ := cfg["Hostname"].(string); h == shortID(old.ID) || strings.HasPrefix(mode, "container:") || mode == "host" {
		delete(cfg, "Hostname")
		if strings.HasPrefix(mode, "container:") || mode == "host" {
			delete(cfg, "Domainname")
		}
	}
	// Keep anonymous volumes: mount them explicitly so data carries over.
	covered := bindTargets(hc)
	var mounts []any
	if m, ok := hc["Mounts"].([]any); ok {
		mounts = m
	}
	for _, m := range old.Mounts {
		if m.Type == "volume" && m.Name != "" && !covered[m.Destination] {
			mounts = append(mounts, map[string]any{"Type": "volume", "Source": m.Name, "Target": m.Destination, "ReadOnly": !m.RW})
		}
	}
	if len(mounts) > 0 {
		hc["Mounts"] = mounts
	}
	body = cfg
	body["HostConfig"] = hc

	if mode == "host" || mode == "none" || strings.HasPrefix(mode, "container:") {
		return body, nil
	}
	endpoints := map[string]any{}
	extraNets = map[string]any{}
	for name, n := range old.raw.NetworkSettings.Networks {
		ep := map[string]any{}
		for _, k := range []string{"IPAMConfig", "Links", "DriverOpts"} {
			if v, ok := n[k]; ok && v != nil {
				ep[k] = v
			}
		}
		if al, ok := n["Aliases"].([]any); ok {
			var keep []any
			for _, a := range al {
				if s, ok := a.(string); ok && (s == shortID(old.ID) || s == old.ID) {
					continue
				}
				keep = append(keep, a)
			}
			if len(keep) > 0 {
				ep["Aliases"] = keep
			}
		}
		primary := name == mode || (mode == "default" && name == "bridge")
		if multiEndpoint || primary {
			endpoints[name] = ep
		} else {
			extraNets[name] = ep
		}
	}
	body["NetworkingConfig"] = map[string]any{"EndpointsConfig": endpoints}
	return body, extraNets
}

func (s *Service) recreate(ctx context.Context, job *jobs.Job, old *inspected, netMode string) (string, error) {
	name := strings.TrimPrefix(old.Name, "/")
	tmpName := name + "-wargo-old"
	wasRunning := old.State.Running || old.State.Restarting

	var imgCfg map[string]any
	var oldImg struct {
		Config map[string]any `json:"Config"`
	}
	if err := s.c.call(ctx, http.MethodGet, "/images/"+old.Image+"/json", nil, &oldImg); err == nil {
		imgCfg = oldImg.Config
	}
	multi := !versionLess(s.c.APIVersion(), "1.44")
	body, extra := createBody(old, imgCfg, netMode, multi)

	// Bounded context for rollback even if the job is canceled.
	bg := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	}
	q := url.QueryEscape
	if err := s.c.call(ctx, http.MethodPost, "/containers/"+old.ID+"/rename?name="+q(tmpName), nil, nil); err != nil {
		return "", fmt.Errorf("rename old container: %w", err)
	}
	job.Log(fmt.Sprintf("Renamed %s to %s", name, tmpName))
	newID := ""
	rollback := func(cause error) error {
		job.Log("Error: " + cause.Error() + " — rolling back")
		rctx, cancel := bg()
		defer cancel()
		if newID != "" {
			if err := s.c.call(rctx, http.MethodDelete, "/containers/"+newID+"?force=1", nil, nil); err != nil {
				job.Log("rollback: remove new container: " + err.Error())
			}
		}
		if err := s.c.call(rctx, http.MethodPost, "/containers/"+old.ID+"/rename?name="+q(name), nil, nil); err != nil {
			job.Log("rollback: rename: " + err.Error())
		}
		if wasRunning {
			if err := s.c.call(rctx, http.MethodPost, "/containers/"+old.ID+"/start", nil, nil); err != nil {
				job.Log("rollback: start old container: " + err.Error())
			}
		}
		job.Log("Rolled back to the previous container")
		return fmt.Errorf("update failed and was rolled back: %w", cause)
	}

	if wasRunning {
		t := 10
		if old.Config.StopTimeout != nil {
			t = *old.Config.StopTimeout
		}
		job.Log("Stopping old container")
		if err := s.c.call(ctx, http.MethodPost, fmt.Sprintf("/containers/%s/stop?t=%d", old.ID, t), nil, nil); err != nil {
			return "", rollback(fmt.Errorf("stop: %w", err))
		}
	}
	var created struct {
		ID       string   `json:"Id"`
		Warnings []string `json:"Warnings"`
	}
	if err := s.c.call(ctx, http.MethodPost, "/containers/create?name="+q(name), body, &created); err != nil {
		return "", rollback(fmt.Errorf("create: %w", err))
	}
	newID = created.ID
	for _, w := range created.Warnings {
		job.Log("warning: " + w)
	}
	job.Log("Created " + shortID(newID))
	for netName, ep := range extra {
		if err := s.c.call(ctx, http.MethodPost, "/networks/"+url.PathEscape(netName)+"/connect",
			map[string]any{"Container": newID, "EndpointConfig": ep}, nil); err != nil {
			return "", rollback(fmt.Errorf("connect network %s: %w", netName, err))
		}
	}
	if wasRunning {
		if err := s.c.call(ctx, http.MethodPost, "/containers/"+newID+"/start", nil, nil); err != nil {
			return "", rollback(fmt.Errorf("start: %w", err))
		}
		job.Log("Started new container, checking it stays up")
		select {
		case <-ctx.Done():
			return "", rollback(ctx.Err())
		case <-time.After(s.settle):
		}
		var st struct {
			State inspectState `json:"State"`
		}
		if err := s.c.call(ctx, http.MethodGet, "/containers/"+newID+"/json", nil, &st); err != nil {
			return "", rollback(fmt.Errorf("inspect new container: %w", err))
		}
		if !st.State.Running || st.State.Restarting {
			return "", rollback(fmt.Errorf("new container is not running (state %s, exit code %d)", st.State.Status, st.State.ExitCode))
		}
	}
	rctx, cancel := bg()
	defer cancel()
	if err := s.c.call(rctx, http.MethodDelete, "/containers/"+old.ID+"?force=1", nil, nil); err != nil {
		job.Log("warning: could not remove old container " + tmpName + ": " + err.Error())
	} else {
		job.Log("Removed old container")
	}
	return newID, nil
}
