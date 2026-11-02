// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux && !386 && !arm

package host

import "syscall"

const (
	sysSetgroups = syscall.SYS_SETGROUPS
	sysSetfsuid  = syscall.SYS_SETFSUID
	sysSetfsgid  = syscall.SYS_SETFSGID
)
