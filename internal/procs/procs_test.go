// SPDX-License-Identifier: AGPL-3.0-only

package procs

import "testing"

func TestParseStat(t *testing.T) {
	tests := []struct {
		name, in string
		want     statFields
		err      bool
	}{
		{
			name: "simple",
			in:   "1234 (bash) S 1000 1234 1234 34816 1234 4194304 100 0 0 0 250 50 0 0 20 0 1 0 5000 10000000 512 18446744073709551615",
			want: statFields{Name: "bash", State: "S", PPID: 1000, UTime: 250, STime: 50, Nice: 0, Threads: 1, StartTime: 5000, VSize: 10000000, RSSPages: 512},
		},
		{
			name: "name with spaces and parens",
			in:   "77 (tmux: server) (x)) R 1 77 77 0 -1 4194560 1 0 0 0 7 3 0 0 20 -5 4 0 900 2048 10 0",
			want: statFields{Name: "tmux: server) (x)", State: "R", PPID: 1, UTime: 7, STime: 3, Nice: -5, Threads: 4, StartTime: 900, VSize: 2048, RSSPages: 10},
		},
		{name: "malformed", in: "garbage", err: true},
		{name: "short", in: "1 (x) S 0 0", err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStat([]byte(tt.in))
			if (err != nil) != tt.err {
				t.Fatalf("err = %v, want err %v", err, tt.err)
			}
			if !tt.err && got != tt.want {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestCleanCmdline(t *testing.T) {
	tests := []struct {
		in    string
		limit int
		want  string
	}{
		{"/usr/bin/foo\x00--bar\x00baz\x00", 0, "/usr/bin/foo --bar baz"},
		{"abc\x00def", 5, "abc d"},
		{"a\nb\x00", 0, "a?b"},
		{"", 0, ""},
	}
	for _, tt := range tests {
		if got := cleanCmdline([]byte(tt.in), tt.limit); got != tt.want {
			t.Errorf("cleanCmdline(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestContainerFromCgroup(t *testing.T) {
	id := "f9a0ad28e7b3a0e578027039c4d6b6ed40d48c7eac6831fad29a3e98f067bb55"
	tests := map[string]string{
		"0::/system.slice/docker-" + id + ".scope\n": id,
		"12:memory:/docker/" + id + "\n":             id,
		"0::/user.slice/user-1000.slice\n":           "",
		"0::/system.slice/docker.service\n":          "",
	}
	for in, want := range tests {
		if got := containerFromCgroup([]byte(in)); got != want {
			t.Errorf("containerFromCgroup(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKB(t *testing.T) {
	if got := kb("  1024 kB"); got != 1024*1024 {
		t.Fatal(got)
	}
	if got := kb(""); got != 0 {
		t.Fatal(got)
	}
}
