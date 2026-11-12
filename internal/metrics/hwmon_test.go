// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, p, v string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadHwmonFiltersBogusSensors(t *testing.T) {
	root := t.TempDir()
	write(t, root+"/hwmon0/name", "coretemp")
	write(t, root+"/hwmon0/temp1_input", "55000")
	write(t, root+"/hwmon0/temp1_label", "Package id 0")
	write(t, root+"/hwmon0/temp1_crit", "100000")
	write(t, root+"/hwmon0/temp2_input", "48000")
	write(t, root+"/hwmon0/temp2_label", "Core 10")
	write(t, root+"/hwmon0/temp3_input", "47000")
	write(t, root+"/hwmon0/temp3_label", "Core 2")
	write(t, root+"/hwmon1/name", "nct6798")
	write(t, root+"/hwmon1/temp1_input", "115000") // disconnected SYSTIN
	write(t, root+"/hwmon1/temp2_input", "-35000")
	write(t, root+"/hwmon1/temp3_input", "0")
	write(t, root+"/hwmon1/fan1_input", "1200")
	write(t, root+"/hwmon1/fan2_input", "0")
	write(t, root+"/hwmon2/name", "nvme")
	write(t, root+"/hwmon2/temp1_input", "39850")
	write(t, root+"/hwmon2/temp1_label", "Composite")

	temps, fans := readHwmon(root)
	if len(temps) != 4 {
		t.Fatalf("want 4 temps, got %d: %+v", len(temps), temps)
	}
	if temps[0].Kind != "cpu" || temps[0].Label != "Package id 0" || temps[0].Crit != 100 {
		t.Fatalf("cpu package should sort first: %+v", temps[0])
	}
	if temps[1].Kind != "disk" {
		t.Fatalf("disk second: %+v", temps[1])
	}
	if temps[2].Label != "Core 2" || temps[3].Label != "Core 10" {
		t.Fatalf("natural sort of cores: %s, %s", temps[2].Label, temps[3].Label)
	}
	if len(fans) != 1 || fans[0].RPM != 1200 {
		t.Fatalf("fans: %+v", fans)
	}
}

func TestNatLess(t *testing.T) {
	cases := [][2]string{{"Core 2", "Core 10"}, {"nvme0n1p2", "nvme0n1p10"}, {"a", "b"}, {"sda1", "sdb"}}
	for _, c := range cases {
		if !natLess(c[0], c[1]) || natLess(c[1], c[0]) {
			t.Errorf("natLess(%q, %q)", c[0], c[1])
		}
	}
}
