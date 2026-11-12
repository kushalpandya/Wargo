// SPDX-License-Identifier: AGPL-3.0-only

package privd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogTargetRewrittenToVerifiedFD(t *testing.T) {
	c, dir := logFixture(t)
	log := filepath.Join(dir, "1.log")
	// Wargo pre-creates the log; privd must accept it.
	if err := os.WriteFile(log, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	argv, files, err := c.openLogTargets(runArgv(log))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	if len(files) != 1 {
		t.Fatalf("stdout and stderr should share one descriptor, got %d", len(files))
	}
	want := fmt.Sprintf("append:/proc/%d/fd/%d", os.Getpid(), files[0].Fd())
	for _, a := range argv[2:4] {
		if !strings.HasSuffix(a, "="+want) {
			t.Fatalf("not rewritten: %q (want %s)", a, want)
		}
	}
	// The magic link refers to the verified file.
	if _, err := os.Stat(strings.TrimPrefix(want, "append:")); err != nil {
		t.Fatal(err)
	}
}
