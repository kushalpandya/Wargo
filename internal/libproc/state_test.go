// SPDX-License-Identifier: AGPL-3.0-only

package libproc

import "testing"

func TestStateLetter(t *testing.T) {
	if StateLetter(2) != "R" || StateLetter(5) != "Z" || StateLetter(99) != "?" {
		t.Fatal()
	}
}
