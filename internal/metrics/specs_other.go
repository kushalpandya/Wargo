// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin

package metrics

func platformOSRelease() (pretty, id string, ok bool) { return "", "", false }

func applyPlatformSpecs(*Specs) {}

func platformPackages() []PackageCount { return nil }
