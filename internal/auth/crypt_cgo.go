// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux && cgo && wargo_container

// libxcrypt is only needed for container mode (host /etc/shadow checks),
// so it is linked only with -tags wargo_container (the Docker image).
// Native binaries then depend on libpam.so.0 and libc only: distributions
// disagree on libcrypt's soname (Arch ships only libcrypt.so.2).

package auth

/*
#cgo LDFLAGS: -lcrypt
#include <crypt.h>
#include <stdlib.h>
#include <string.h>

static char *wargo_crypt(const char *phrase, const char *setting, void *data) {
	return crypt_r(phrase, setting, (struct crypt_data *)data);
}

static char *wargo_gensalt(const char *prefix, char *buf, int size) {
	return crypt_gensalt_rn(prefix, 0, NULL, 0, buf, size);
}
*/
import "C"

import (
	"unsafe"
)

const cryptAvailable = true

// cryptFunc hashes password with the method and salt in setting using
// libxcrypt's reentrant crypt_r. The password copy in C memory is wiped.
func cryptFunc(password, setting string) (string, error) {
	data := C.calloc(1, C.size_t(unsafe.Sizeof(C.struct_crypt_data{})))
	if data == nil {
		return "", errNoCrypt
	}
	defer C.free(data)
	pw := C.CString(password)
	defer func() {
		C.memset(unsafe.Pointer(pw), 0, C.size_t(len(password)))
		C.free(unsafe.Pointer(pw))
	}()
	st := C.CString(setting)
	defer C.free(unsafe.Pointer(st))
	out := C.wargo_crypt(pw, st, data)
	if out == nil {
		return "", errBadHash
	}
	res := C.GoString(out)
	C.memset(data, 0, C.size_t(unsafe.Sizeof(C.struct_crypt_data{})))
	return res, nil
}

// cryptGensalt returns a fresh setting string for prefix ("$y$", "$6$"...).
// Used by tests to create hashes the way passwd(1) would.
func cryptGensalt(prefix string) (string, error) {
	buf := (*C.char)(C.calloc(1, C.CRYPT_GENSALT_OUTPUT_SIZE))
	defer C.free(unsafe.Pointer(buf))
	p := C.CString(prefix)
	defer C.free(unsafe.Pointer(p))
	if C.wargo_gensalt(p, buf, C.CRYPT_GENSALT_OUTPUT_SIZE) == nil {
		return "", errBadHash
	}
	return C.GoString(buf), nil
}
