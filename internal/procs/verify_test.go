// SPDX-License-Identifier: AGPL-3.0-only

package procs

import (
	"errors"
	"syscall"
	"testing"
)

// verifyTarget rejects a pid whose process changed since the sample (pid
// reuse) so the signal can't hit an unrelated process.
func TestVerifyTargetDetectsPidReuse(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, "10", "10 (worker) R 1 0 0 0 0 0 0 0 0 0 100 0 0 0 20 0 3 0 200 4096 25 0", "worker\x00")
	s := New(nil, nil, nil, nil)
	s.root = root
	if err := s.verifyTarget(Proc{PID: 10, startTicks: 200}); err != nil {
		t.Fatalf("same process rejected: %v", err)
	}
	if err := s.verifyTarget(Proc{PID: 10, startTicks: 199}); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("reused pid accepted: %v", err)
	}
	if err := s.verifyTarget(Proc{PID: 11, startTicks: 200}); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("missing pid accepted: %v", err)
	}
}
