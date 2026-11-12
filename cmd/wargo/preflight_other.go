// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin || cgo

package main

func platformPreflight() error { return nil }
