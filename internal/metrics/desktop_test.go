// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import "testing"

func TestVersionExtraction(t *testing.T) {
	for in, want := range map[string]string{
		"plasma-workspace 6.7.5-1":          "6.7.5",
		"4:6.2.4-0ubuntu1":                  "6.2.4",
		"47.1":                              "47.1",
		"sway version 1.10":                 "1.10",
		"Hyprland 0.45.2 built from branch": "0.45.2",
	} {
		if got := versionRe.FindString(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestDesktopTableComplete(t *testing.T) {
	for _, d := range desktops {
		if d.name == "" || len(d.procs) == 0 || len(d.packages) == 0 {
			t.Errorf("incomplete entry %+v", d)
		}
	}
}
