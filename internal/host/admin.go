// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"os"
	"os/user"
	"strconv"
	"sync"
)

// Account is the admin user Wargo acts as, with its groups resolved.
type Account struct {
	User
	Groups []int // supplementary gids (incl. the primary gid)
}

var (
	adminMu sync.Mutex
	admin   *Account
)

// SetAdmin resolves name as the admin account. In native mode Wargo runs as
// that account already, so this only records it.
func SetAdmin(name string) (*Account, error) {
	u, err := LookupUser(name)
	if err != nil {
		return nil, err
	}
	gs, err := UserGroups(u)
	if err != nil {
		gs = []int{u.GID}
	}
	a := &Account{User: *u, Groups: gs}
	adminMu.Lock()
	admin = a
	adminMu.Unlock()
	return a, nil
}

// Admin returns the admin account. Native mode defaults to the account the
// process runs as; container mode returns nil until SetAdmin succeeded.
func Admin() *Account {
	adminMu.Lock()
	a := admin
	adminMu.Unlock()
	if a != nil || Container() {
		return a
	}
	u := &User{UID: os.Getuid(), GID: os.Getgid(), Name: os.Getenv("USER"), Home: os.Getenv("HOME")}
	if ou, err := user.Current(); err == nil {
		u = fromOS(ou)
	}
	gs, _ := os.Getgroups()
	a = &Account{User: *u, Groups: append([]int{u.GID}, gs...)}
	adminMu.Lock()
	admin = a
	adminMu.Unlock()
	return a
}

// AdminUID is the uid of the admin account (os.Getuid() natively).
func AdminUID() int {
	if !Container() {
		return os.Getuid()
	}
	if a := Admin(); a != nil {
		return a.UID
	}
	return -1
}

// Identity is the effective identity used for file permission decisions:
// the process's own natively, the admin account in container mode.
func Identity() (uid, gid int, groups []int) {
	if !Container() {
		gs, _ := os.Getgroups()
		return os.Geteuid(), os.Getegid(), gs
	}
	a := Admin()
	if a == nil {
		return 0, 0, nil
	}
	return a.UID, a.GID, a.Groups
}

// AdminName is the admin's user name ("" when unknown).
func AdminName() string {
	if a := Admin(); a != nil {
		return a.Name
	}
	return ""
}

func itoa(n int) string { return strconv.Itoa(n) }
