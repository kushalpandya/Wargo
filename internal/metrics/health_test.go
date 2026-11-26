// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"slices"
	"testing"
)

func TestVerdictWarnings(t *testing.T) {
	cases := []struct {
		f    smartFacts
		note string
	}{
		{smartFacts{Reallocated: 8}, "8 reallocated sectors"},
		{smartFacts{Pending: 1}, "1 pending sector"},
		{smartFacts{Uncorrectable: 3}, "3 uncorrectable errors"},
		{smartFacts{MediaErrors: 2}, "2 media errors"},
		{smartFacts{WearPct: new(90.0)}, "wear 90%"},
		{smartFacts{Spare: new(5.0), SpareThreshold: new(10.0)}, "spare 5% (threshold 10%)"},
		{smartFacts{CriticalWarning: 0x04}, "critical warning 0x04"},
		{smartFacts{FailedPast: []string{"Spin_Retry_Count"}}, "Spin_Retry_Count failed in the past"},
	}
	for _, c := range cases {
		c.f.Known, c.f.Passed = true, new(true)
		h := verdict(c.f)
		if h.Status != HealthWarn || !slices.Contains(h.Notes, c.note) {
			t.Errorf("verdict(%+v) = %+v, want warn with note %q", c.f, h, c.note)
		}
	}
}

func TestVerdictWearBelowThresholdIsOK(t *testing.T) {
	if h := verdict(smartFacts{Known: true, Passed: new(true), WearPct: new(89.0)}); h.Status != HealthOK {
		t.Fatalf("status = %s, want ok just below the %d%% threshold", h.Status, wearWarnPct)
	}
}

func TestVerdictFail(t *testing.T) {
	if h := verdict(smartFacts{Known: true, Passed: new(false)}); h.Status != HealthFail || h.Reason == "" {
		t.Fatalf("verdict = %+v, want fail with a reason", h)
	}
	h := verdict(smartFacts{Known: true, Passed: new(true), FailingNow: []string{"Reallocated_Sector_Ct"}})
	if h.Status != HealthFail || h.Reason != "Reallocated_Sector_Ct is past its failure threshold" {
		t.Fatalf("verdict = %+v, want fail naming the attribute", h)
	}
}

func TestVerdictUnknown(t *testing.T) {
	h := verdict(smartFacts{Reason: "needs root"})
	if h.Status != HealthUnknown || h.Reason != "needs root" {
		t.Fatalf("verdict = %+v, want unknown with reason", h)
	}
}
