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

func TestDecodePlistMalformed(t *testing.T) {
	deep := `<plist>` + strings.Repeat("<dict><key>k</key>", 40) + "<true/>" + strings.Repeat("</dict>", 40) + `</plist>`
	for name, doc := range map[string]string{
		"entity":      `<?xml version="1.0"?><!DOCTYPE plist [<!ENTITY x "boom">]><plist><string>&x;</string></plist>`,
		"truncated":   `<plist><dict><key>Size</key><integer>12`,
		"no plist":    `<dict><key>a</key><true/></dict>`,
		"integer":     `<plist><integer>12abc</integer></plist>`,
		"dict no key": `<plist><dict><string>a</string><true/></dict></plist>`,
		"deep dict":   deep,
		"unknown":     `<plist><bogus/></plist>`,
	} {
		if v, err := decodePlist(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: decoded %v, want error", name, v)
		}
	}
}

func TestDecodePlistSizeLimit(t *testing.T) {
	big := `<plist><string>` + strings.Repeat("a", plistMaxBytes) + `</string></plist>`
	if _, err := decodePlist(strings.NewReader(big)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want too large", err)
	}
	ok := `<plist><string>` + strings.Repeat("a", 1<<20) + `</string></plist>`
	if _, err := decodePlist(strings.NewReader(ok)); err != nil {
		t.Fatal(err)
	}
}
