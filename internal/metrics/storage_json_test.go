// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"encoding/json"
	"testing"
)

// With nothing to show (a plain container), both lists encode as [] so the
// UI can read their lengths.
func TestStorageEncodesEmptyListsNotNull(t *testing.T) {
	b, err := json.Marshal(newStorage(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"drives":[],"filesystems":[]}`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
