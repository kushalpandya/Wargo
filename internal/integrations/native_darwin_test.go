// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package integrations

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// binaryPlist writes xml as a binary plist named rel in a temp dir, the
// way macOS stores preferences.
func binaryPlist(t *testing.T, rel, xml string) string {
	t.Helper()
	root := t.TempDir()
	p := filepath.Join(root, rel)
	if err := os.WriteFile(p, []byte(xml), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/plutil", "-convert", "binary1", p).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v %s", err, out)
	}
	return root
}

func TestReadNativeConfTransmissionPlist(t *testing.T) {
	root := binaryPlist(t, "org.m0k.transmission.plist", `<?xml version="1.0"?><plist version="1.0"><dict><key>RPC</key><true/><key>RPCPort</key><integer>9093</integer></dict></plist>`)
	c, ok := readNativeConf(nativeLoc{App: "transmission", Root: root, Rel: "org.m0k.transmission.plist", Format: "transmission-plist"})
	if !ok || c.Port != 9093 || c.RPCPath != "/transmission/rpc" {
		t.Fatalf("conf = %+v ok %v", c, ok)
	}
	// Transmission.app leaves RPCPort out until the user changes it.
	root = binaryPlist(t, "org.m0k.transmission.plist", `<?xml version="1.0"?><plist version="1.0"><dict><key>BindPort</key><integer>51413</integer></dict></plist>`)
	if c, ok := readNativeConf(nativeLoc{App: "transmission", Root: root, Rel: "org.m0k.transmission.plist", Format: "transmission-plist"}); !ok || c.Port != 9091 {
		t.Fatalf("default conf = %+v ok %v", c, ok)
	}
}

func TestParsePlexSecretFromPlist(t *testing.T) {
	root := binaryPlist(t, "com.plexapp.plexmediaserver.plist", `<?xml version="1.0"?><plist version="1.0"><dict><key>FriendlyName</key><string>mac</string><key>PlexOnlineToken</key><string>AbCdEfGhIjKlMnOpQrSt</string></dict></plist>`)
	tok, err := parsePlexSecretFile(t, root)
	if err != nil || tok != "AbCdEfGhIjKlMnOpQrSt" {
		t.Fatalf("token %q err %v", tok, err)
	}
	root = binaryPlist(t, "com.plexapp.plexmediaserver.plist", `<?xml version="1.0"?><plist version="1.0"><dict><key>FriendlyName</key><string>mac</string></dict></plist>`)
	if _, err := parsePlexSecretFile(t, root); err == nil {
		t.Fatal("a plist without a token must fail")
	}
}

func parsePlexSecretFile(t *testing.T, root string) (string, error) {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "com.plexapp.plexmediaserver.plist"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return parsePlexSecret("com.plexapp.plexmediaserver.plist", f)
}
