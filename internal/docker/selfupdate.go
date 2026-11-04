// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/jobs"
)

// Self update: Wargo can't recreate its own container from inside it (the
// recreate would stop the process doing it). Instead it pulls the new
// image and starts a short-lived helper container from it that runs
// `wargo self-update`, which recreates Wargo's container with the same
// steps as any other container and rolls back if the new one doesn't
// become healthy. Whichever Wargo runs afterwards (new on success, old
// after a rollback) collects the helper's result.

const (
	keySelfPending = "docker.selfUpdate"
	keySelfResult  = "docker.selfUpdate.result"

	selfHelperName    = "wargo-self-update"
	labelSelfUpdate   = "io.wargo.self-update"
	labelSelfTarget   = "io.wargo.self-update.target"
	labelSelfJob      = "io.wargo.self-update.job"
	helperSocket      = "/var/run/docker.sock"
	selfLogLines      = 200
	selfStartDeadline = time.Minute
)

// selfUpdateDelay lets the HTTP answer and job state reach the browser
// before the helper stops Wargo; selfSettle is the helper's settle time
// (tests shorten both).
var (
	selfUpdateDelay = 2 * time.Second
	selfSettle      = 3 * time.Second
)

type selfPending struct {
	JobID       string `json:"jobId"`
	StartedAt   int64  `json:"startedAt"` // unix ms
	Target      string `json:"target"`
	Name        string `json:"name"`
	Ref         string `json:"ref"`
	FromImage   string `json:"fromImage"`
	ToImage     string `json:"toImage"`
	FromVersion string `json:"fromVersion"`
	ToVersion   string `json:"toVersion,omitempty"` // from the new image's labels
	HelperID    string `json:"helperId"`
}

type selfResult struct {
	OK          bool     `json:"ok"`
	RolledBack  bool     `json:"rolledBack"`
	Error       string   `json:"error,omitempty"`
	FromVersion string   `json:"fromVersion"`
	ToVersion   string   `json:"toVersion,omitempty"`
	StartedAt   int64    `json:"startedAt"`
	FinishedAt  int64    `json:"finishedAt"` // unix ms
	ExitCode    *int     `json:"exitCode,omitempty"`
	Log         []string `json:"log"`
	JobID       string   `json:"jobId"`
}

// hostSocketPath maps the socket path Wargo uses inside its container to
// the host path through the container's bind mounts ("" if not mounted).
func hostSocketPath(mounts []inspectMount, sock string) string {
	best := -1
	for i, m := range mounts {
		if m.Type != "bind" || m.Destination == "" {
			continue
		}
		d := strings.TrimSuffix(m.Destination, "/")
		if sock != d && !strings.HasPrefix(sock, d+"/") {
			continue
		}
		if best < 0 || len(d) > len(strings.TrimSuffix(mounts[best].Destination, "/")) {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	m := mounts[best]
	return path.Join(m.Source, strings.TrimPrefix(sock, strings.TrimSuffix(m.Destination, "/")))
}

// selfUpdateCheck reports whether in (Wargo's own container) can update
// itself, and the host path of the Docker socket for the helper.
func (s *Service) selfUpdateCheck(in *inspected) (string, error) {
	if s.selfID == "" || in.ID != s.selfID {
		return "", errSelfUpdate("it is not running in its own container")
	}
	if s.st == nil {
		return "", errSelfUpdate("it has no database to keep track of the update")
	}
	ref := in.Config.Image
	if strings.HasPrefix(ref, "sha256:") || !validImage(ref) {
		return "", errSelfUpdate("its container was created from an image ID, not a name like ghcr.io/kushalpandya/wargo:latest")
	}
	if strings.Contains(ref, "@sha256:") {
		return "", errSelfUpdate("its image is pinned by digest")
	}
	sock := s.c.Socket()
	if strings.Contains(sock, "://") {
		return "", errSelfUpdate("it reaches Docker over the network, not a mounted socket")
	}
	hs := hostSocketPath(in.Mounts, sock)
	if hs == "" {
		return "", errSelfUpdate("it can't tell where the Docker socket " + sock + " comes from on the host (mount it with -v /var/run/docker.sock:/var/run/docker.sock)")
	}
	return hs, nil
}

// selfUpdatePossible is the "selfUpdate" capability.
func (s *Service) selfUpdatePossible(ctx context.Context) bool {
	if s.selfID == "" {
		return false
	}
	in, err := s.inspectFull(ctx, s.selfID)
	if err != nil {
		return false
	}
	_, err = s.selfUpdateCheck(in)
	return err == nil
}

func (s *Service) handleSelfUpdate(w http.ResponseWriter, r *http.Request, in *inspected) error {
	hostSock, err := s.selfUpdateCheck(in)
	if err != nil {
		return err
	}
	var p selfPending
	if ok, _ := s.st.Get(r.Context(), keySelfPending, &p); ok {
		return httpx.Errorf(http.StatusConflict, "busy", "A Wargo update is already in progress")
	}
	s.mu.Lock()
	if s.updating[in.ID] {
		s.mu.Unlock()
		return httpx.Errorf(http.StatusConflict, "busy", "A Wargo update is already in progress")
	}
	s.updating[in.ID] = true
	s.mu.Unlock()

	name := strings.TrimPrefix(in.Name, "/")
	jctx, jcancel := context.WithTimeout(context.Background(), 30*time.Minute)
	job := s.jobs.Start("containers.update", "Update Wargo", jcancel)
	rr := r.Clone(context.WithoutCancel(r.Context()))
	go func() {
		defer jcancel()
		defer func() {
			s.mu.Lock()
			delete(s.updating, in.ID)
			s.mu.Unlock()
		}()
		res, err := s.startSelfUpdate(jctx, job, in, name, hostSock)
		if err != nil && s.audit != nil {
			s.audit.Record(rr, "containers.update.self.start", name, err, map[string]any{"id": in.ID, "image": in.Config.Image, "job": job.ID()})
		}
		if err == nil && res["handedOver"] == true && s.audit != nil {
			s.audit.Record(rr, "containers.update.self.start", name, nil, map[string]any{
				"id": in.ID, "image": in.Config.Image, "job": job.ID(), "helper": res["helperId"], "newImage": res["newImage"],
			})
		}
		job.Finish(err, res)
		if res["handedOver"] == true {
			// Usually the helper stops this process. If it fails before
			// that, this process records the result.
			s.mu.Lock()
			rctx := s.runCtx
			s.mu.Unlock()
			if rctx != nil {
				s.finishSelfUpdate(rctx)
			}
		}
	}()
	httpx.OK(w, map[string]any{"jobId": job.ID(), "strategy": "self"})
	return nil
}

// startSelfUpdate pulls the new image and, if it changed, hands over to
// the helper container.
func (s *Service) startSelfUpdate(ctx context.Context, job *jobs.Job, in *inspected, name, hostSock string) (map[string]any, error) {
	ref := in.Config.Image
	job.Progress("Pulling "+ref, -1, "")
	job.Log("Pulling " + ref)
	if err := s.pull(ctx, job, ref); err != nil {
		return nil, fmt.Errorf("pull %s: %w", ref, err)
	}
	var img struct {
		ID     string `json:"Id"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := s.c.call(ctx, http.MethodGet, "/images/"+ref+"/json", nil, &img); err != nil {
		return nil, fmt.Errorf("inspect pulled image: %w", err)
	}
	res := map[string]any{"self": true, "oldImage": in.Image, "newImage": img.ID}
	if img.ID == in.Image {
		job.Log("Image unchanged: already up to date")
		res["updated"], res["message"] = false, "already up to date"
		s.markUpToDate(ref, img.ID)
		return res, nil
	}
	job.Log(fmt.Sprintf("New image %s (was %s)", shortID(img.ID), shortID(in.Image)))
	job.Progress("Starting the updater", -1, "")
	s.removeOldHelpers(ctx, job)

	q := url.QueryEscape
	body := map[string]any{
		"Image": img.ID,
		"Cmd":   []string{"self-update", "--target", in.ID, "--socket", helperSocket},
		"Labels": map[string]string{
			labelSelfUpdate: "1",
			labelSelfTarget: in.ID,
			labelSelfJob:    job.ID(),
		},
		// The image's health check probes the web service, which the
		// helper doesn't run.
		"Healthcheck": map[string]any{"Test": []string{"NONE"}},
		"HostConfig": map[string]any{
			"Binds":         []string{hostSock + ":" + helperSocket},
			"NetworkMode":   "none",
			"AutoRemove":    false,
			"RestartPolicy": map[string]any{"Name": "no"},
		},
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := s.c.call(ctx, http.MethodPost, "/containers/create?name="+q(selfHelperName), body, &created); err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			return nil, fmt.Errorf("a container named %s already exists (another update may be running): %w", selfHelperName, err)
		}
		return nil, fmt.Errorf("create the updater: %w", err)
	}
	job.Log("Created the updater " + shortID(created.ID))
	removeHelper := func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = s.c.call(rctx, http.MethodDelete, "/containers/"+created.ID+"?force=1&v=1", nil, nil)
	}
	_, tag := splitRef(ref)
	p := selfPending{
		JobID: job.ID(), StartedAt: time.Now().UnixMilli(), Target: in.ID, Name: name, Ref: ref,
		FromImage: in.Image, ToImage: img.ID, FromVersion: s.version,
		ToVersion: imageVersion(img.Config.Labels, tag, img.ID), HelperID: created.ID,
	}
	// Saved before the helper starts: it may stop Wargo at any moment after.
	if err := s.st.Set(ctx, keySelfPending, p); err != nil {
		removeHelper()
		return nil, fmt.Errorf("save update state: %w", err)
	}
	if err := s.c.call(ctx, http.MethodPost, "/containers/"+created.ID+"/start", nil, nil); err != nil {
		removeHelper()
		_ = s.st.Delete(context.WithoutCancel(ctx), keySelfPending)
		return nil, fmt.Errorf("start the updater: %w", err)
	}
	job.Log("Handed over to the updater; Wargo restarts now")
	res["handedOver"], res["helperId"] = true, created.ID
	return res, nil
}

// removeOldHelpers deletes finished helper containers left from earlier
// updates.
func (s *Service) removeOldHelpers(ctx context.Context, log logger) {
	f, _ := json.Marshal(map[string][]string{"label": {labelSelfUpdate + "=1"}})
	var l []apiContainer
	if err := s.c.call(ctx, http.MethodGet, "/containers/json?all=1&filters="+url.QueryEscape(string(f)), nil, &l); err != nil {
		return
	}
	for _, c := range l {
		if c.Labels[labelSelfUpdate] != "1" || c.State == "running" || c.State == "restarting" {
			continue
		}
		if err := s.c.call(ctx, http.MethodDelete, "/containers/"+c.ID+"?force=1&v=1", nil, nil); err == nil {
			log.Log("Removed an old updater container " + shortID(c.ID))
		}
	}
}

func (s *Service) handleSelfUpdateStatus(w http.ResponseWriter, r *http.Request) error {
	out := map[string]any{}
	if s.st != nil {
		var p selfPending
		if ok, _ := s.st.Get(r.Context(), keySelfPending, &p); ok {
			out["pending"] = map[string]any{"startedAt": p.StartedAt, "fromVersion": p.FromVersion, "toVersion": p.ToVersion, "ref": p.Ref, "jobId": p.JobID}
		}
		var res selfResult
		if ok, _ := s.st.Get(r.Context(), keySelfResult, &res); ok {
			out["result"] = res
		}
	}
	httpx.OK(w, out)
	return nil
}

// --- finishing (runs in whichever Wargo starts after the helper) ---

// finishSelfUpdate waits for a pending update's helper to exit, then
// records its outcome and removes it.
func (s *Service) finishSelfUpdate(ctx context.Context) {
	if s.st == nil {
		return
	}
	s.mu.Lock()
	if s.selfFinishing {
		s.mu.Unlock()
		return
	}
	s.selfFinishing = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.selfFinishing = false
		s.mu.Unlock()
	}()
	var p selfPending
	if ok, err := s.st.Get(ctx, keySelfPending, &p); err != nil || !ok {
		return
	}
	poll, wait := s.selfPoll, s.selfWait
	if poll <= 0 {
		poll = 2 * time.Second
	}
	if wait <= 0 {
		wait = 10 * time.Minute
	}
	deadline := time.Now().Add(wait)
	res := selfResult{FromVersion: p.FromVersion, ToVersion: p.ToVersion, StartedAt: p.StartedAt, JobID: p.JobID, Log: []string{}}
	for {
		var in inspect
		err := s.c.call(ctx, http.MethodGet, "/containers/"+p.HelperID+"/json", nil, &in)
		if ctx.Err() != nil {
			return
		}
		switch {
		case IsNotFound(err):
			// Without the helper, judge by the image Wargo runs now.
			if s.selfImage(ctx) == p.ToImage {
				res.OK, res.ToVersion = true, s.version
			} else {
				res.Error = "The updater disappeared before it reported a result."
			}
			s.recordSelfResult(ctx, p, res)
			return
		case err == nil && in.State.Status == "created" && time.Since(time.UnixMilli(p.StartedAt)) > selfStartDeadline:
			res.Error = "The updater never started."
			s.recordSelfResult(ctx, p, res)
			return
		case err == nil && !in.State.Running && !in.State.Restarting && in.State.Status != "created":
			res.Log = s.helperLog(ctx, p.HelperID, in.Config.Tty)
			code := in.State.ExitCode
			res.ExitCode = &code
			switch code {
			case 0:
				res.OK, res.ToVersion = true, s.version
			case 1:
				res.RolledBack = true
				res.Error = "The update did not complete, so the previous version is running again."
			case 2:
				res.Error = "The update failed and the previous version could not be restored completely. Check the containers on the host."
			default:
				res.Error = fmt.Sprintf("The updater stopped with exit code %d.", code)
			}
			s.recordSelfResult(ctx, p, res)
			return
		}
		if time.Now().After(deadline) {
			if err == nil {
				res.Log = s.helperLog(ctx, p.HelperID, in.Config.Tty)
			}
			res.Error = "The updater did not finish in time."
			s.recordSelfResult(ctx, p, res)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

func (s *Service) selfImage(ctx context.Context) string {
	if s.selfID == "" {
		return ""
	}
	var v struct {
		Image string `json:"Image"`
	}
	if s.c.call(ctx, http.MethodGet, "/containers/"+s.selfID+"/json", nil, &v) != nil {
		return ""
	}
	return v.Image
}

// helperLog returns the last lines of the helper's output.
func (s *Service) helperLog(ctx context.Context, id string, tty bool) []string {
	lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {fmt.Sprint(selfLogLines)}}
	body, err := s.c.stream(lctx, http.MethodGet, "/containers/"+id+"/logs?"+q.Encode(), nil)
	if err != nil {
		return []string{}
	}
	defer body.Close()
	lr := NewLineReader(body, tty, false)
	out := []string{}
	for {
		l, err := lr.Next()
		if err != nil {
			break
		}
		out = append(out, l.Text)
	}
	if len(out) > selfLogLines {
		out = out[len(out)-selfLogLines:]
	}
	return out
}

func (s *Service) recordSelfResult(ctx context.Context, p selfPending, res selfResult) {
	res.FinishedAt = time.Now().UnixMilli()
	if err := s.st.Set(ctx, keySelfResult, res); err != nil {
		slog.Error("self update: save result", "err", err)
		return
	}
	result := "ok"
	if !res.OK {
		result = "error: " + res.Error
	}
	if s.audit != nil {
		s.audit.RecordSystemDetails("containers.update", p.Name, result, map[string]any{
			"id": p.Target, "strategy": "self", "image": p.Ref, "job": p.JobID, "fromVersion": res.FromVersion,
			"toVersion": res.ToVersion, "rolledBack": res.RolledBack, "exitCode": res.ExitCode,
		})
	}
	slog.Info("self update finished", "ok", res.OK, "rolledBack", res.RolledBack, "from", res.FromVersion, "to", res.ToVersion)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.c.call(rctx, http.MethodDelete, "/containers/"+p.HelperID+"?force=1&v=1", nil, nil); err != nil && !IsNotFound(err) {
		slog.Warn("self update: remove updater", "err", err)
	}
	if err := s.st.Delete(rctx, keySelfPending); err != nil {
		slog.Error("self update: clear state", "err", err)
	}
	if res.OK {
		s.markUpToDate(p.Ref, p.ToImage)
	}
	s.invalidate()
}

// --- the helper (`wargo self-update`) ---

type lineLog struct{ w io.Writer }

func (l lineLog) Log(s string) { fmt.Fprintln(l.w, s) }

// SelfUpdate recreates Wargo's container target from its (already
// pulled) image, like any other container update, but waits for the new
// container's health check before removing the old one. It runs inside the
// helper container and logs plain lines to w. Errors wrap ErrRolledBack or
// ErrRollbackFailed when the recreate itself failed.
func SelfUpdate(ctx context.Context, socket, target string, w io.Writer, timeout time.Duration) error {
	log := lineLog{w}
	err := selfUpdate(ctx, socket, target, log, timeout)
	if err != nil {
		log.Log("Update failed: " + err.Error())
	}
	return err
}

func selfUpdate(ctx context.Context, socket, target string, log logger, timeout time.Duration) error {
	if !validRef(target) {
		return errors.New("invalid target container")
	}
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s := &Service{c: NewClient(socket), settle: selfSettle, healthPoll: time.Second}
	log.Log("Wargo updater started")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(selfUpdateDelay):
	}
	old, err := s.inspectFull(ctx, target)
	if err != nil {
		return fmt.Errorf("inspect Wargo's container: %w", err)
	}
	if !old.State.Running {
		return fmt.Errorf("Wargo's container is not running (state %s)", old.State.Status)
	}
	ref := old.Config.Image
	if strings.HasPrefix(ref, "sha256:") || !validImage(ref) {
		return errors.New("Wargo's container was created from an image ID")
	}
	var img struct {
		ID string `json:"Id"`
	}
	if err := s.c.call(ctx, http.MethodGet, "/images/"+ref+"/json", nil, &img); err != nil {
		return fmt.Errorf("inspect image %s: %w", ref, err)
	}
	if img.ID == old.Image {
		log.Log("Image unchanged: already up to date")
		return nil
	}
	name := strings.TrimPrefix(old.Name, "/")
	log.Log(fmt.Sprintf("Updating %s to %s (was %s)", name, shortID(img.ID), shortID(old.Image)))
	newID, err := s.recreate(ctx, log, old, "", true)
	if err != nil {
		return err
	}
	log.Log("Updated " + name + ", new container " + shortID(newID))
	return nil
}
