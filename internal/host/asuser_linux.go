// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package host

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// ErrNotRoot is returned by AsUser in container mode when Wargo isn't root
// (it then cannot switch identities and refuses to act at all).
var ErrNotRoot = errors.New("container mode requires running as root inside the container")

// AsUser runs fn with the calling thread's filesystem identity (fsuid,
// fsgid and supplementary groups) switched to the admin account, so file
// permissions are exactly those of the user (container mode). Natively
// Wargo already is that user and fn runs unchanged.
//
// Only the current OS thread is affected: the goroutine is locked to it,
// identity changes use raw per-thread syscalls (not syscall.Setuid & co,
// which apply to every thread), and the identity is restored afterwards.
// If restoring fails the thread stays locked, so the runtime destroys it
// when the goroutine exits instead of reusing it. fn must do its file
// work on the calling goroutine (goroutines it starts run as root).
func AsUser(fn func() error) error {
	if !Container() {
		return fn()
	}
	a := Admin()
	if a == nil {
		return errors.New("container mode: admin account is not configured")
	}
	if os.Geteuid() != 0 {
		return ErrNotRoot
	}
	runtime.LockOSThread()
	if cur := getfsuid(); cur == a.UID && cur != 0 {
		// Already switched (nested call on the same goroutine).
		runtime.UnlockOSThread()
		return fn()
	}
	orig, err := syscall.Getgroups()
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	if err := switchTo(a.UID, a.GID, a.Groups); err != nil {
		if rerr := restore(orig); rerr != nil {
			// Thread state unknown: keep it locked so it dies with us.
			return fmt.Errorf("switch identity: %v (restore failed: %v)", err, rerr)
		}
		runtime.UnlockOSThread()
		return fmt.Errorf("switch identity: %w", err)
	}
	var ferr error
	func() {
		defer func() {
			if v := recover(); v != nil {
				if rerr := restore(orig); rerr != nil {
					panic(fmt.Sprintf("%v (and restoring the thread identity failed: %v)", v, rerr))
				}
				runtime.UnlockOSThread()
				panic(v)
			}
		}()
		ferr = fn()
	}()
	if err := restore(orig); err != nil {
		// Leave the thread locked: it is terminated when this goroutine
		// exits rather than being handed to other goroutines.
		return fmt.Errorf("restore identity: %w", err)
	}
	runtime.UnlockOSThread()
	return ferr
}

// AsRoot runs fn with full root privileges (container mode) even when
// called from inside AsUser: it runs on a fresh goroutine, which never
// shares the caller's locked thread (the runtime also never clones new
// threads from a locked one). Natively it simply calls fn.
func AsRoot(fn func() error) error {
	if !Container() {
		return fn()
	}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				done <- fmt.Errorf("panic: %v", v)
			}
		}()
		if os.Geteuid() == 0 && getfsuid() != 0 {
			done <- errors.New("unexpected filesystem identity on a fresh thread")
			return
		}
		done <- fn()
	}()
	return <-done
}

func switchTo(uid, gid int, groups []int) error {
	gs := make([]uint32, 0, len(groups))
	for _, g := range groups {
		gs = append(gs, uint32(g))
	}
	if err := setgroups(gs); err != nil {
		return err
	}
	setfsgid(gid)
	if getfsgid() != gid {
		return errors.New("setfsgid did not take effect")
	}
	setfsuid(uid)
	if getfsuid() != uid {
		return errors.New("setfsuid did not take effect")
	}
	return nil
}

func restore(orig []int) error {
	setfsuid(0)
	if getfsuid() != 0 {
		return errors.New("setfsuid(0) did not take effect")
	}
	setfsgid(0)
	if getfsgid() != 0 {
		return errors.New("setfsgid(0) did not take effect")
	}
	gs := make([]uint32, len(orig))
	for i, g := range orig {
		gs[i] = uint32(g)
	}
	return setgroups(gs)
}

func setgroups(gs []uint32) error {
	var p unsafe.Pointer
	if len(gs) > 0 {
		p = unsafe.Pointer(&gs[0])
	}
	_, _, e := syscall.RawSyscall(sysSetgroups, uintptr(len(gs)), uintptr(p), 0)
	if e != 0 {
		return e
	}
	return nil
}

// setfsuid returns the previous fsuid; it has no error return.
func setfsuid(uid int) int {
	r, _, _ := syscall.RawSyscall(sysSetfsuid, uintptr(uint32(uid)), 0, 0)
	return int(uint32(r))
}

func setfsgid(gid int) int {
	r, _, _ := syscall.RawSyscall(sysSetfsgid, uintptr(uint32(gid)), 0, 0)
	return int(uint32(r))
}

// getfsuid queries the current fsuid: setfsuid with an invalid id changes
// nothing and returns the current value.
func getfsuid() int { return setfsuid(-1) }
func getfsgid() int { return setfsgid(-1) }
