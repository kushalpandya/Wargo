// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// User is a host account.
type User struct {
	Name  string
	UID   int
	GID   int
	Gecos string
	Home  string
	Shell string
}

// Group is a host group.
type Group struct {
	Name    string
	GID     int
	Members []string
}

// Shadow is the subset of a shadow(5) entry Wargo needs.
type Shadow struct {
	Name string
	Hash string
	// Expire is the account expiry (days since the epoch), -1 when unset.
	Expire int64
}

// ErrUnknownUser / ErrUnknownGroup are returned for missing entries.
var (
	ErrUnknownUser  = errors.New("unknown user")
	ErrUnknownGroup = errors.New("unknown group")
)

// DB is a parsed passwd/group database rooted at a directory (the host
// root in container mode, or a test fixture).
type DB struct {
	root   string
	users  []User
	groups []Group
	at     time.Time
	mtime  [2]time.Time
}

// LoadDB parses <root>/etc/passwd and <root>/etc/group.
func LoadDB(root string) (*DB, error) {
	d := &DB{root: root, at: time.Now()}
	pf, err := os.Open(filepath.Join(root, "etc/passwd"))
	if err != nil {
		return nil, err
	}
	defer pf.Close()
	d.users = ParsePasswd(pf)
	if st, err := pf.Stat(); err == nil {
		d.mtime[0] = st.ModTime()
	}
	gf, err := os.Open(filepath.Join(root, "etc/group"))
	if err != nil {
		return nil, err
	}
	defer gf.Close()
	d.groups = ParseGroup(gf)
	if st, err := gf.Stat(); err == nil {
		d.mtime[1] = st.ModTime()
	}
	return d, nil
}

// ParsePasswd parses passwd(5). Malformed lines and NIS "+" entries are
// skipped.
func ParsePasswd(r io.Reader) []User {
	var out []User
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || l[0] == '#' || l[0] == '+' || l[0] == '-' {
			continue
		}
		f := strings.Split(l, ":")
		if len(f) < 7 || f[0] == "" {
			continue
		}
		uid, err1 := strconv.Atoi(f[2])
		gid, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil || uid < 0 || gid < 0 {
			continue
		}
		out = append(out, User{Name: f[0], UID: uid, GID: gid, Gecos: f[4], Home: f[5], Shell: f[6]})
	}
	return out
}

// ParseGroup parses group(5).
func ParseGroup(r io.Reader) []Group {
	var out []Group
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || l[0] == '#' || l[0] == '+' || l[0] == '-' {
			continue
		}
		f := strings.Split(l, ":")
		if len(f) < 4 || f[0] == "" {
			continue
		}
		gid, err := strconv.Atoi(f[2])
		if err != nil || gid < 0 {
			continue
		}
		g := Group{Name: f[0], GID: gid}
		for _, m := range strings.Split(f[3], ",") {
			if m = strings.TrimSpace(m); m != "" {
				g.Members = append(g.Members, m)
			}
		}
		out = append(out, g)
	}
	return out
}

// ParseShadow parses shadow(5).
func ParseShadow(r io.Reader) []Shadow {
	var out []Shadow
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), "\r")
		if l == "" || l[0] == '#' {
			continue
		}
		f := strings.Split(l, ":")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		s := Shadow{Name: f[0], Hash: f[1], Expire: -1}
		if len(f) >= 8 && f[7] != "" {
			if v, err := strconv.ParseInt(f[7], 10, 64); err == nil {
				s.Expire = v
			}
		}
		out = append(out, s)
	}
	return out
}

func (d *DB) User(name string) (*User, error) {
	for i := range d.users {
		if d.users[i].Name == name {
			u := d.users[i]
			return &u, nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownUser, name)
}

func (d *DB) UID(uid int) (*User, error) {
	for i := range d.users {
		if d.users[i].UID == uid {
			u := d.users[i]
			return &u, nil
		}
	}
	return nil, fmt.Errorf("%w %d", ErrUnknownUser, uid)
}

func (d *DB) Group(name string) (*Group, error) {
	for i := range d.groups {
		if d.groups[i].Name == name {
			g := d.groups[i]
			return &g, nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownGroup, name)
}

func (d *DB) GID(gid int) (*Group, error) {
	for i := range d.groups {
		if d.groups[i].GID == gid {
			g := d.groups[i]
			return &g, nil
		}
	}
	return nil, fmt.Errorf("%w %d", ErrUnknownGroup, gid)
}

// Groups returns u's primary gid followed by its supplementary gids.
func (d *DB) Groups(u *User) []int {
	out := []int{u.GID}
	for _, g := range d.groups {
		if slices.Contains(g.Members, u.Name) && !slices.Contains(out, g.GID) {
			out = append(out, g.GID)
		}
	}
	return out
}

// FirstAdmin returns the first member of the first existing group in
// groups with uid >= 1000 (a regular account), in group member order.
func (d *DB) FirstAdmin(groups ...string) (*User, string) {
	for _, gn := range groups {
		g, err := d.Group(gn)
		if err != nil {
			continue
		}
		for _, m := range g.Members {
			if u, err := d.User(m); err == nil && u.UID >= 1000 && u.UID < 65534 {
				return u, gn
			}
		}
		// Accounts whose primary group is the admin group.
		for _, u := range d.users {
			if u.GID == g.GID && u.UID >= 1000 && u.UID < 65534 {
				uu := u
				return &uu, gn
			}
		}
	}
	return nil, ""
}

// ShadowEntry reads <root>/etc/shadow for name.
func (d *DB) ShadowEntry(name string) (*Shadow, error) {
	f, err := os.Open(filepath.Join(d.root, "etc/shadow"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	for _, s := range ParseShadow(io.LimitReader(f, 16<<20)) {
		if s.Name == name {
			ss := s
			return &ss, nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownUser, name)
}

// ---- process-wide host database ----------------------------------------

var (
	dbMu    sync.Mutex
	dbCache *DB
)

// hostDB returns the cached container-mode database, reloading it when the
// files change (checked at most every few seconds).
func hostDB() (*DB, error) {
	r := Root()
	dbMu.Lock()
	defer dbMu.Unlock()
	if dbCache != nil && time.Since(dbCache.at) < 5*time.Second {
		return dbCache, nil
	}
	if dbCache != nil {
		var cur [2]time.Time
		if st, err := os.Stat(filepath.Join(r, "etc/passwd")); err == nil {
			cur[0] = st.ModTime()
		}
		if st, err := os.Stat(filepath.Join(r, "etc/group")); err == nil {
			cur[1] = st.ModTime()
		}
		if cur == dbCache.mtime {
			dbCache.at = time.Now()
			return dbCache, nil
		}
	}
	d, err := LoadDB(r)
	if err != nil {
		return nil, err
	}
	dbCache = d
	return d, nil
}

// LookupUser finds a host account by name.
func LookupUser(name string) (*User, error) {
	if !Container() {
		u, err := user.Lookup(name)
		if err != nil {
			return nil, err
		}
		return fromOS(u), nil
	}
	d, err := hostDB()
	if err != nil {
		return nil, err
	}
	return d.User(name)
}

// LookupUID finds a host account by uid.
func LookupUID(uid int) (*User, error) {
	if !Container() {
		u, err := user.LookupId(strconv.Itoa(uid))
		if err != nil {
			return nil, err
		}
		return fromOS(u), nil
	}
	d, err := hostDB()
	if err != nil {
		return nil, err
	}
	return d.UID(uid)
}

// LookupGroup finds a host group by name.
func LookupGroup(name string) (*Group, error) {
	if !Container() {
		g, err := user.LookupGroup(name)
		if err != nil {
			return nil, err
		}
		gid, _ := strconv.Atoi(g.Gid)
		return &Group{Name: g.Name, GID: gid}, nil
	}
	d, err := hostDB()
	if err != nil {
		return nil, err
	}
	return d.Group(name)
}

// LookupGID finds a host group by gid.
func LookupGID(gid int) (*Group, error) {
	if !Container() {
		g, err := user.LookupGroupId(strconv.Itoa(gid))
		if err != nil {
			return nil, err
		}
		return &Group{Name: g.Name, GID: gid}, nil
	}
	d, err := hostDB()
	if err != nil {
		return nil, err
	}
	return d.GID(gid)
}

// UserGroups returns the gids of u (primary first).
func UserGroups(u *User) ([]int, error) {
	if !Container() {
		ou, err := user.LookupId(strconv.Itoa(u.UID))
		if err != nil {
			return nil, err
		}
		ids, err := ou.GroupIds()
		if err != nil {
			return nil, err
		}
		out := []int{u.GID}
		for _, s := range ids {
			if v, err := strconv.Atoi(s); err == nil && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		return out, nil
	}
	d, err := hostDB()
	if err != nil {
		return nil, err
	}
	return d.Groups(u), nil
}

// LookupShadow reads the host's shadow entry (container mode only; needs
// root inside the container).
func LookupShadow(name string) (*Shadow, error) {
	if !Container() {
		return nil, errors.New("shadow lookups are only used in container mode")
	}
	d, err := hostDB()
	if err != nil {
		return nil, err
	}
	return d.ShadowEntry(name)
}

// DefaultAdmin picks the admin account in container mode: the first
// regular member of wheel, sudo or admin.
func DefaultAdmin() (*User, string, error) {
	d, err := hostDB()
	if err != nil {
		return nil, "", err
	}
	u, g := d.FirstAdmin("wheel", "sudo", "admin")
	if u == nil {
		return nil, "", errors.New("no member of wheel/sudo/admin with uid >= 1000 found on the host; set WARGO_USER")
	}
	return u, g, nil
}

func fromOS(u *user.User) *User {
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	out := &User{Name: u.Username, UID: uid, GID: gid, Gecos: u.Name, Home: u.HomeDir}
	if f, err := os.Open("/etc/passwd"); err == nil {
		for _, pu := range ParsePasswd(f) {
			if pu.UID == uid && pu.Name == u.Username {
				out.Shell = pu.Shell
				break
			}
		}
		f.Close()
	}
	return out
}
