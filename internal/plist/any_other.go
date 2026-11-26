// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin

package plist

import "errors"

func binaryToXML([]byte) ([]byte, error) {
	return nil, errors.New("plist: binary property lists can only be read on macOS")
}
