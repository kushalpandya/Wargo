// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"testing"

	"github.com/kushalpandya/wargo/internal/host"
)

func TestDarwinDriveRootWritable(t *testing.T) {
	ds := drivesFromMounts([]host.Mount{
		{Device: "/dev/disk3s1s1", Path: "/", FSType: "apfs", ReadOnly: true, Local: true, RootFS: true},
		{Device: "/dev/disk3s5", Path: "/System/Volumes/Data", FSType: "apfs", Local: true, DontBrowse: true},
		{Device: "devfs", Path: "/dev", FSType: "devfs", Local: true, DontBrowse: true},
		{Device: "/dev/disk5s1", Path: "/Volumes/USB", FSType: "exfat", Local: true, Removable: true},
		{Device: "//u@nas/share", Path: "/Volumes/share", FSType: "smbfs"},
	})
	if len(ds) != 3 || ds[0].Path != "/" || ds[0].ReadOnly || ds[0].Label != "System" {
		t.Fatalf("%+v", ds)
	}
	if !ds[1].Removable || ds[2].Path != "/Volumes/share" || !ds[2].Network {
		t.Fatalf("%+v", ds)
	}
}

func TestDarwinDriveFilter(t *testing.T) {
	ds := drivesFromMounts([]host.Mount{
		{Device: "map auto_home", Path: "/System/Volumes/Data/home", FSType: "autofs"},
		{Device: "/dev/disk7s1", Path: "/Library/Developer/CoreSimulator/Volumes/iOS", FSType: "apfs", Local: true, ReadOnly: true},
		{Device: "/dev/disk9s1", Path: "/Volumes", FSType: "apfs", Local: true},
		{Device: "/dev/disk8s1", Path: "/Volumes/Backup", FSType: "hfs", Local: true, ReadOnly: true},
		{Device: "/dev/disk8s2", Path: "/Volumes/Hidden", FSType: "apfs", Local: true, DontBrowse: true},
	})
	if len(ds) != 1 || ds[0].Label != "Backup" || !ds[0].ReadOnly || ds[0].Network || ds[0].ID != mountID("/Volumes/Backup") || ds[0].Device != "/dev/disk8s1" || ds[0].FSType != "hfs" {
		t.Fatalf("%+v", ds)
	}
}
