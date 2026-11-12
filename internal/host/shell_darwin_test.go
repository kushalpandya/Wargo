// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLoginShellCurrentUser(t *testing.T) {
	u, _ := user.Current()
	uid, _ := strconv.Atoi(u.Uid)
	sh := LoginShell(u.Username, uid)
	if !filepath.IsAbs(sh) {
		t.Fatalf("shell %q", sh)
	}
	if st, err := os.Stat(sh); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("not executable: %q", sh)
	}
	if LoginShell("-x", uid) != "/bin/zsh" || LoginShell("a/b", uid) != "/bin/zsh" {
		t.Fatal("bad names must not reach dscl")
	}
}
