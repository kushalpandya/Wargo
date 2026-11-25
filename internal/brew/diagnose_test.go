// SPDX-License-Identifier: AGPL-3.0-only

package brew

import (
	"strings"
	"testing"
)

func TestDiagnose(t *testing.T) {
	for log, code := range map[string]string{
		"==> Pouring node--23.1.0.arm64_sequoia.bottle.tar.gz\nError: The `brew link` step did not complete successfully\nThe formula built, but is not symlinked into /opt/homebrew\nCould not symlink bin/npm\nTarget /opt/homebrew/bin/npm\nalready exists. You may want to remove it:": CodeLink,
		"Error: Cannot install mysql because conflicting formulae are installed.\n  mariadb: because both install the same binaries":                                                                                                                                                       CodeConflict,
		"Error: A `brew upgrade` process has already locked /opt/homebrew/Cellar/git.\nPlease wait for it to finish or terminate it to continue.":                                                                                                                                          CodeLocked,
		"curl: (6) Could not resolve host: ghcr.io\nError: Failed to download resource \"git\"\nDownload failed: https://ghcr.io/v2/homebrew/core/git/blobs/sha256:0":                                                                                                                      CodeNetwork,
		"Error: No space left on device @ rb_sysopen - /opt/homebrew/Cellar/x":                                                                                                                                                                                                             CodeNoSpace,
		"==> Upgrading 1 outdated package:\nError: something unexpected":                                                                                                                                                                                                                   CodeFailed,
	} {
		d := diagnose(strings.Split(log, "\n"))
		if d == nil || d.Code != code || d.Title == "" || len(d.Lines) == 0 {
			t.Errorf("%q: got %+v, want %s", log, d, code)
		}
	}
	if d := diagnose([]string{"==> Upgrading git", "==> Pouring git--2.47.bottle.tar.gz", "🍺  /opt/homebrew/Cellar/git/2.47: 1,600 files"}); d != nil {
		t.Errorf("a good update was diagnosed: %+v", d)
	}
}
