// SPDX-License-Identifier: AGPL-3.0-only

package term

import (
	"os/user"
	"strconv"
	"testing"

	"github.com/kushalpandya/wargo/internal/host"
)

func TestCurrentAccountDarwin(t *testing.T) {
	a := currentAccount(func(string) string { return "" }) // launchd gives no $SHELL
	u, _ := user.Current()
	uid, _ := strconv.Atoi(u.Uid)
	if a.Shell != host.LoginShell(u.Username, uid) || a.Home != u.HomeDir {
		t.Fatalf("%+v", a)
	}
}
