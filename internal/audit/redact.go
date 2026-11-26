// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Masking happens here, for every entry, so a caller that passes a URL
// with credentials or a command's output can't leak a secret into the
// log by accident.

const redacted = "[redacted]"

var redactRules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// user:password@ in URLs.
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@"']+:[^/\s@"']+@`), "${1}" + redacted + "@"},
	// Secret-looking query, form and command-line values (--password=x).
	{regexp.MustCompile(`(?i)((?:^|[?&;\s"'-])(?:x-plex-token|access_token|refresh_token|token|api_?key|apikey|password|passwd|pass|secret|client_secret)=)[^&\s"']+`), "${1}" + redacted},
	// Authorization headers and bearer tokens.
	{regexp.MustCompile(`(?i)(authorization:\s*)[^\r\n"]+`), "${1}" + redacted},
	{regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]+`), "${1}" + redacted},
	// Header-style API keys.
	{regexp.MustCompile(`(?i)\b(x-(?:api-key|plex-token|transmission-session-id):\s*)[^\s"]+`), "${1}" + redacted},
	// JSON-like "password": "..." inside free text.
	{regexp.MustCompile(`(?i)("(?:password|passwd|token|api_?key|apikey|secret)"\s*:\s*")[^"]*`), "${1}" + redacted},
	// Secret command-line flags given as a separate argument
	// (cloudflared tunnel run --token eyJ...).
	{regexp.MustCompile(`(?i)((?:^|\s)--?(?:token|password|passwd|secret|api-?key|client-secret)\s+)\S+`), "${1}" + redacted},
	// Wargo's own agent tokens, wherever they appear.
	{regexp.MustCompile(`\bwargo_[A-Za-z0-9_-]{20,}`), "wargo_" + redacted},
}

// Redact masks credentials in s.
func Redact(s string) string {
	if s == "" {
		return s
	}
	for _, r := range redactRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// secretKey matches details fields that hold a secret themselves, also as
// the end of a longer name (accessToken, clientSecret, privateKey). Flags
// such as passwordChanged are booleans and kept.
var secretKey = regexp.MustCompile(`(?i)(password|passwd|secret|token|api_?key|apikey|authorization|credentials?|assertion|private_?key|cookie)$`)

// redactValue masks secret fields and credentials in decoded JSON.
func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if _, isBool := x.(bool); secretKey.MatchString(k) && !isBool && x != nil {
				t[k] = redacted
				continue
			}
			t[k] = redactValue(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = redactValue(x)
		}
		return t
	case string:
		return Redact(t)
	}
	return v
}

// redactJSON masks secrets in an encoded details value. Numbers are kept
// as written (no float64 round trip for inode numbers and the like).
func redactJSON(b []byte) []byte {
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return []byte(Redact(string(b)))
	}
	out, err := json.Marshal(redactValue(v))
	if err != nil {
		return []byte(Redact(string(b)))
	}
	return out
}

// redactLines masks secrets in every line.
func redactLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = Redact(strings.TrimRight(l, "\r"))
	}
	return out
}
