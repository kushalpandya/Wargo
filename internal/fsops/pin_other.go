// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux && !darwin

package fsops

import (
	"errors"
	"os"
	"time"
)

// PinParent is not available on this platform.
func PinParent(root *os.Root, p string) (*os.Root, string, error) {
	return nil, "", errors.ErrUnsupported
}

// ChmodNoFollow is not available on this platform.
func ChmodNoFollow(dir *os.Root, base string, m os.FileMode) error {
	return errors.ErrUnsupported
}

// OpenNoFollow is not available on this platform.
func OpenNoFollow(dir *os.Root, base string, flag int) (*os.File, error) {
	return nil, errors.ErrUnsupported
}

func openNoFollow(dir *os.Root, base string, flag int, perm os.FileMode) (*os.File, error) {
	return nil, errors.ErrUnsupported
}

func rootOf(f *os.File) (*os.Root, error) { return nil, errors.ErrUnsupported }

func chtimesFile(f *os.File, t time.Time) error { return errors.ErrUnsupported }
