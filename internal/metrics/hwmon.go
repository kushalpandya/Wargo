// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Temp struct {
	Chip  string  `json:"chip"`
	Label string  `json:"label"`
	Value float64 `json:"value"`
	High  float64 `json:"high,omitempty"`
	Crit  float64 `json:"crit,omitempty"`
	Kind  string  `json:"kind"` // cpu | core | disk | gpu | board | wifi | other
}

type Fan struct {
	Chip  string `json:"chip"`
	Label string `json:"label"`
	RPM   int    `json:"rpm"`
}

func readInt(path string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return v, err == nil
}

func readStr(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

func chipKind(chip, label string) string {
	l := strings.ToLower(label)
	switch chip {
	case "coretemp":
		if strings.HasPrefix(l, "package") {
			return "cpu"
		}
		return "core"
	case "k10temp", "zenpower":
		if l == "tctl" || l == "tdie" || l == "" {
			return "cpu"
		}
		return "core"
	case "cpu_thermal", "soc_thermal", "x86_pkg_temp":
		return "cpu"
	case "nvme", "drivetemp":
		return "disk"
	case "amdgpu", "nouveau", "radeon", "i915", "xe":
		return "gpu"
	case "acpitz", "pch_cannonlake", "pch_skylake":
		return "board"
	}
	if strings.HasPrefix(chip, "nct") || strings.HasPrefix(chip, "it87") || strings.HasPrefix(chip, "asus") {
		if strings.Contains(l, "cpu") || strings.Contains(l, "peci") {
			return "cpu"
		}
		return "board"
	}
	if strings.HasPrefix(chip, "mt79") || strings.HasPrefix(chip, "iwlwifi") || strings.HasPrefix(chip, "ath") {
		return "wifi"
	}
	return "other"
}

// hwmonSensor is one discovered *_input file with its static attributes.
type hwmonSensor struct {
	input       string
	chip, label string
	high, crit  float64
	kind        string
}

// hwmonSensors is the result of scanning /sys/class/hwmon. Chip names,
// labels and limits don't change at runtime, so the scan (dozens of globs
// and reads) is cached and only the *_input files are read per sample.
type hwmonSensors struct {
	temps, fans []hwmonSensor
}

func scanHwmon(root string) hwmonSensors {
	var hs hwmonSensors
	dirs, _ := filepath.Glob(filepath.Join(root, "hwmon*"))
	for _, d := range dirs {
		chip := readStr(filepath.Join(d, "name"))
		inputs, _ := filepath.Glob(filepath.Join(d, "temp*_input"))
		for _, in := range inputs {
			base := strings.TrimSuffix(in, "_input")
			label := readStr(base + "_label")
			if label == "" {
				label = chip
			}
			t := hwmonSensor{input: in, chip: chip, label: label, kind: chipKind(chip, label)}
			if h, ok := readInt(base + "_max"); ok && h > 0 {
				t.high = float64(h) / 1000
			}
			if h, ok := readInt(base + "_crit"); ok && h > 0 {
				t.crit = float64(h) / 1000
			}
			hs.temps = append(hs.temps, t)
		}
		finputs, _ := filepath.Glob(filepath.Join(d, "fan*_input"))
		for _, in := range finputs {
			label := readStr(strings.TrimSuffix(in, "_input") + "_label")
			if label == "" {
				label = strings.TrimSuffix(filepath.Base(in), "_input")
			}
			hs.fans = append(hs.fans, hwmonSensor{input: in, chip: chip, label: label})
		}
	}
	return hs
}

// read samples the sensors, dropping readings that are obviously
// disconnected sensors.
func (hs hwmonSensors) read() ([]Temp, []Fan) {
	var temps []Temp
	var fans []Fan
	for _, sn := range hs.temps {
		v, ok := readInt(sn.input)
		if !ok {
			continue
		}
		c := float64(v) / 1000
		if c <= 0 || c >= 105 {
			continue
		}
		temps = append(temps, Temp{Chip: sn.chip, Label: sn.label, Value: c, High: sn.high, Crit: sn.crit, Kind: sn.kind})
	}
	for _, sn := range hs.fans {
		v, ok := readInt(sn.input)
		if !ok || v <= 0 {
			continue
		}
		fans = append(fans, Fan{Chip: sn.chip, Label: sn.label, RPM: int(v)})
	}
	order := map[string]int{"cpu": 0, "gpu": 1, "disk": 2, "board": 3, "wifi": 4, "core": 5, "other": 6}
	sort.SliceStable(temps, func(a, b int) bool {
		if order[temps[a].Kind] != order[temps[b].Kind] {
			return order[temps[a].Kind] < order[temps[b].Kind]
		}
		if temps[a].Chip != temps[b].Chip {
			return temps[a].Chip < temps[b].Chip
		}
		return natLess(temps[a].Label, temps[b].Label)
	})
	return temps, fans
}

// readHwmon scans root and samples it once.
func readHwmon(root string) ([]Temp, []Fan) { return scanHwmon(root).read() }

// natLess compares strings with embedded numbers naturally ("Core 2" < "Core 10").
func natLess(a, b string) bool {
	for a != "" && b != "" {
		ad, bd := a[0] >= '0' && a[0] <= '9', b[0] >= '0' && b[0] <= '9'
		if ad && bd {
			i, j := 0, 0
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			x, _ := strconv.Atoi(a[:i])
			y, _ := strconv.Atoi(b[:j])
			if x != y {
				return x < y
			}
			a, b = a[i:], b[j:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}
