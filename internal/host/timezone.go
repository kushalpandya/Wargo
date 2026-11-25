// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Timezone is the host's IANA time zone (e.g. "America/Toronto"), or ""
// when it can't be told: from where /etc/localtime points (what the system
// uses), else from /etc/timezone, which Debian keeps only for compatibility
// and can be stale. Natively it falls back to the name of Go's local zone,
// which is "Local" unless TZ is set.
func Timezone() string {
	if l, err := os.Readlink(Path("/etc/localtime")); err == nil {
		if i := strings.Index(l, "zoneinfo/"); i >= 0 {
			return l[i+len("zoneinfo/"):]
		}
	}
	if b, err := os.ReadFile(Path("/etc/timezone")); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t
		}
	}
	if Container() {
		return ""
	}
	return time.Now().Location().String()
}

// locationTTL is how long Location keeps a zone it loaded from the host, so
// a change of the host's zone is picked up without reading it on every call.
const locationTTL = 10 * time.Minute

var locCache struct {
	sync.Mutex
	root string
	at   time.Time
	loc  *time.Location
}

// Location is the host's time zone. Natively that is time.Local. In
// container mode the container's own zone is usually UTC, so the zone is
// loaded from the host: the zone data /etc/localtime holds (a link or a
// copy), else the file /etc/timezone names in the host's zoneinfo, else the
// container's zoneinfo for that name. When none of these work it is the
// container's own zone (time.Local, which honours TZ).
func Location() *time.Location {
	r := Root()
	if r == "" {
		return time.Local
	}
	locCache.Lock()
	defer locCache.Unlock()
	if locCache.loc != nil && locCache.root == r && time.Since(locCache.at) < locationTTL {
		return locCache.loc
	}
	loc := loadLocation()
	locCache.root, locCache.at, locCache.loc = r, time.Now(), loc
	return loc
}

func loadLocation() *time.Location {
	name := Timezone()
	label := name
	if label == "" {
		label = "Local"
	}
	if b, err := ReadFile("/etc/localtime"); err == nil {
		if loc, err := time.LoadLocationFromTZData(label, b); err == nil {
			return loc
		}
	}
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "..") {
		return time.Local
	}
	if b, err := ReadFile(filepath.Join("/usr/share/zoneinfo", name)); err == nil {
		if loc, err := time.LoadLocationFromTZData(name, b); err == nil {
			return loc
		}
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.Local
}
