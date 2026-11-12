// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux && !darwin

package host

// LoginShell is not implemented on this platform.
func LoginShell(name string, uid int) string { return "" }
