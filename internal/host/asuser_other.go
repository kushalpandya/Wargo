// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package host

import "errors"

var ErrNotRoot = errors.New("container mode is only supported on Linux")

func AsUser(fn func() error) error {
	if Container() {
		return ErrNotRoot
	}
	return fn()
}

func AsRoot(fn func() error) error { return fn() }
