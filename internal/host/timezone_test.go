// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeHost is a host root holding the zoneinfo of Toronto and Kolkata.
func fakeHost(t *testing.T) (root string, write func(p string, b []byte), link func(p, target string)) {
	t.Helper()
	root = t.TempDir()
	write = func(p string, b []byte) {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link = func(p, target string) {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, z := range []string{"America/Toronto", "Asia/Kolkata"} {
		b, err := os.ReadFile(filepath.Join("/usr/share/zoneinfo", z))
		if err != nil {
			t.Skip("no zoneinfo here")
		}
		write(filepath.Join("usr/share/zoneinfo", z), b)
	}
	t.Cleanup(SetRoot(root))
	return root, write, link
}

// offset is loc's UTC offset in hours on a winter day.
func offset(loc *time.Location) float64 {
	_, off := time.Date(2026, 11, 25, 12, 0, 0, 0, loc).Zone()
	return float64(off) / 3600
}

func TestLocationContainer(t *testing.T) {
	t.Run("linked localtime", func(t *testing.T) {
		_, _, link := fakeHost(t)
		// An absolute link, resolved inside the host root.
		link("etc/localtime", "/usr/share/zoneinfo/America/Toronto")
		if loc := loadLocation(); loc.String() != "America/Toronto" || offset(loc) != -5 {
			t.Fatalf("got %v (%v)", loc, offset(loc))
		}
	})
	t.Run("copied localtime", func(t *testing.T) {
		root, write, _ := fakeHost(t)
		b, _ := os.ReadFile(filepath.Join(root, "usr/share/zoneinfo/Asia/Kolkata"))
		write("etc/localtime", b)
		if loc := loadLocation(); offset(loc) != 5.5 {
			t.Fatalf("got %v (%v)", loc, offset(loc))
		}
	})
	t.Run("localtime wins over a stale timezone file", func(t *testing.T) {
		_, write, link := fakeHost(t)
		link("etc/localtime", "../usr/share/zoneinfo/America/Toronto")
		write("etc/timezone", []byte("Asia/Kolkata\n"))
		if loc := loadLocation(); loc.String() != "America/Toronto" || offset(loc) != -5 {
			t.Fatalf("got %v (%v)", loc, offset(loc))
		}
	})
	t.Run("timezone file only", func(t *testing.T) {
		_, write, _ := fakeHost(t)
		write("etc/timezone", []byte("Asia/Kolkata\n"))
		if loc := loadLocation(); loc.String() != "Asia/Kolkata" || offset(loc) != 5.5 {
			t.Fatalf("got %v (%v)", loc, offset(loc))
		}
	})
	t.Run("nothing set falls back to the container's zone", func(t *testing.T) {
		fakeHost(t)
		if loc := loadLocation(); loc != time.Local {
			t.Fatalf("got %v, want time.Local", loc)
		}
	})
	t.Run("a name outside zoneinfo is ignored", func(t *testing.T) {
		_, write, _ := fakeHost(t)
		write("etc/timezone", []byte("../../etc/passwd\n"))
		if loc := loadLocation(); loc != time.Local {
			t.Fatalf("got %v, want time.Local", loc)
		}
	})
	t.Run("Location caches until the root changes", func(t *testing.T) {
		_, write, _ := fakeHost(t)
		write("etc/timezone", []byte("Asia/Kolkata\n"))
		if loc := Location(); loc.String() != "Asia/Kolkata" {
			t.Fatalf("got %v", loc)
		}
		write("etc/timezone", []byte("America/Toronto\n"))
		if loc := Location(); loc.String() != "Asia/Kolkata" {
			t.Fatalf("not cached: %v", loc)
		}
	})
}

func TestLocationNative(t *testing.T) {
	t.Cleanup(SetRoot(""))
	if Location() != time.Local {
		t.Fatal("native Location is not time.Local")
	}
}
