// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"os"
	"strings"

	hostfs "github.com/kushalpandya/wargo/internal/host"
)

var armVendors = map[string]string{
	"0x41": "Arm", "0x42": "Broadcom", "0x48": "HiSilicon",
	"0x51": "Qualcomm", "0x61": "Apple", "0xc0": "Ampere",
}

var armParts = map[string]map[string]string{
	"0x41": {
		"0xd03": "Cortex-A53", "0xd04": "Cortex-A35", "0xd05": "Cortex-A55",
		"0xd07": "Cortex-A57", "0xd08": "Cortex-A72", "0xd09": "Cortex-A73",
		"0xd0a": "Cortex-A75", "0xd0b": "Cortex-A76", "0xd0c": "Neoverse-N1",
		"0xd0d": "Cortex-A77", "0xd40": "Neoverse-V1", "0xd41": "Cortex-A78",
		"0xd44": "Cortex-X1", "0xd46": "Cortex-A510", "0xd47": "Cortex-A710",
		"0xd48": "Cortex-X2", "0xd49": "Neoverse-N2", "0xd4f": "Neoverse-V2",
		"0xd80": "Cortex-A520", "0xd81": "Cortex-A720", "0xd82": "Cortex-X4",
	},
	"0xc0": {"0xac3": "Ampere-1", "0xac4": "Ampere-1a"},
	"0x48": {"0xd01": "Kunpeng-920"},
}

// armCPUModel names an ARM CPU from /proc/cpuinfo, which has no "model name"
// line there. It returns "" when nothing is recognised.
func armCPUModel(cpuinfo string) string {
	var impl, part string
	for _, line := range strings.Split(cpuinfo, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.ToLower(strings.TrimSpace(v))
		switch k {
		case "CPU implementer":
			if impl == "" {
				impl = v
			}
		case "CPU part":
			if part == "" {
				part = v
			}
		}
	}
	vendor, ok := armVendors[impl]
	if !ok {
		return ""
	}
	// Normalise zero padding, e.g. 0x0d08 -> 0xd08.
	if p := strings.TrimLeft(strings.TrimPrefix(part, "0x"), "0"); p != "" {
		if name, ok := armParts[impl]["0x"+p]; ok {
			return vendor + " " + name
		}
	}
	if name, ok := armParts[impl][part]; ok {
		return vendor + " " + name
	}
	return vendor + " CPU"
}

// fallbackCPUModel is used when gopsutil reports no model name.
func fallbackCPUModel() string {
	b, err := os.ReadFile(hostfs.Path("/proc/cpuinfo"))
	if err == nil {
		if m := armCPUModel(string(b)); m != "" {
			return m
		}
	}
	if b, err := os.ReadFile(hostfs.Path("/sys/firmware/devicetree/base/model")); err == nil {
		return strings.TrimSpace(strings.TrimRight(string(b), "\x00"))
	}
	return ""
}
