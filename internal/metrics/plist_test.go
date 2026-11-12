// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"strings"
	"testing"
)

func TestDecodePlist(t *testing.T) {
	v, err := decodePlist(strings.NewReader(`<?xml version="1.0"?><plist version="1.0"><dict><key>ProductVersion</key><string>27.2</string><key>A</key><array><dict><key>Size</key><integer>994662584320</integer><key>Internal</key><true/></dict></array></dict></plist>`))
	m, _ := v.(map[string]any)
	if err != nil || m["ProductVersion"] != "27.2" {
		t.Fatal(m, err)
	}
	d := m["A"].([]any)[0].(map[string]any)
	if d["Size"] != int64(994662584320) || d["Internal"] != true {
		t.Fatal(d)
	}
}

func TestDecodePlistDepthLimit(t *testing.T) {
	n := 40
	doc := `<plist>` + strings.Repeat("<array>", n) + strings.Repeat("</array>", n) + `</plist>`
	if _, err := decodePlist(strings.NewReader(doc)); err == nil {
		t.Fatal("expected depth error")
	}
	n = 20
	doc = `<plist>` + strings.Repeat("<array>", n) + strings.Repeat("</array>", n) + `</plist>`
	if _, err := decodePlist(strings.NewReader(doc)); err != nil {
		t.Fatal(err)
	}
}
