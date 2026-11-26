// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"cmp"
	"fmt"
	"strconv"
)

// Drive health status values.
const (
	HealthOK      = "ok"
	HealthWarn    = "warn"
	HealthFail    = "fail"
	HealthUnknown = "unknown"
)

// noHealthReport is the reason shown when a drive reports nothing.
const noHealthReport = "The drive does not report health"

// wearWarnPct is the wear (percentage of rated endurance used) at which a
// passing drive is shown as a warning.
const wearWarnPct = 90

// DriveHealth is what the drive or the OS reports about a drive's health.
type DriveHealth struct {
	Status       string   `json:"status"`           // ok | warn | fail | unknown
	Reason       string   `json:"reason,omitempty"` // why it is unknown or failing
	WearPct      *float64 `json:"wearPct,omitempty"`
	PowerOnHours uint64   `json:"powerOnHours,omitempty"`
	SparePct     *float64 `json:"sparePct,omitempty"`
	Notes        []string `json:"notes,omitempty"` // what raised a warning
}

// smartFacts are the parts of a SMART report the verdict uses, in a
// platform-neutral form. Known is false when the drive reported nothing.
type smartFacts struct {
	Known           bool
	Reason          string // why nothing is known
	Passed          *bool  // the drive's own overall assessment
	CriticalWarning uint8  // NVMe critical warning bits
	Spare           *float64
	SpareThreshold  *float64
	WearPct         *float64
	PowerOnHours    uint64
	MediaErrors     uint64
	Reallocated     uint64
	Pending         uint64
	Uncorrectable   uint64
	FailingNow      []string // ATA attributes at or below threshold now
	FailedPast      []string // ATA attributes that were below threshold before
}

func unknownHealth(reason string) *DriveHealth {
	return &DriveHealth{Status: HealthUnknown, Reason: reason}
}

func plural(n uint64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.FormatUint(n, 10) + " " + many
}

func fmtPct(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + "%" }

// verdict turns SMART facts into a health status. The drive's own
// assessment decides pass or fail; a few well-understood warning signs
// raise a passing drive to warn.
func verdict(f smartFacts) DriveHealth {
	if !f.Known {
		return *unknownHealth(cmp.Or(f.Reason, noHealthReport))
	}
	h := DriveHealth{Status: HealthOK, WearPct: f.WearPct, PowerOnHours: f.PowerOnHours, SparePct: f.Spare}
	note := func(s string) { h.Notes = append(h.Notes, s) }

	if f.CriticalWarning != 0 {
		note(fmt.Sprintf("critical warning 0x%02x", f.CriticalWarning))
	}
	if f.Spare != nil && f.SpareThreshold != nil && *f.Spare < *f.SpareThreshold {
		note(fmt.Sprintf("spare %s (threshold %s)", fmtPct(*f.Spare), fmtPct(*f.SpareThreshold)))
	}
	if f.WearPct != nil && *f.WearPct >= wearWarnPct {
		note("wear " + fmtPct(*f.WearPct))
	}
	if f.MediaErrors > 0 {
		note(plural(f.MediaErrors, "media error", "media errors"))
	}
	if f.Reallocated > 0 {
		note(plural(f.Reallocated, "reallocated sector", "reallocated sectors"))
	}
	if f.Pending > 0 {
		note(plural(f.Pending, "pending sector", "pending sectors"))
	}
	if f.Uncorrectable > 0 {
		note(plural(f.Uncorrectable, "uncorrectable error", "uncorrectable errors"))
	}
	for _, a := range f.FailedPast {
		note(a + " failed in the past")
	}
	if len(h.Notes) > 0 {
		h.Status = HealthWarn
	}

	switch {
	case f.Passed != nil && !*f.Passed:
		h.Status, h.Reason = HealthFail, "The drive reports that it is failing"
	case len(f.FailingNow) > 0:
		h.Status, h.Reason = HealthFail, f.FailingNow[0]+" is past its failure threshold"
		for _, a := range f.FailingNow {
			note(a + " failing")
		}
	}
	return h
}
