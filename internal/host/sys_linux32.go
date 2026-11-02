// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux && (386 || arm)

package host

import "syscall"

// 32-bit uid/gid variants (the plain syscalls take 16-bit ids here).
const (
	sysSetgroups = syscall.SYS_SETGROUPS32
	sysSetfsuid  = syscall.SYS_SETFSUID32
	sysSetfsgid  = syscall.SYS_SETFSGID32
)
