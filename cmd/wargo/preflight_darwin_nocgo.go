// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin && !cgo

package main

import "errors"

func platformPreflight() error {
	return errors.New("this macOS build has no cgo: build with CGO_ENABLED=1 (Directory Services, PAM and process details need it)")
}
