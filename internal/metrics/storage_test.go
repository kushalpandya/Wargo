// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hostfs "github.com/kushalpandya/wargo/internal/host"
)

// A disk with a filesystem directly on it (no partition table) must report
// an empty partition list, not null: the System page maps over it.
func TestDriveWithoutPartitionsHasEmptyList(t *testing.T) {
	root := t.TempDir()
	disk := filepath.Join(root, "sys/block/vdc")
	if err := os.MkdirAll(disk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(disk, "size"), []byte("39364\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer hostfs.SetRoot(root)()

	ds := drives(nil, nil)
	if len(ds) != 1 || ds[0].Name != "vdc" {
		t.Fatalf("drives = %+v, want one drive vdc", ds)
	}
	b, err := json.Marshal(ds[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"partitions":[]`) {
		t.Fatalf("drive JSON %s, want \"partitions\":[]", b)
	}
}
