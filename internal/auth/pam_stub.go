// SPDX-License-Identifier: AGPL-3.0-only

//go:build !(linux || darwin) || !cgo

package auth

import "errors"

func pamAuthenticate(service, user, password string) error {
	return errors.New("wargo was built without PAM support")
}

const pamAvailable = false
