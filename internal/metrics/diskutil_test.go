// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func loadPlist(t *testing.T, name string) any {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := decodePlist(f)
	if err != nil {
		t.Fatal(name, err)
	}
	return v
}

func TestParseDiskutil(t *testing.T) {
	list := loadPlist(t, "diskutil-list.plist")
	physical := loadPlist(t, "diskutil-list-physical.plist")
	info := loadPlist(t, "diskutil-info-disk0.plist")
	ds := parseDiskutil(list, physical, map[string]any{"disk0": info})
	if len(ds) != 1 || ds[0].Name != "disk0" || ds[0].Transport != "nvme" || ds[0].Size == 0 {
		t.Fatalf("%+v", ds)
	}
	if !slices.ContainsFunc(ds[0].Partitions, func(p Partition) bool { return slices.Contains(p.Mounts, "/") }) {
		t.Fatal("no / mount")
	}
	d := ds[0]
	if d.Model != "APPLE SSD AP1024Z" || d.Rotational || d.Removable || len(d.Partitions) != 3 {
		t.Fatalf("%+v", d)
	}
	p := d.Partitions[1]
	if p.Name != "disk0s2" || p.Size != 994662584320 || p.Label != "Macintosh HD" {
		t.Fatalf("%+v", p)
	}
	// "/" is both the sealed snapshot and the volume's snapshot mount: listed once.
	n := 0
	for _, m := range p.Mounts {
		if m == "/" {
			n++
		}
	}
	if n != 1 || !slices.Contains(p.Mounts, "/System/Volumes/Data") {
		t.Fatalf("mounts %q", p.Mounts)
	}
	// Simulator disk images are not physical drives.
	if slices.ContainsFunc(ds, func(d Drive) bool { return d.Name != "disk0" }) {
		t.Fatalf("%+v", ds)
	}
}

const usbPhysical = `<plist><dict>
<key>WholeDisks</key><array><string>disk4</string></array>
<key>AllDisksAndPartitions</key><array><dict>
<key>DeviceIdentifier</key><string>disk4</string><key>Size</key><integer>15376000000</integer>
<key>MountPoint</key><string>/Volumes/STICK</string><key>VolumeName</key><string>STICK</string>
</dict></array></dict></plist>`

const usbInfo = `<plist><dict><key>BusProtocol</key><string>USB</string><key>MediaName</key><string> SanDisk Ultra </string>
<key>RemovableMediaOrExternalDevice</key><true/><key>SolidState</key><false/></dict></plist>`

// A drive with a filesystem directly on it has no partitions: the list is
// empty, never null, because the System page maps over it.
func TestParseDiskutilNoPartitions(t *testing.T) {
	phys, err := decodePlist(strings.NewReader(usbPhysical))
	if err != nil {
		t.Fatal(err)
	}
	info, err := decodePlist(strings.NewReader(usbInfo))
	if err != nil {
		t.Fatal(err)
	}
	ds := parseDiskutil(nil, phys, map[string]any{"disk4": info})
	if len(ds) != 1 {
		t.Fatalf("%+v", ds)
	}
	d := ds[0]
	if d.Transport != "usb" || !d.Removable || !d.Rotational || d.Model != "SanDisk Ultra" || d.Size != 15376000000 {
		t.Fatalf("%+v", d)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"partitions":[]`) {
		t.Fatalf("drive JSON %s, want \"partitions\":[]", b)
	}
	// Garbage input yields no drives rather than a panic.
	if ds := parseDiskutil("x", []any{1}, nil); len(ds) != 0 {
		t.Fatalf("%+v", ds)
	}
}

func TestTransportOf(t *testing.T) {
	for in, want := range map[string]string{
		"Apple Fabric": "nvme", "PCI-Express": "nvme", "NVMe": "nvme", "USB": "usb", "SATA": "sata",
		"Thunderbolt": "other", "Disk Image": "other", "": "other",
	} {
		if got := transportOf(in); got != want {
			t.Errorf("transportOf(%q) = %q, want %q", in, got, want)
		}
	}
}
