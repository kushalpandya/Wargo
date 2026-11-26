// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"encoding/json"
	"strings"
)

// Parser for `smartctl -j -a`, untagged so it is tested on every platform.

type smartctlJSON struct {
	Smartctl struct {
		ExitStatus int `json:"exit_status"`
		Messages   []struct {
			String   string `json:"string"`
			Severity string `json:"severity"`
		} `json:"messages"`
	} `json:"smartctl"`
	SmartSupport *struct {
		Available *bool `json:"available"`
		Enabled   *bool `json:"enabled"`
	} `json:"smart_support"`
	SmartStatus *struct {
		Passed *bool `json:"passed"`
	} `json:"smart_status"`
	PowerOnTime *struct {
		Hours uint64 `json:"hours"`
	} `json:"power_on_time"`
	NVMe *struct {
		CriticalWarning         uint8    `json:"critical_warning"`
		AvailableSpare          *float64 `json:"available_spare"`
		AvailableSpareThreshold *float64 `json:"available_spare_threshold"`
		PercentageUsed          *float64 `json:"percentage_used"`
		MediaErrors             uint64   `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
	ATA *struct {
		Table []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Value      int    `json:"value"`
			WhenFailed string `json:"when_failed"`
			Flags      struct {
				Prefailure bool `json:"prefailure"`
			} `json:"flags"`
			Raw struct {
				Value uint64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
}

// ATA attributes whose normalized value is the percentage of life left,
// by the names smartctl's drive database gives them.
var lifeLeftAttrs = []string{"Percent_Lifetime_Remain", "SSD_Life_Left", "Wear_Leveling_Count", "Media_Wearout_Indicator", "Percent_Life_Remaining"}

// parseSmartctl reads smartctl's JSON. standby reports that the drive was
// asleep and smartctl left it alone (-n standby). smartctl's exit status
// is a bitmask that is non-zero for many healthy drives, so callers pass
// stdout whatever the exit status was.
func parseSmartctl(out []byte) (f smartFacts, standby bool, err error) {
	var j smartctlJSON
	if err := json.Unmarshal(out, &j); err != nil {
		return smartFacts{}, false, err
	}
	var errMsg string
	for _, m := range j.Smartctl.Messages {
		s := m.String
		if strings.Contains(strings.ToUpper(s), "STANDBY") || strings.Contains(s, "SLEEP mode") {
			standby = true
		}
		if errMsg == "" && m.Severity == "error" {
			errMsg = s
		}
	}
	if j.PowerOnTime != nil {
		f.PowerOnHours = j.PowerOnTime.Hours
	}
	if j.SmartStatus != nil && j.SmartStatus.Passed != nil {
		f.Known, f.Passed = true, j.SmartStatus.Passed
	}
	if n := j.NVMe; n != nil {
		f.Known = true
		f.CriticalWarning = n.CriticalWarning
		f.Spare, f.SpareThreshold = n.AvailableSpare, n.AvailableSpareThreshold
		f.WearPct = n.PercentageUsed
		f.MediaErrors = n.MediaErrors
	}
	if a := j.ATA; a != nil && len(a.Table) > 0 {
		f.Known = true
		for _, at := range a.Table {
			switch at.ID {
			case 5:
				f.Reallocated = at.Raw.Value
			case 197:
				f.Pending = at.Raw.Value
			case 187, 198:
				f.Uncorrectable = max(f.Uncorrectable, at.Raw.Value)
			}
			if f.WearPct == nil {
				for _, n := range lifeLeftAttrs {
					if at.Name == n && at.Value >= 0 && at.Value <= 100 {
						w := float64(100 - at.Value)
						f.WearPct = &w
					}
				}
			}
			// Only Pre-fail attributes predict failure; Old_age ones
			// (temperature above all) cross their thresholds on healthy
			// drives, and smartctl's own verdict ignores them too.
			if !at.Flags.Prefailure || at.ID == 190 || at.ID == 194 {
				continue
			}
			switch at.WhenFailed {
			case "now":
				f.FailingNow = append(f.FailingNow, at.Name)
			case "past":
				f.FailedPast = append(f.FailedPast, at.Name)
			}
		}
	}
	if !f.Known {
		f.Reason = smartUnknownReason(j, errMsg)
	}
	return f, standby, nil
}

func smartUnknownReason(j smartctlJSON, errMsg string) string {
	switch {
	case strings.Contains(errMsg, "Permission denied") || strings.Contains(errMsg, "Operation not permitted"):
		return "Reading drive health needs root (container mode reads it)"
	case strings.Contains(errMsg, "USB bridge") || strings.Contains(errMsg, "specify device type"):
		return "The USB adapter does not pass drive health through"
	case j.SmartSupport != nil && j.SmartSupport.Available != nil && !*j.SmartSupport.Available:
		return "The drive does not support SMART"
	case j.SmartSupport != nil && j.SmartSupport.Enabled != nil && !*j.SmartSupport.Enabled:
		return "SMART is turned off on this drive"
	}
	return noHealthReport
}

// trimLabel is the Trim value for an SSD that supports TRIM: "on" when
// something runs it (online discard or a periodic trim), else "off".
func trimLabel(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// discardOpt reports whether mount options turn on online discard
// ("discard", "discard=async"), not "nodiscard".
func discardOpt(opts []string) bool {
	for _, o := range opts {
		if o == "discard" || strings.HasPrefix(o, "discard=") {
			return true
		}
	}
	return false
}
