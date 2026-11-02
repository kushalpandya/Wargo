// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"io"
	"os"
	"syscall"
	"unicode/utf8"

	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// openRegular opens name inside root for reading and verifies (on the open
// descriptor) that it is a regular file. O_NONBLOCK makes opening a FIFO
// return at once instead of blocking the request (and, in container mode,
// its locked OS thread) until a writer shows up; O_NOCTTY keeps a terminal
// device from becoming our controlling tty. Both are no-ops for regular
// files. This closes the window between a Stat and the Open in which the
// file could be swapped for a FIFO or device.
func openRegular(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	f, err := fsops.OpenNonblock(root, name)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, nil, httpx.BadRequest("not a regular file")
	}
	return f, st, nil
}

// openDir opens name inside root as a directory without blocking on FIFOs.
func openDir(root *os.Root, name string) (*os.File, error) {
	f, err := fsops.OpenNonblock(root, name)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !st.IsDir() {
		f.Close()
		return nil, syscall.ENOTDIR
	}
	return f, nil
}

// readRegular reads a regular file of at most max bytes.
func readRegular(root *os.Root, name string, max int64) ([]byte, error) {
	f, _, err := openRegular(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// The file may grow after it was measured: never read more than max.
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errTooLargeToEdit
	}
	return b, nil
}

var errTooLargeToEdit = httpx.Errorf(413, "too_large", "File is too large to edit")

// sideName builds the name of a temporary sibling of base: prefix + base +
// suffix, shortening base (on a UTF-8 boundary) so the result stays within
// the 255-byte file name limit even for very long names.
func sideName(prefix, base, suffix string) string {
	const maxName = 255
	if room := maxName - len(prefix) - len(suffix); len(base) > room {
		if room < 0 {
			room = 0
		}
		base = base[:room]
		for len(base) > 0 && !utf8.ValidString(base) {
			base = base[:len(base)-1]
		}
	}
	return prefix + base + suffix
}
