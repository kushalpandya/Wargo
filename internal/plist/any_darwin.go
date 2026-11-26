// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package plist

import (
	"bytes"
	"context"
	"time"

	"github.com/kushalpandya/wargo/internal/host"
)

// binaryToXML converts a binary plist with plutil, passing the data on
// stdin so plutil never opens a file itself.
func binaryToXML(b []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := host.Command(ctx, "/usr/bin/plutil", "-convert", "xml1", "-o", "-", "-")
	cmd.Stdin = bytes.NewReader(b)
	return cmd.Output()
}
