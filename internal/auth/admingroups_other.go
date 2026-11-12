// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin

package auth

// adminGroups lists the groups that may hold the Wargo admin, in order of
// preference.
var adminGroups = []string{"wheel", "sudo", "admin"}
