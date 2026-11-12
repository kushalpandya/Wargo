// SPDX-License-Identifier: AGPL-3.0-only

package term

// Terminal environment defaults on macOS. launchd starts Wargo with a bare
// PATH and no LANG, so the login PATH is resolved from the user's shell.
const (
	defaultPath = "/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	defaultLang = "en_US.UTF-8"

	resolvePath = true
)
