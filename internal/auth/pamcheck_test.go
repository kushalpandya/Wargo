// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPAMServiceProblem(t *testing.T) {
	d := t.TempDir()
	if msg := PAMServiceProblem(d, "wargo"); !strings.Contains(msg, filepath.Join(d, "wargo")) {
		t.Fatal(msg)
	}
	if err := os.WriteFile(filepath.Join(d, "wargo"), []byte("auth required pam_opendirectory.so\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := PAMServiceProblem(d, "wargo"); msg != "" {
		t.Fatal(msg)
	}
}
