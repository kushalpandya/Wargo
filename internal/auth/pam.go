// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux && cgo

package auth

import (
	"errors"

	"github.com/msteinert/pam/v2"
)

// pamAuthenticate verifies user/password through the given PAM service and
// runs account management (expired / locked accounts fail).
func pamAuthenticate(service, user, password string) error {
	t, err := pam.StartFunc(service, user, func(s pam.Style, msg string) (string, error) {
		switch s {
		case pam.PromptEchoOff, pam.PromptEchoOn:
			return password, nil
		case pam.ErrorMsg, pam.TextInfo:
			return "", nil
		}
		return "", errors.New("unsupported PAM conversation")
	})
	if err != nil {
		return err
	}
	defer t.End()
	if err := t.Authenticate(pam.DisallowNullAuthtok); err != nil {
		return err
	}
	return t.AcctMgmt(pam.Silent)
}

const pamAvailable = true
