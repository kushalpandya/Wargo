// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package metrics

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const systemVersionPlist = "/System/Library/CoreServices/SystemVersion.plist"

// platformOSRelease reads the macOS version from SystemVersion.plist.
func platformOSRelease() (pretty, id string, ok bool) {
	f, err := os.Open(systemVersionPlist)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	v, err := decodePlist(io.LimitReader(f, 1<<20))
	if err != nil {
		return "", "", false
	}
	m, _ := v.(map[string]any)
	ver, _ := m["ProductVersion"].(string)
	if ver == "" {
		return "", "", false
	}
	return "macOS " + ver, "macos", true
}

func applyPlatformSpecs(s *Specs) {
	if m, err := unix.Sysctl("hw.model"); err == nil {
		s.Model = m
	}
	s.Init = "launchd"
}

// platformPackages counts Homebrew formulae and casks by listing the
// Cellar and Caskroom directories (read-only).
func platformPackages() []PackageCount {
	for _, prefix := range []string{"/opt/homebrew", "/usr/local"} {
		formulae := countDirs(prefix + "/Cellar")
		casks := countDirs(prefix + "/Caskroom")
		if formulae < 0 && casks < 0 {
			continue
		}
		var out []PackageCount
		if formulae > 0 {
			out = append(out, PackageCount{"brew", formulae})
		}
		if casks > 0 {
			out = append(out, PackageCount{"brew-cask", casks})
		}
		return out
	}
	return nil
}
