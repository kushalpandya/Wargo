// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux || darwin

package term

import (
	"syscall"
	"testing"
	"unsafe"
)

func TestSetSizeRoundTrip(t *testing.T) {
	m, s, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	defer s.Close()
	if err := setSize(m, 120, 40); err != nil {
		t.Fatal(err)
	}
	var ws winsize
	if err := ioctl(s, syscall.TIOCGWINSZ, unsafe.Pointer(&ws)); err != nil {
		t.Fatal(err)
	}
	if ws.Cols != 120 || ws.Rows != 40 {
		t.Fatalf("got %dx%d", ws.Cols, ws.Rows)
	}
}
