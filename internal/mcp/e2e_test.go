// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"slices"
	"testing"

	"github.com/kushalpandya/wargo/internal/audit"
)

// TestEndToEnd follows one agent from connecting to a finished admin action:
// it lists tools, reads, asks for an update, waits, the owner approves in the
// browser, the agent sees the outcome, and the audit log holds the chain.
func TestEndToEnd(t *testing.T) {
	h := newActHarness(t, "docker")
	addTool(h.s, containersSpec, h.s.listContainers)
	updates := h.fakeContainers()
	cs := h.client(t, h.token, false)

	names := toolNames(t, cs)
	for _, want := range []string{"list_containers", "approval_status", "container_action", "update_container"} {
		if !slices.Contains(names, want) {
			t.Fatalf("tools/list lacks %s: %v", want, names)
		}
	}

	var list containersOut
	toolOK(t, cs, "list_containers", nil, &list)
	if len(list.Containers) != 2 {
		t.Fatalf("containers %+v", list.Containers)
	}

	var out pendingOut
	toolOK(t, cs, "update_container", map[string]any{"id": "plex"}, &out)
	if out.Approval.State != StatePending || out.Approval.ID == "" {
		t.Fatalf("pending %+v", out)
	}
	if updates.count() != 0 {
		t.Fatal("the update ran before approval")
	}
	var st approvalStatusOut
	toolOK(t, cs, "approval_status", map[string]any{"id": out.Approval.ID}, &st)
	if st.State != StatePending {
		t.Fatalf("status before approval %+v", st)
	}

	if w := h.approve(t, out.Approval.ID, nil); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	if updates.count() != 1 {
		t.Fatalf("update ran %d times", updates.count())
	}
	toolOK(t, cs, "approval_status", map[string]any{"id": out.Approval.ID}, &st)
	if st.State != StateApproved || st.JobID != "j1" {
		t.Fatalf("status after approval %+v", st)
	}

	// The update itself (agent) and approved (owner) link to the
	// requested entry (agent).
	p, _ := h.s.approvals.Get(out.Approval.ID)
	var requested, source string
	if err := h.s.st.DB.QueryRow(`SELECT action, source FROM audit_log WHERE id = ?`, p.Ref).Scan(&requested, &source); err != nil {
		t.Fatal(err)
	}
	if requested != "mcp.approval.requested" || source != audit.SourceAgent {
		t.Fatalf("request entry %s from %s", requested, source)
	}
	rows, err := h.s.st.DB.Query(`SELECT action, source FROM audit_log WHERE ref = ? ORDER BY id`, p.Ref)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var linked []string
	for rows.Next() {
		var action, source string
		if err := rows.Scan(&action, &source); err != nil {
			t.Fatal(err)
		}
		linked = append(linked, action+" "+source)
	}
	if !slices.Equal(linked, []string{"containers.update agent", "mcp.approval.approved user"}) {
		t.Fatalf("entries linked to the request: %v", linked)
	}
}
