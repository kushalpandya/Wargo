// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin

package files

func platformPrivacyBlocked(error, string, string) bool { return false }

func platformPrivacyReadBlocked(error, string, string) bool { return false }
