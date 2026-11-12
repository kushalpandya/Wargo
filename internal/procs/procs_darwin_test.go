// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package procs

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/libproc"
)

func TestScanDarwin(t *testing.T) {
	s := New(nil, nil, nil, nil)
	first := s.scan()
	var self, init Proc
	for _, p := range first.Procs {
		if p.PID == os.Getpid() {
			self = p
		}
		if p.PID == 1 {
			init = p
		}
	}
	if self.MemRSS == 0 || !strings.Contains(self.Command, "procs.test") || self.UID != os.Getuid() {
		t.Fatalf("self %+v", self)
	}
	if init.Name != "launchd" || init.User != "root" {
		t.Fatalf("pid 1 %+v", init)
	}
	for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); {
	} // burn CPU
	for _, p := range s.scan().Procs {
		if p.PID == os.Getpid() && p.CPU <= 0 {
			t.Fatalf("cpu %.1f", p.CPU)
		}
	}
}

func TestVerifyTargetDarwin(t *testing.T) {
	s := New(nil, nil, nil, nil)
	st, _ := libproc.StartTime(os.Getpid())
	if err := s.verifyTarget(Proc{PID: os.Getpid(), startTicks: st}); err != nil {
		t.Fatal(err)
	}
	if err := s.verifyTarget(Proc{PID: os.Getpid(), startTicks: st + 1}); !errors.Is(err, syscall.ESRCH) {
		t.Fatal(err)
	}
}

func TestDetailsDarwin(t *testing.T) {
	d, err := New(nil, nil, nil, nil).Details(context.Background(), os.Getpid())
	if err != nil || !d.Readable || d.FDCount == nil || *d.FDCount == 0 || len(d.Args) == 0 || d.Cwd == "" {
		t.Fatalf("%+v %v", d, err)
	}
}
