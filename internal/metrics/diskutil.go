// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"slices"
	"strings"
)

// Parsers for diskutil's plist output, untagged so Linux tests cover them.

func plDict(v any) map[string]any { m, _ := v.(map[string]any); return m }
func plArray(v any) []any         { a, _ := v.([]any); return a }
func plString(v any) string       { s, _ := v.(string); return s }
func plBool(v any) bool           { b, _ := v.(bool); return b }

func plSize(v any) uint64 {
	if n, ok := v.(int64); ok && n > 0 {
		return uint64(n)
	}
	return 0
}

func addMount(ms []string, m string) []string {
	if m == "" || slices.Contains(ms, m) {
		return ms
	}
	return append(ms, m)
}

// transportOf maps diskutil's BusProtocol to the transports the UI knows.
func transportOf(protocol string) string {
	switch protocol {
	case "Apple Fabric", "PCI-Express", "NVMe":
		return "nvme"
	case "USB":
		return "usb"
	case "SATA":
		return "sata"
	}
	return "other"
}

// parseDiskutil builds drives from `diskutil list -plist physical` (the
// whole disks and their partitions), `diskutil list -plist` (APFS
// containers and their volumes) and `diskutil info -plist <disk>` per
// whole disk. An APFS partition gets the mounts of the volumes in the
// container whose physical store it is.
func parseDiskutil(list, physical any, info map[string]any) []Drive {
	storeMounts := map[string][]string{}
	storeLabel := map[string]string{}
	for _, c := range plArray(plDict(list)["AllDisksAndPartitions"]) {
		cm := plDict(c)
		var mounts []string
		label := ""
		for _, v := range plArray(cm["APFSVolumes"]) {
			vm := plDict(v)
			mounts = addMount(mounts, plString(vm["MountPoint"]))
			for _, s := range plArray(vm["MountedSnapshots"]) {
				mounts = addMount(mounts, plString(plDict(s)["SnapshotMountPoint"]))
			}
			if label == "" && !plBool(vm["OSInternal"]) {
				label = plString(vm["VolumeName"])
			}
		}
		for _, s := range plArray(cm["APFSPhysicalStores"]) {
			id := plString(plDict(s)["DeviceIdentifier"])
			for _, m := range mounts {
				storeMounts[id] = addMount(storeMounts[id], m)
			}
			if storeLabel[id] == "" {
				storeLabel[id] = label
			}
		}
	}

	pm := plDict(physical)
	whole := map[string]bool{}
	for _, w := range plArray(pm["WholeDisks"]) {
		whole[plString(w)] = true
	}
	out := []Drive{}
	for _, e := range plArray(pm["AllDisksAndPartitions"]) {
		em := plDict(e)
		name := plString(em["DeviceIdentifier"])
		if name == "" || !whole[name] {
			continue
		}
		in := plDict(info[name])
		d := Drive{Name: name, Size: plSize(em["Size"]), Partitions: []Partition{}}
		if d.Size == 0 {
			d.Size = plSize(in["Size"])
		}
		d.Model = strings.TrimSpace(plString(in["MediaName"]))
		d.Transport = transportOf(plString(in["BusProtocol"]))
		if ss, ok := in["SolidState"].(bool); ok {
			d.Rotational = !ss
		}
		d.Removable = plBool(in["RemovableMediaOrExternalDevice"]) || plBool(in["Removable"])
		for _, p := range plArray(em["Partitions"]) {
			ppm := plDict(p)
			pn := plString(ppm["DeviceIdentifier"])
			if pn == "" {
				continue
			}
			part := Partition{Name: pn, Size: plSize(ppm["Size"]), Label: plString(ppm["VolumeName"])}
			part.Mounts = addMount(nil, plString(ppm["MountPoint"]))
			for _, m := range storeMounts[pn] {
				part.Mounts = addMount(part.Mounts, m)
			}
			if part.Label == "" {
				part.Label = storeLabel[pn]
			}
			d.Partitions = append(d.Partitions, part)
		}
		slices.SortFunc(d.Partitions, func(a, b Partition) int { return natCmp(a.Name, b.Name) })
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Drive) int { return natCmp(a.Name, b.Name) })
	return out
}

func natCmp(a, b string) int {
	switch {
	case a == b:
		return 0
	case natLess(a, b):
		return -1
	}
	return 1
}
