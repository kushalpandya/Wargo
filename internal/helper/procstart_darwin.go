// SPDX-License-Identifier: AGPL-3.0-only

package helper

import "errors"

// procStartTime is a build-only stub; Task 6 replaces it with the real
// darwin implementation.
func procStartTime(pid int) (uint64, error) { return 0, errors.ErrUnsupported }
