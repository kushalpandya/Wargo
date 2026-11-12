// SPDX-License-Identifier: AGPL-3.0-only

package term

import (
	"context"
	"testing"
	"time"
)

func TestParseLoginPath(t *testing.T) {
	if p, ok := parseLoginPath([]byte("Welcome!\n\n__WARGO_PATH__=/opt/homebrew/bin:/usr/bin\nbye\n")); !ok || p != "/opt/homebrew/bin:/usr/bin" {
		t.Fatal(p)
	}
	for _, bad := range []string{"__WARGO_PATH__=bin:/usr/bin\n", "__WARGO_PATH__=/a\x1b[0m:/b\n", "no marker\n"} {
		if _, ok := parseLoginPath([]byte(bad)); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestLoginPathIgnoresNoiseAndTimeout(t *testing.T) {
	a := account{User: "u", Home: "/", Shell: "/bin/zsh"}
	noisy := func(ctx context.Context, argv, env []string) ([]byte, error) {
		return []byte("oh-my-zsh update?\n__WARGO_PATH__=/x/bin:/usr/bin\n"), nil
	}
	if p := resolveLoginPath(context.Background(), a, noisy); p != "/x/bin:/usr/bin" {
		t.Fatal(p)
	}
	hang := func(ctx context.Context, argv, env []string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
	start := time.Now()
	if p := resolveLoginPath(context.Background(), a, hang); p != "" || time.Since(start) > 4*time.Second {
		t.Fatal(p)
	}
}

func TestLookIn(t *testing.T) {
	if p, err := lookIn("/nonexistent:/bin:/usr/bin", "sh"); err != nil || (p != "/bin/sh" && p != "/usr/bin/sh") {
		t.Fatal(p, err)
	}
	if _, err := lookIn("/nonexistent", "sh"); err == nil {
		t.Fatal("found")
	}
}

func TestRunLoginRealShell(t *testing.T) {
	a := account{User: "u", Home: "/", Shell: "/bin/sh"}
	if p := resolveLoginPath(context.Background(), a, runLogin); p == "" {
		t.Fatal("no path")
	}
}
