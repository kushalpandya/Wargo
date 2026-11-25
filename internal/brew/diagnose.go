// SPDX-License-Identifier: AGPL-3.0-only

package brew

import (
	"strings"

	"github.com/kushalpandya/wargo/internal/pkg"
)

// Diagnosis codes of a failed Homebrew update.
const (
	CodeLink     = "brew.link"     // a keg couldn't be linked (files in the way)
	CodeConflict = "brew.conflict" // conflicting formulae are installed
	CodeLocked   = "brew.locked"   // another brew process is running
	CodeNoSpace  = "brew.no_space" // not enough disk space
	CodeNetwork  = "brew.network"  // a download failed
	CodeCask     = "brew.cask"     // a cask's installer or uninstaller failed
	CodeFailed   = "brew.failed"   // something else; Lines has the errors
)

var brewCodes = map[string]pkg.Explanation{
	CodeConflict: {Rank: 7, Title: "Formula conflict",
		Summary: "A formula conflicts with one that is installed. Unlink or uninstall the other formula in a terminal, then update again."},
	CodeLink: {Rank: 6, Title: "Link failed",
		Summary: "A formula was installed but couldn't be linked because other files are in the way. Run brew link --overwrite for it in a terminal, or remove the files."},
	CodeNoSpace: {Rank: 5, Title: "Not enough disk space", Summary: "There wasn't enough free disk space. Free some space (for example with brew cleanup) and try again."},
	CodeLocked:  {Rank: 4, Title: "Homebrew busy", Summary: "Another Homebrew process was running. Wait for it to finish and try again."},
	CodeNetwork: {Rank: 3, Title: "Download failed", Summary: "A download failed. Check the network and try again."},
	CodeCask:    {Rank: 2, Title: "Cask installer failed", Summary: "A cask's installer or uninstaller failed; see the lines below."},
	CodeFailed:  {Rank: 1, Title: "Update failed", Summary: "Homebrew stopped with an error; see the lines below."},
}

// diagnose explains a failed update from its log (nil if it can't).
func diagnose(lines []string) *pkg.Diagnosis {
	d := pkg.Diagnoser{Codes: brewCodes}
	for _, raw := range lines {
		l := strings.TrimSpace(pkg.CleanLine(raw))
		switch {
		case l == "":
		case strings.Contains(l, "brew link") && strings.Contains(l, "did not complete"),
			strings.Contains(l, "Could not symlink"), strings.HasPrefix(l, "Target ") && strings.Contains(l, "already exists"):
			d.Add(CodeLink, l)
		case strings.Contains(l, "conflicting formulae are installed"), strings.Contains(l, "because conflicting"):
			d.Add(CodeConflict, l)
		case strings.Contains(l, "has already locked"), strings.Contains(l, "Another active Homebrew"):
			d.Add(CodeLocked, l)
		case strings.HasPrefix(l, "curl: ("), strings.Contains(l, "Failed to download resource"), strings.Contains(l, "Download failed"):
			d.Add(CodeNetwork, l)
		case strings.Contains(l, "installer: Error"), strings.Contains(l, "Failure while executing") && strings.Contains(l, "installer"):
			d.Add(CodeCask, l)
		case d.Common(l, CodeNoSpace, CodeNetwork):
		case strings.HasPrefix(l, "Error:"):
			d.Add(CodeFailed, l)
		}
	}
	return d.Result()
}
