// SPDX-License-Identifier: AGPL-3.0-only

package torrent

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

const singleInfo = "d6:lengthi1048576e4:name15:debian-12.6.iso12:piece lengthi262144e6:pieces20:ABCDEFGHIJKLMNOPQRSTe"
const multiInfo = "d5:filesld6:lengthi100e4:pathl5:a.txteed6:lengthi200e4:pathl3:sub5:b.txteee4:name3:dir12:piece lengthi16384e6:pieces20:01234567890123456789e"

// Expected hashes computed independently with Python's hashlib over the raw info bytes.
const singleHash = "fe3f0564c4c5015a479ab7470319c3b866116932"
const multiHash = "2771bd702b4edb69f5fed658d6d032404d6f3bc5"

func TestDecodeValues(t *testing.T) {
	tests := []struct {
		in   string
		want string // %v formatting of the decoded value
	}{
		{"i42e", "42"},
		{"i-7e", "-7"},
		{"i0e", "0"},
		{"4:spam", "[115 112 97 109]"},
		{"0:", "[]"},
		{"le", "[]"},
		{"li1ei2ee", "[1 2]"},
		{"de", "map[]"},
		{"d1:ai1ee", "map[a:1]"},
	}
	for _, tt := range tests {
		v, err := Decode([]byte(tt.in))
		if err != nil {
			t.Fatalf("%q: %v", tt.in, err)
		}
		if got := fmtAny(v); got != tt.want {
			t.Errorf("%q: got %s want %s", tt.in, got, tt.want)
		}
	}
}

func fmtAny(v any) string { return fmt.Sprint(v) }

func TestDecodeMalformed(t *testing.T) {
	bad := []string{
		"", "i", "ie", "i-e", "i-0e", "i03e", "i12", "i1x2e", "i123456789012345678901234e",
		"5:abc", "-1:a", "01:a", "4294967296:x", "1234567890:x", "l", "li1e", "d", "d1:a", "di1ei2ee",
		"d1:ai1e1:ai2ee", "x", "i1ei2e", "4:spamjunk", "d1:ae",
	}
	for _, in := range bad {
		if _, err := Decode([]byte(in)); err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestDecodeDepthLimit(t *testing.T) {
	ok := strings.Repeat("l", MaxDepth) + strings.Repeat("e", MaxDepth)
	if _, err := Decode([]byte(ok)); err != nil {
		t.Fatalf("depth %d should be fine: %v", MaxDepth, err)
	}
	deep := strings.Repeat("l", MaxDepth+1) + strings.Repeat("e", MaxDepth+1)
	if _, err := Decode([]byte(deep)); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("expected ErrTooDeep, got %v", err)
	}
	// unterminated huge nesting must not blow the stack either
	if _, err := Decode([]byte(strings.Repeat("d", 100000))); err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeOversized(t *testing.T) {
	b := make([]byte, MaxSize+1)
	if _, err := Decode(b); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
	if _, err := Parse(b); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Parse: expected ErrTooLarge, got %v", err)
	}
}

func TestParseSingleFile(t *testing.T) {
	tor := "d8:announce35:http://tracker.example.org/announce13:announce-listll35:http://tracker.example.org/announceel26:udp://tr2.example.net:6969el11:ftp://bad/xee4:info" + singleInfo + "e"
	m, err := Parse([]byte(tor))
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHash != singleHash {
		t.Errorf("info hash %s want %s", m.InfoHash, singleHash)
	}
	if m.Name != "debian-12.6.iso" || m.Length != 1048576 || m.InfoHashV2 != "" {
		t.Errorf("unexpected meta %+v", m)
	}
	want := []string{"http://tracker.example.org/announce", "udp://tr2.example.net:6969"}
	if strings.Join(m.Trackers, ",") != strings.Join(want, ",") {
		t.Errorf("trackers %v want %v (deduped, ftp dropped)", m.Trackers, want)
	}
}

func TestParseMultiFile(t *testing.T) {
	// keys intentionally after info to check the span is exact
	tor := "d4:info" + multiInfo + "7:comment3:hiie"
	m, err := Parse([]byte(tor))
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHash != multiHash || m.Length != 300 || m.Name != "dir" {
		t.Errorf("unexpected meta %+v", m)
	}
}

func TestParseRejects(t *testing.T) {
	for _, in := range []string{"i1e", "de", "d4:infoi1ee", "d4:infodee"} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestParseV2(t *testing.T) {
	info := "d9:file treed5:a.bind0:d6:lengthi512e11:pieces root32:" + strings.Repeat("x", 32) + "eee12:meta versioni2e4:name1:v12:piece lengthi16384ee"
	m, err := Parse([]byte("d4:info" + info + "e"))
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHash != "" || len(m.InfoHashV2) != 64 || m.Length != 512 {
		t.Errorf("unexpected v2 meta %+v", m)
	}
	if !strings.Contains(m.Magnet(), "xt=urn:btmh:1220"+m.InfoHashV2) {
		t.Errorf("magnet lacks btmh: %s", m.Magnet())
	}
}

func TestMagnetBuilder(t *testing.T) {
	got := BuildMagnet("ABCDEF0123456789ABCDEF0123456789ABCDEF01", "", "Debian 12 & more.iso", 1234, []string{"udp://t.example:80/announce", "http://x/a?b=c"})
	want := "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&dn=Debian+12+%26+more.iso&xl=1234&tr=udp%3A%2F%2Ft.example%3A80%2Fannounce&tr=http%3A%2F%2Fx%2Fa%3Fb%3Dc"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	q, err := url.ParseQuery(strings.TrimPrefix(got, "magnet:?"))
	if err != nil || q.Get("dn") != "Debian 12 & more.iso" || len(q["tr"]) != 2 {
		t.Fatalf("round trip failed: %v %v", q, err)
	}
	if MagnetInfoHash(got) != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Fatal("MagnetInfoHash round trip")
	}
	many := make([]string, 50)
	for i := range many {
		many[i] = "udp://t/" + string(rune('a'+i%26))
	}
	if n := strings.Count(BuildMagnet(singleHash, "", "", 0, many), "&tr="); n != MaxTrackers {
		t.Fatalf("tracker cap: %d", n)
	}
	if s := BuildMagnet(singleHash, "", "", 0, nil); s != "magnet:?xt=urn:btih:"+singleHash {
		t.Fatalf("minimal: %s", s)
	}
}

func TestMagnetInfoHash(t *testing.T) {
	tests := map[string]string{
		"magnet:?xt=urn:btih:2C6B6858D61DA9543D4231A71DB4B1C9264B0685&dn=x":        "2c6b6858d61da9543d4231a71db4b1c9264b0685",
		"magnet:?dn=x&xt=urn:btih:7Y7QKZGEYUAVUR42W5DQGGODXBTBC2JS":                singleHash,
		"magnet:?xt=urn:btih:nothex":                                               "",
		"http://example.com/?xt=urn:btih:2C6B6858D61DA9543D4231A71DB4B1C9264B0685": "",
	}
	for in, want := range tests {
		if got := MagnetInfoHash(in); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

func TestDecodeRejectsTooManyValues(t *testing.T) {
	in := []byte("l" + strings.Repeat("le", maxValues+1) + "e")
	if _, err := Decode(in); !errors.Is(err, errTooManyValues) {
		t.Fatalf("err = %v, want errTooManyValues", err)
	}
}
