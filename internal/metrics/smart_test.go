// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"os"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseSmartctlNVMe(t *testing.T) {
	f, standby, err := parseSmartctl(readFixture(t, "smartctl-nvme.json"))
	if err != nil || standby {
		t.Fatalf("err %v standby %v", err, standby)
	}
	if !f.Known || f.Passed == nil || !*f.Passed || *f.WearPct != 4 || f.PowerOnHours != 6802 || *f.Spare != 100 || *f.SpareThreshold != 10 {
		t.Fatalf("facts = %+v", f)
	}
	// 36690 error log entries are normal noise and must not warn.
	if h := verdict(f); h.Status != HealthOK {
		t.Fatalf("verdict = %+v, want ok", h)
	}
}

func TestParseSmartctlSATASSD(t *testing.T) {
	f, _, err := parseSmartctl(readFixture(t, "smartctl-sata-ssd.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Known || *f.WearPct != 2 || f.PowerOnHours != 19267 || f.Reallocated != 0 {
		t.Fatalf("facts = %+v", f)
	}
	// UDMA CRC errors (236 here) are cabling, not drive health.
	if h := verdict(f); h.Status != HealthOK {
		t.Fatalf("verdict = %+v, want ok", h)
	}
}

func TestParseSmartctlFailingHDD(t *testing.T) {
	js := `{"smartctl":{"exit_status":24},"smart_status":{"passed":true},"power_on_time":{"hours":31880},
	"ata_smart_attributes":{"table":[
	 {"id":5,"name":"Reallocated_Sector_Ct","value":90,"when_failed":"","raw":{"value":8}},
	 {"id":197,"name":"Current_Pending_Sector","value":100,"when_failed":"","raw":{"value":2}},
	 {"id":10,"name":"Spin_Retry_Count","value":100,"when_failed":"past","flags":{"prefailure":true},"raw":{"value":0}},
	 {"id":190,"name":"Airflow_Temperature_Cel","value":40,"when_failed":"now","flags":{"prefailure":false},"raw":{"value":60}},
	 {"id":7,"name":"Seek_Error_Rate","value":40,"when_failed":"past","flags":{"prefailure":false},"raw":{"value":0}}]}}`
	f, _, err := parseSmartctl([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	h := verdict(f)
	if h.Status != HealthWarn || len(h.Notes) != 3 || h.Notes[0] != "8 reallocated sectors" {
		t.Fatalf("verdict = %+v, want warn with three notes", h)
	}
	// A hot Old_age temperature attribute must not fail a passing drive.
	if len(f.FailingNow) != 0 || len(f.FailedPast) != 1 {
		t.Fatalf("now %v past %v, want only the Pre-fail attribute", f.FailingNow, f.FailedPast)
	}
}

func TestParseSmartctlNoData(t *testing.T) {
	cases := map[string]string{
		`{"smartctl":{"exit_status":2,"messages":[{"string":"Smartctl open device: /dev/sda failed: Permission denied","severity":"error"}]}}`:                 "Reading drive health needs root (container mode reads it)",
		`{"smartctl":{"exit_status":1,"messages":[{"string":"/dev/sdb: Unknown USB bridge [0x1234:0x5678]\nPlease specify device type","severity":"error"}]}}`: "The USB adapter does not pass drive health through",
		`{"smartctl":{"exit_status":4},"smart_support":{"available":false}}`:                                                                                   "The drive does not support SMART",
	}
	for js, want := range cases {
		f, _, err := parseSmartctl([]byte(js))
		if err != nil {
			t.Fatal(err)
		}
		if h := verdict(f); h.Status != HealthUnknown || h.Reason != want {
			t.Errorf("verdict = %+v, want unknown %q", h, want)
		}
	}
}

func TestParseSmartctlStandby(t *testing.T) {
	_, standby, err := parseSmartctl([]byte(`{"smartctl":{"exit_status":2,"messages":[{"string":"Device is in STANDBY mode, exit(2)","severity":"information"}]}}`))
	if err != nil || !standby {
		t.Fatalf("standby = %v err %v, want true", standby, err)
	}
}

func TestTrimLabelAndDiscardOpt(t *testing.T) {
	if trimLabel(true) != "on" || trimLabel(false) != "off" {
		t.Fatal("trimLabel")
	}
	if !discardOpt([]string{"rw", "discard=async"}) || !discardOpt([]string{"discard"}) || discardOpt([]string{"rw", "nodiscard"}) {
		t.Fatal("discardOpt")
	}
}
