// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package files

import (
	"io/fs"
	"os"
	"syscall"
	"testing"
)

func fakeLstat(t *testing.T, f func(string) (fs.FileInfo, error)) {
	old := lstat
	lstat = f
	t.Cleanup(func() { lstat = old })
}

func TestEACCESStillElevates(t *testing.T) {
	fakeLstat(t, func(string) (fs.FileInfo, error) { return nil, syscall.EPERM })
	if platformPrivacyBlocked(syscall.EACCES, "/Users/k", "/Users/k/Desktop/x") {
		t.Error("EACCES must keep the sudo path")
	}
	if platformPrivacyBlocked(syscall.EPERM, "/Users/k", "/Users/k/Code/x") {
		t.Error("EPERM outside protected folders must keep the sudo path")
	}
}

func TestEPERMWithReadableTargetElevates(t *testing.T) {
	// chown/chmod/unlink by a non-owner fail with EPERM although Lstat works.
	fakeLstat(t, func(string) (fs.FileInfo, error) { return os.Lstat("/") })
	if platformPrivacyBlocked(syscall.EPERM, "/Users/k", "/Users/k/Downloads/x") {
		t.Error("EPERM with a readable target must keep the sudo path")
	}
}

func TestEPERMWithLstatEPERMIsFullDiskAccess(t *testing.T) {
	fakeLstat(t, func(string) (fs.FileInfo, error) { return nil, &fs.PathError{Op: "lstat", Err: syscall.EPERM} })
	if !platformPrivacyBlocked(syscall.EPERM, "/Users/k", "/Users/k/Desktop/x") {
		t.Error("want full disk access")
	}
}

func TestMissingTargetChecksParent(t *testing.T) {
	fakeLstat(t, func(p string) (fs.FileInfo, error) {
		if p == "/Users/k/Desktop" {
			return nil, syscall.EPERM
		}
		return nil, syscall.ENOENT
	})
	if !platformPrivacyBlocked(syscall.EPERM, "/Users/k", "/Users/k/Desktop/new") {
		t.Error("parent EPERM must count")
	}
	fakeLstat(t, func(p string) (fs.FileInfo, error) {
		if p == "/Users/k/Desktop" {
			return os.Lstat("/")
		}
		return nil, syscall.ENOENT
	})
	if platformPrivacyBlocked(syscall.EPERM, "/Users/k", "/Users/k/Desktop/new") {
		t.Error("readable parent must keep the sudo path")
	}
}

func TestReadOfProtectedFolderItselfIsBlocked(t *testing.T) {
	// Lstat of ~/Desktop works under TCC, but listing it fails with EPERM.
	fakeLstat(t, func(string) (fs.FileInfo, error) { return os.Lstat("/") })
	if !platformPrivacyReadBlocked(syscall.EPERM, "/Users/k", "/Users/k/Desktop") {
		t.Error("a read of the protected folder itself must be blocked")
	}
	if platformPrivacyReadBlocked(syscall.EACCES, "/Users/k", "/Users/k/Desktop") {
		t.Error("EACCES is an ordinary permission problem")
	}
	if platformPrivacyReadBlocked(syscall.EPERM, "/Users/k", "/Users/k/Code") {
		t.Error("outside protected folders")
	}
	if platformPrivacyBlocked(syscall.EPERM, "/Users/k", "/Users/k/Desktop") {
		t.Error("a mutation with a readable target must still elevate")
	}
}
