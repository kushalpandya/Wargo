// SPDX-License-Identifier: AGPL-3.0-only

package plist

import (
	"bytes"
	"io"
)

// DecodeAny parses a property list in XML or binary form, such as a macOS
// preferences file. Binary plists are converted with plutil, which only
// macOS has; elsewhere they are an error.
func DecodeAny(r io.Reader) (any, error) {
	b, err := io.ReadAll(io.LimitReader(r, plistMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > plistMaxBytes {
		return nil, errPlistTooLarge
	}
	if bytes.HasPrefix(b, []byte("bplist")) {
		if b, err = binaryToXML(b); err != nil {
			return nil, err
		}
	}
	return Decode(bytes.NewReader(b))
}
