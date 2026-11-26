// SPDX-License-Identifier: AGPL-3.0-only

package plist

import (
	"runtime"
	"strings"
	"testing"
)

func TestDecodeAnyXML(t *testing.T) {
	v, err := DecodeAny(strings.NewReader(`<?xml version="1.0"?><plist version="1.0"><dict><key>RPCPort</key><integer>9093</integer></dict></plist>`))
	if m, _ := v.(map[string]any); err != nil || m["RPCPort"] != int64(9093) {
		t.Fatalf("DecodeAny = %v, %v", v, err)
	}
}

func TestDecodeAnyBinaryOffMacOS(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS converts binary plists; see the integrations tests")
	}
	if _, err := DecodeAny(strings.NewReader("bplist00\x00\x01")); err == nil {
		t.Fatal("a binary plist must be an error off macOS")
	}
}

func TestDecodeAnyTooLarge(t *testing.T) {
	if _, err := DecodeAny(strings.NewReader(strings.Repeat(" ", plistMaxBytes+1))); err != errPlistTooLarge {
		t.Fatalf("err = %v, want too large", err)
	}
}
