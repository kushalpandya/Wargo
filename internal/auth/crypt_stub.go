// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux || !cgo || !wargo_container

package auth

const cryptAvailable = false

func cryptFunc(password, setting string) (string, error) { return "", errNoCrypt }

func cryptGensalt(prefix string) (string, error) { return "", errNoCrypt }
