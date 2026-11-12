// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import "testing"

func TestArmCPUModel(t *testing.T) {
	arm := func(impl, part string) string {
		return "processor\t: 0\nBogoMIPS\t: 50.00\nCPU implementer\t: " + impl + "\nCPU architecture: 8\nCPU part\t: " + part + "\n"
	}
	cases := []struct{ name, in, want string }{
		{"a72", arm("0x41", "0xd08"), "Arm Cortex-A72"},
		{"a76", arm("0x41", "0xd0b"), "Arm Cortex-A76"},
		{"apple", arm("0x61", "0x000"), "Apple CPU"},
		{"ampere", arm("0xc0", "0xac3"), "Ampere Ampere-1"},
		{"unknown vendor", arm("0x99", "0x001"), ""},
		{"x86", "processor\t: 0\nmodel name\t: Intel(R) Xeon(R)\n", ""},
	}
	for _, c := range cases {
		if got := armCPUModel(c.in); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
