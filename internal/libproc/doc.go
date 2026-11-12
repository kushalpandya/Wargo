// SPDX-License-Identifier: AGPL-3.0-only

// Package libproc reads macOS process data through libproc and sysctl.
// The full API needs cgo; without cgo, PIDs, Get, StartTime and ArgMax work
// from sysctl alone and the rest return errors.ErrUnsupported.
package libproc
