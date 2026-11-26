// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kushalpandya/wargo/internal/docker"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/pkg"
)

// Action tools send the requests the web app sends for the same buttons.
// Everyday actions (level operate) run at once; if a handler answers that
// it needs admin rights, the same request waits for the owner instead.
// Admin actions always wait for the owner: their handler is reached only
// through an approved request (Approvals.Create), and the summary the
// owner sees is built from the stored request itself.

func (s *Service) addActTools() {
	addTool(s, containerActionSpec, s.containerAction)
	addTool(s, torrentAddSpec, s.torrentAdd)
	addTool(s, torrentActionSpec, s.torrentAction)
	addTool(s, checkUpdatesSpec, s.checkUpdates)
	addTool(s, processSignalSpec, s.processSignal)
	addTool(s, applyOSUpdatesSpec, s.applyOSUpdates)
	addTool(s, upgradeHomebrewSpec, s.upgradeHomebrew)
	addTool(s, updateContainerSpec, s.updateContainer)
}

// askOwner records an everyday tool's request that a handler refused for
// want of admin rights, unchanged, as a request waiting for the owner.
func (s *Service) askOwner(ctx context.Context, spec ToolSpec, args any, method, path string, body any, summary string) (approvalRef, error) {
	p, err := s.approvals.Create(ctx, spec, args, method, path, body, summary)
	if err != nil {
		return approvalRef{}, err
	}
	return refOf(p, s.approvalURL(p.ID)), nil
}

// checksFor is the read-only part of admin tool spec: what it looks at
// before it asks the owner, through call (which refuses admin specs).
func checksFor(spec ToolSpec, patterns ...string) ToolSpec {
	c := spec
	c.Admin, c.ReadOnly, c.Routes = false, true, nil
	for _, p := range patterns {
		c.Routes = append(c.Routes, route{http.MethodGet, p})
	}
	return c
}

// --- container_action ---

var containerActionSpec = ToolSpec{Name: "container_action",
	Description: "Start, stop or restart a Docker container on this host. Wargo's own container cannot be stopped or restarted this way.",
	Level:       LevelOperate, Destructive: true, Needs: "docker",
	Routes: []route{{http.MethodPost, "POST /api/containers/{id}/{action}"}}}

type containerActionIn struct {
	ID     string `json:"id" jsonschema:"container name or id, from list_containers"`
	Action string `json:"action" jsonschema:"start, stop or restart"`
}

type containerActionOut struct {
	Container string `json:"container"`
	Action    string `json:"action"`
	OK        bool   `json:"ok"`
}

func (s *Service) containerAction(ctx context.Context, _ *sdk.CallToolRequest, in containerActionIn) (*sdk.CallToolResult, containerActionOut, error) {
	out := containerActionOut{Container: in.ID, Action: in.Action}
	if err := containerRef(in.ID); err != nil {
		return nil, out, err
	}
	switch in.Action {
	case "start", "stop", "restart":
	default:
		return nil, out, invalidArg("action must be start, stop or restart")
	}
	// No body: the handler's confirmation for Wargo's own container is
	// never given.
	if _, err := s.send(ctx, containerActionSpec, http.MethodPost, "/api/containers/"+url.PathEscape(in.ID)+"/"+in.Action, nil); err != nil {
		return nil, out, err
	}
	out.OK = true
	return nil, out, nil
}

// --- torrent_add ---

var torrentAddSpec = ToolSpec{Name: "torrent_add",
	Description: "Download a torrent: give a magnet link, or a resultId from torrent_search. Exactly one of the two.",
	Level:       LevelOperate,
	Routes: []route{{http.MethodPost, "POST /api/transmission/torrents"},
		{http.MethodPost, "POST /api/prowlarr/results/{id}/queue"}}}

// maxMagnet caps a magnet link, in bytes.
const maxMagnet = 8 << 10

type torrentAddIn struct {
	Magnet   string `json:"magnet,omitempty" jsonschema:"a magnet link, starting with magnet:?"`
	ResultID string `json:"resultId,omitempty" jsonschema:"a resultId from torrent_search"`
}

type torrentAddOut struct {
	// For a magnet link: the torrent in Transmission.
	ID        int64  `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"`
	// For a search result: who it was sent to ("prowlarr" or "transmission").
	Via string `json:"via,omitempty"`
}

func (s *Service) torrentAdd(ctx context.Context, _ *sdk.CallToolRequest, in torrentAddIn) (*sdk.CallToolResult, torrentAddOut, error) {
	var out torrentAddOut
	switch {
	case in.Magnet != "" && in.ResultID == "":
		if !strings.HasPrefix(in.Magnet, "magnet:?") || len(in.Magnet) > maxMagnet {
			return nil, out, invalidArg(fmt.Sprintf("magnet must start with magnet:? and be at most %d bytes", maxMagnet))
		}
		if !s.capable("transmission") {
			return nil, out, unavailable("Transmission")
		}
		body, err := s.send(ctx, torrentAddSpec, http.MethodPost, "/api/transmission/torrents", map[string]string{"magnet": in.Magnet})
		if err != nil {
			return nil, out, err
		}
		var added struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			Duplicate bool   `json:"duplicate"`
		}
		if err := json.Unmarshal(body, &added); err != nil {
			return nil, out, fmt.Errorf("mcp: torrent_add: decoding answer: %w", err)
		}
		out.ID, out.Name, out.Duplicate = added.ID, untrusted(added.Name), added.Duplicate
	case in.ResultID != "" && in.Magnet == "":
		if !idRe.MatchString(in.ResultID) {
			return nil, out, invalidArg("resultId must be a resultId from torrent_search")
		}
		if !s.capable("prowlarr") {
			return nil, out, unavailable("Prowlarr")
		}
		body, err := s.send(ctx, torrentAddSpec, http.MethodPost, "/api/prowlarr/results/"+url.PathEscape(in.ResultID)+"/queue", nil)
		if err != nil {
			return nil, out, err
		}
		var queued struct {
			Via string `json:"via"`
		}
		if err := json.Unmarshal(body, &queued); err != nil {
			return nil, out, fmt.Errorf("mcp: torrent_add: decoding answer: %w", err)
		}
		out.Via = queued.Via
	default:
		return nil, out, invalidArg("give exactly one of magnet or resultId")
	}
	return nil, out, nil
}

// --- torrent_action ---

var torrentActionSpec = ToolSpec{Name: "torrent_action",
	Description: "Start, stop or remove a torrent in Transmission. Removing keeps the downloaded files unless deleteData is true, " +
		"which also deletes them and cannot be undone: set it only when the user asked for the files to go, and give the info hash as id.",
	Level: LevelOperate, Destructive: true, Idempotent: true, Needs: "transmission",
	Routes: []route{{http.MethodPost, "POST /api/transmission/torrents/{id}/start"},
		{http.MethodPost, "POST /api/transmission/torrents/{id}/stop"},
		{http.MethodDelete, "DELETE /api/transmission/torrents/{id}"}}}

// torrentIDRe accepts Transmission's ids and info hashes.
var torrentIDRe = regexp.MustCompile(`^([1-9][0-9]{0,17}|[0-9a-fA-F]{40})$`)

type torrentActionIn struct {
	ID     string `json:"id" jsonschema:"the torrent's id or info hash, from torrents"`
	Action string `json:"action" jsonschema:"start, stop or remove"`
	// DeleteData is the web app's "Remove and delete data".
	DeleteData bool `json:"deleteData,omitempty" jsonschema:"with remove, also delete the downloaded files (cannot be undone); needs the info hash as id"`
}

// hashLen is an info hash's length. Deleting files takes the hash, not the
// numeric id: Transmission renumbers torrents when it restarts, so an id an
// agent kept from earlier could now name another torrent.
const hashLen = 40

type torrentActionOut struct {
	ID         string `json:"id"`
	Action     string `json:"action"`
	DeleteData bool   `json:"deleteData,omitempty"`
	OK         bool   `json:"ok"`
}

func (s *Service) torrentAction(ctx context.Context, _ *sdk.CallToolRequest, in torrentActionIn) (*sdk.CallToolResult, torrentActionOut, error) {
	out := torrentActionOut{ID: in.ID, Action: in.Action, DeleteData: in.DeleteData}
	if !torrentIDRe.MatchString(in.ID) {
		return nil, out, invalidArg("id must be a torrent id or info hash")
	}
	if in.DeleteData && in.Action != "remove" {
		return nil, out, invalidArg("deleteData goes only with remove")
	}
	if in.DeleteData && len(in.ID) != hashLen {
		return nil, out, invalidArg("deleteData needs the torrent's info hash as id, from torrents")
	}
	method, target := http.MethodPost, "/api/transmission/torrents/"+in.ID+"/"+in.Action
	switch in.Action {
	case "start", "stop":
	case "remove":
		method, target = http.MethodDelete, "/api/transmission/torrents/"+in.ID+"?deleteData="+strconv.FormatBool(in.DeleteData)
	default:
		return nil, out, invalidArg("action must be start, stop or remove")
	}
	if _, err := s.send(ctx, torrentActionSpec, method, target, nil); err != nil {
		return nil, out, err
	}
	out.OK = true
	return nil, out, nil
}

// --- check_updates ---

var checkUpdatesSpec = ToolSpec{Name: "check_updates",
	Description: "Check for updates now: OS packages, Homebrew and Docker container images. Each part reports on its own; " +
		"a part that needs admin rights waits for the owner's approval (follow it with approval_status).",
	Level: LevelOperate, Idempotent: true,
	Routes: []route{{http.MethodPost, "POST /api/pkg/check"}, {http.MethodPost, "POST /api/brew/check"},
		{http.MethodPost, "POST /api/containers/check-updates"}}}

// The states of a check_updates part.
const (
	partChecked     = "checked"
	partWaiting     = "waiting_for_approval"
	partUnavailable = "unavailable"
	partFailed      = "failed"
)

type checkPart struct {
	State   string `json:"state" jsonschema:"checked, waiting_for_approval, unavailable or failed"`
	Updates *int   `json:"updates,omitempty"`
	Error   string `json:"error,omitempty"`
	// Approval is the request waiting for the owner.
	Approval *approvalRef `json:"approval,omitempty"`
}

type checkUpdatesOut struct {
	OS         checkPart `json:"os"`
	Homebrew   checkPart `json:"homebrew"`
	Containers checkPart `json:"containers"`
}

func (s *Service) checkUpdates(ctx context.Context, _ *sdk.CallToolRequest, in none) (*sdk.CallToolResult, checkUpdatesOut, error) {
	var out checkUpdatesOut
	var wg sync.WaitGroup
	wg.Go(func() {
		out.OS = s.checkPart(ctx, in, "/api/pkg/check", "Refresh package databases and check for OS updates", true, countPackages)
	})
	wg.Go(func() {
		out.Homebrew = s.checkPart(ctx, in, "/api/brew/check", "Check for Homebrew updates", true, countPackages)
	})
	wg.Go(func() {
		out.Containers = s.checkPart(ctx, in, "/api/containers/check-updates", "Check for container image updates", s.capable("docker"), countImages)
	})
	wg.Wait()
	return nil, out, nil
}

// checkPart runs one check (when available): its update count, its
// request waiting for the owner, or why it did not run.
func (s *Service) checkPart(ctx context.Context, in none, target, summary string, available bool, count func(json.RawMessage) (int, error)) checkPart {
	spec := checkUpdatesSpec
	if !available || !s.routed(spec, http.MethodPost, target) {
		return checkPart{State: partUnavailable}
	}
	body, err := s.send(ctx, spec, http.MethodPost, target, nil)
	if isElevationRequired(err) {
		ref, err := s.askOwner(ctx, spec, in, http.MethodPost, target, nil, summary)
		if err != nil {
			return failedPart(err)
		}
		return checkPart{State: partWaiting, Approval: &ref}
	}
	var e *httpx.Error
	if errors.As(err, &e) && e.Status == http.StatusServiceUnavailable {
		return checkPart{State: partUnavailable, Error: untrusted(e.Message)}
	}
	if err != nil {
		return failedPart(err)
	}
	n, err := count(body)
	if err != nil {
		return failedPart(fmt.Errorf("mcp: check_updates: decoding %s: %w", target, err))
	}
	return checkPart{State: partChecked, Updates: &n}
}

func failedPart(err error) checkPart {
	e := apiError(checkUpdatesSpec, err)
	return checkPart{State: partFailed, Error: untrusted(e.Code + ": " + e.Message)}
}

func countPackages(b json.RawMessage) (int, error) {
	var c pkg.CheckResult
	err := json.Unmarshal(b, &c)
	return len(c.Updates), err
}

func countImages(b json.RawMessage) (int, error) {
	var u docker.UpdatesStatus
	err := json.Unmarshal(b, &u)
	n := 0
	for _, r := range u.Results {
		if r.UpdateAvailable != nil && *r.UpdateAvailable {
			n++
		}
	}
	return n, err
}

// --- process_signal ---

var processSignalSpec = ToolSpec{Name: "process_signal",
	Description: "Ask a process on this host to stop (SIGTERM). A process Wargo may not signal itself waits for the owner's approval.",
	Level:       LevelOperate, Destructive: true, Idempotent: true,
	Routes: []route{{http.MethodGet, "GET /api/procs/{pid}"}, {http.MethodPost, "POST /api/procs/{pid}/signal"}}}

// maxPID is above any process id Linux or macOS hands out.
const maxPID = 1 << 22

type processSignalIn struct {
	PID int `json:"pid" jsonschema:"the process id, from list_processes"`
}

type processSignalOut struct {
	PID    int    `json:"pid"`
	Signal string `json:"signal"`
	Sent   bool   `json:"sent"`
	// Approval is the request waiting for the owner, when Wargo needs
	// admin rights to signal the process.
	Approval *approvalRef `json:"approval,omitempty"`
	Message  string       `json:"message,omitempty"`
}

func (s *Service) processSignal(ctx context.Context, req *sdk.CallToolRequest, in processSignalIn) (*sdk.CallToolResult, processSignalOut, error) {
	out := processSignalOut{PID: in.PID, Signal: "TERM"}
	if in.PID <= 1 || in.PID > maxPID {
		return nil, out, invalidArg("pid must be a process id greater than 1")
	}
	// The process as it is now: its start time goes into the request, so
	// a request that waits for approval cannot signal a process that took
	// the pid over meanwhile (the handler refuses it as process_changed).
	var proc struct {
		Name       string `json:"name"`
		User       string `json:"user"`
		StartTicks uint64 `json:"startTicks"`
	}
	if err := s.get(ctx, processSignalSpec, "/api/procs/"+strconv.Itoa(in.PID), &proc); err != nil {
		return nil, out, err
	}
	path := "/api/procs/" + strconv.Itoa(in.PID) + "/signal"
	body := json.RawMessage(fmt.Sprintf(`{"signal":"TERM","startTicks":%d}`, proc.StartTicks))
	_, err := s.send(ctx, processSignalSpec, http.MethodPost, path, body)
	if isElevationRequired(err) {
		po, err := s.waitForOwner(ctx, req, processSignalSpec, in, http.MethodPost, path, body,
			fmt.Sprintf("Send TERM to process %d (%s, user %s)", in.PID, untrusted(proc.Name), untrusted(proc.User)))
		if err != nil {
			return nil, out, err
		}
		out.Approval, out.Message = &po.Approval, po.Message
		return nil, out, nil
	}
	if err != nil {
		return nil, out, err
	}
	out.Sent = true
	return nil, out, nil
}

// --- apply_os_updates ---

var applyOSUpdatesSpec = ToolSpec{Name: "apply_os_updates",
	Description: "Install the pending OS package updates. Waits for the owner's approval in Wargo. If something blocks the upgrade " +
		"(the package manager is busy, disk space is low), it reports that and asks for nothing; it never removes conflicting packages.",
	Level: LevelAdmin, Admin: true, Destructive: true, Needs: "pkg",
	Routes: []route{{http.MethodPost, "POST /api/pkg/upgrade"}}}

var applyOSUpdatesChecks = checksFor(applyOSUpdatesSpec, "GET /api/pkg/precheck")

func (s *Service) applyOSUpdates(ctx context.Context, req *sdk.CallToolRequest, in none) (*sdk.CallToolResult, pendingOut, error) {
	var pc struct {
		Lock    pkg.LockStatus `json:"lock"`
		Blocked string         `json:"blocked"`
	}
	if err := s.get(ctx, applyOSUpdatesChecks, "/api/pkg/precheck", &pc); err != nil {
		return nil, pendingOut{}, err
	}
	if pc.Blocked != "" || pc.Lock.Locked {
		blockers := map[string]any{"blocked": untrusted(pc.Blocked)}
		if pc.Lock.Locked {
			blockers["lock"] = map[string]string{"reason": untrusted(pc.Lock.Reason), "path": untrusted(pc.Lock.Path)}
		}
		return nil, pendingOut{}, httpx.Errorf(http.StatusConflict, "upgrade_blocked",
			"The upgrade cannot start now, so nothing was requested. "+untrusted(pc.Blocked)).With(blockers)
	}
	// No body: the upgrade never asks to remove conflicting packages.
	out, err := s.waitForOwner(ctx, req, applyOSUpdatesSpec, in, http.MethodPost, "/api/pkg/upgrade", nil, "Install OS updates")
	return nil, out, err
}

// --- upgrade_homebrew ---

var upgradeHomebrewSpec = ToolSpec{Name: "upgrade_homebrew",
	Description: "Install the pending Homebrew updates. Waits for the owner's approval in Wargo.",
	Level:       LevelAdmin, Admin: true, Destructive: true, Needs: "brew",
	Routes: []route{{http.MethodPost, "POST /api/brew/upgrade"}}}

func (s *Service) upgradeHomebrew(ctx context.Context, req *sdk.CallToolRequest, in none) (*sdk.CallToolResult, pendingOut, error) {
	out, err := s.waitForOwner(ctx, req, upgradeHomebrewSpec, in, http.MethodPost, "/api/brew/upgrade", nil, "Install Homebrew updates")
	return nil, out, err
}

// --- update_container ---

var updateContainerSpec = ToolSpec{Name: "update_container",
	Description: "Update a Docker container to its latest image (pull and recreate). Waits for the owner's approval in Wargo. " +
		"Wargo's own container is not updated this way.",
	Level: LevelAdmin, Admin: true, Destructive: true, Needs: "docker",
	Routes: []route{{http.MethodPost, "POST /api/containers/{id}/{action}"}}}

var updateContainerChecks = checksFor(updateContainerSpec, "GET /api/containers/{id}", "GET /api/containers")

var fullIDRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// updateBody is what the web app sends to update a container.
var updateBody = json.RawMessage(`{"strategy":"auto"}`)

type updateContainerIn struct {
	ID string `json:"id" jsonschema:"container name or id, from list_containers"`
}

func (s *Service) updateContainer(ctx context.Context, req *sdk.CallToolRequest, in updateContainerIn) (*sdk.CallToolResult, pendingOut, error) {
	if err := containerRef(in.ID); err != nil {
		return nil, pendingOut{}, err
	}
	var d docker.Details
	if err := s.get(ctx, updateContainerChecks, "/api/containers/"+url.PathEscape(in.ID), &d); err != nil {
		return nil, pendingOut{}, err
	}
	if !fullIDRe.MatchString(d.FullID) {
		return nil, pendingOut{}, fmt.Errorf("mcp: update_container: unexpected container id %q", d.FullID)
	}
	// Wargo's own container, as the containers list marks it (the
	// docker feature's own check).
	var list []docker.Container
	if err := s.get(ctx, updateContainerChecks, "/api/containers", &list); err != nil {
		return nil, pendingOut{}, err
	}
	self := d.Self
	for _, c := range list {
		self = self || (c.Self && c.FullID == d.FullID)
	}
	if self {
		return nil, pendingOut{}, httpx.Errorf(http.StatusConflict, "self_container",
			"This is Wargo's own container. Updating it restarts Wargo, so the owner does that in Wargo.")
	}
	// The full id, so the approved request updates the container shown
	// to the owner even if a name moves meanwhile.
	out, err := s.waitForOwner(ctx, req, updateContainerSpec, in, http.MethodPost, "/api/containers/"+d.FullID+"/update", updateBody,
		"Update container "+untrusted(d.Name)+" ("+d.FullID[:12]+")")
	return nil, out, err
}
