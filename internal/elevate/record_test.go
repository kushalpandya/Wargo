// SPDX-License-Identifier: AGPL-3.0-only

package elevate

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

func TestFailedCommandOutputIsAudited(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Service{audit: audit.New(st, httpx.NewProxyTrust(nil))}
	r := httptest.NewRequest("POST", "/api/x", nil)
	failed := errors.New("command failed")
	s.record(r, "sudo", "pkg.refresh", "Refresh package databases", []string{"pacman", "-Sy"}, []byte("error: failed retrieving file\n"), failed)
	// A read that failed half way printed part of the file.
	s.record(r, "sudo", "files.op", "read /etc/shadow", []string{"wargo", "helper", "fs", "read"}, []byte("root:$6$hash:19000::::::\n"), failed)

	outputs := map[string]string{}
	rows, err := st.DB.Query(`SELECT action, COALESCE(output, '') FROM audit_log`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var action, output string
		if err := rows.Scan(&action, &output); err != nil {
			t.Fatal(err)
		}
		outputs[action] = output
	}
	if !strings.Contains(outputs["pkg.refresh"], "failed retrieving file") {
		t.Errorf("command output not kept: %q", outputs["pkg.refresh"])
	}
	if outputs["files.op"] != "" {
		t.Errorf("file content stored: %q", outputs["files.op"])
	}
}
