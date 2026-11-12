// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const darwinFallbackShell = "/bin/zsh"

var (
	shellMu    sync.Mutex
	shellCache = map[string]string{}
)

// LoginShell asks Directory Services for the account's login shell. Real
// accounts are not in /etc/passwd on macOS and launchd sets no $SHELL. It
// falls back to /bin/zsh. Results are cached per name.
func LoginShell(name string, uid int) string {
	if name == "" || strings.Contains(name, "/") || strings.HasPrefix(name, "-") {
		return darwinFallbackShell
	}
	shellMu.Lock()
	if s, ok := shellCache[name]; ok {
		shellMu.Unlock()
		return s
	}
	shellMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/dscl", ".", "-read", "/Users/"+name, "UserShell").Output()
	sh := ""
	if err == nil {
		sh = parseDsclShell(out)
	}
	if sh == "" {
		sh = darwinFallbackShell
	}
	shellMu.Lock()
	shellCache[name] = sh
	shellMu.Unlock()
	return sh
}
