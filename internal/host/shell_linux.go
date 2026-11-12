// SPDX-License-Identifier: AGPL-3.0-only

package host

import "os"

// LoginShell returns the shell of the /etc/passwd entry matching uid and
// name, or "" when there is none.
func LoginShell(name string, uid int) string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return ""
	}
	defer f.Close()
	for _, pu := range ParsePasswd(f) {
		if pu.UID == uid && pu.Name == name {
			return pu.Shell
		}
	}
	return ""
}
