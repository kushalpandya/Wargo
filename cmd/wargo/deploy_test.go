// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
)

func TestLaunchdPlistTemplate(t *testing.T) {
	b, err := os.ReadFile("../../deploy/launchd/io.github.kushalpandya.wargo.plist")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"<string>@BIN@</string>", "<string>serve</string>", "<string>-config</string>", "<string>@CONFIG@</string>",
		"<key>RunAtLoad</key>", "<key>SuccessfulExit</key>", "/opt/homebrew/bin", "en_US.UTF-8", "@LOGDIR@/wargo.log"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(s, "~") {
		t.Error("launchd does not expand ~")
	}
	if err := xml.Unmarshal(b, new(struct{ XMLName xml.Name })); err != nil {
		t.Fatal(err)
	}
}
