// SPDX-License-Identifier: AGPL-3.0-only

package torrent

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// MaxTrackers bounds the number of trackers put into a magnet link.
const MaxTrackers = 30

// Meta is the subset of a .torrent's metadata Wargo needs.
type Meta struct {
	Name string
	// Length is the total payload size in bytes (0 if unknown).
	Length int64
	// InfoHash is the hex SHA-1 of the bencoded info dict (BitTorrent v1).
	// Empty for v2-only torrents.
	InfoHash string
	// InfoHashV2 is the hex SHA-256 of the info dict for v2/hybrid torrents.
	InfoHashV2 string
	Trackers   []string
	Private    bool
}

// Parse decodes a .torrent file.
func Parse(b []byte) (*Meta, error) {
	v, d, err := decode(b)
	if err != nil {
		return nil, err
	}
	root, ok := v.(Dict)
	if !ok {
		return nil, errors.New("torrent: top level is not a dictionary")
	}
	info, ok := root["info"].(Dict)
	if !ok || d.infoStart < 0 {
		return nil, errors.New("torrent: missing info dictionary")
	}
	raw := b[d.infoStart:d.infoEnd]
	m := &Meta{}
	if name, ok := info["name.utf-8"].([]byte); ok {
		m.Name = string(name)
	} else if name, ok := info["name"].([]byte); ok {
		m.Name = string(name)
	}
	version, _ := info["meta version"].(int64)
	_, hasPieces := info["pieces"]
	_, hasTree := info["file tree"]
	if hasPieces {
		sum := sha1.Sum(raw)
		m.InfoHash = hex.EncodeToString(sum[:])
	}
	if version >= 2 && hasTree {
		sum := sha256.Sum256(raw)
		m.InfoHashV2 = hex.EncodeToString(sum[:])
	}
	if p, _ := info["private"].(int64); p == 1 {
		m.Private = true
	}
	switch {
	case info["length"] != nil:
		m.Length, _ = info["length"].(int64)
	case info["files"] != nil:
		files, _ := info["files"].([]any)
		for _, f := range files {
			if fd, ok := f.(Dict); ok {
				if n, ok := fd["length"].(int64); ok && n > 0 {
					m.Length += n
				}
			}
		}
	case info["file tree"] != nil:
		m.Length = treeLength(info["file tree"], 0)
	}
	if m.InfoHash == "" && m.InfoHashV2 == "" {
		return nil, errors.New("torrent: cannot determine info hash")
	}

	seen := map[string]bool{}
	add := func(v any) {
		s, ok := v.([]byte)
		if !ok {
			return
		}
		t := strings.TrimSpace(string(s))
		if t == "" || seen[t] || len(m.Trackers) >= MaxTrackers {
			return
		}
		if u, err := url.Parse(t); err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "udp" && u.Scheme != "wss") {
			return
		}
		seen[t] = true
		m.Trackers = append(m.Trackers, t)
	}
	add(root["announce"])
	if tiers, ok := root["announce-list"].([]any); ok {
		for _, tier := range tiers {
			if l, ok := tier.([]any); ok {
				for _, t := range l {
					add(t)
				}
			}
		}
	}
	return m, nil
}

func treeLength(v any, depth int) int64 {
	d, ok := v.(Dict)
	if !ok || depth > MaxDepth {
		return 0
	}
	var n int64
	for k, child := range d {
		if k == "" {
			if cd, ok := child.(Dict); ok {
				if l, ok := cd["length"].(int64); ok && l > 0 {
					n += l
				}
			}
			continue
		}
		n += treeLength(child, depth+1)
	}
	return n
}

// Magnet returns a magnet link for m (xt, dn, xl and tr parameters).
func (m *Meta) Magnet() string {
	return BuildMagnet(m.InfoHash, m.InfoHashV2, m.Name, m.Length, m.Trackers)
}

// BuildMagnet assembles a magnet URI. infoHash is the v1 hex hash (may be
// empty for v2-only torrents, in which case v2 is used via btmh).
func BuildMagnet(infoHash, v2 string, name string, length int64, trackers []string) string {
	var b strings.Builder
	b.WriteString("magnet:?")
	sep := ""
	param := func(k, v string) {
		b.WriteString(sep)
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		sep = "&"
	}
	if infoHash != "" {
		param("xt", "urn:btih:"+strings.ToLower(infoHash))
	}
	if v2 != "" {
		// multihash: 0x12 (sha2-256), 0x20 (32 bytes)
		param("xt", "urn:btmh:1220"+strings.ToLower(v2))
	}
	if name != "" {
		param("dn", url.QueryEscape(name))
	}
	if length > 0 {
		param("xl", strconv.FormatInt(length, 10))
	}
	n := 0
	for _, t := range trackers {
		if n >= MaxTrackers {
			break
		}
		param("tr", url.QueryEscape(t))
		n++
	}
	return b.String()
}

// MagnetInfoHash extracts the v1 info hash (lower-case hex) from a magnet
// link, accepting both hex and base32 encodings. Returns "" if absent.
func MagnetInfoHash(magnet string) string {
	if !strings.HasPrefix(strings.ToLower(magnet), "magnet:?") {
		return ""
	}
	q, err := url.ParseQuery(magnet[len("magnet:?"):])
	if err != nil {
		return ""
	}
	for _, xt := range q["xt"] {
		if !strings.HasPrefix(strings.ToLower(xt), "urn:btih:") {
			continue
		}
		h := xt[len("urn:btih:"):]
		return NormalizeHash(h)
	}
	return ""
}

// NormalizeHash returns h as lower-case hex if it is a valid 40-char hex or
// 32-char base32 v1 info hash, else "".
func NormalizeHash(h string) string {
	switch len(h) {
	case 40:
		if _, err := hex.DecodeString(h); err == nil {
			return strings.ToLower(h)
		}
	case 32:
		if b, err := base32.StdEncoding.DecodeString(strings.ToUpper(h)); err == nil && len(b) == 20 {
			return hex.EncodeToString(b)
		}
	}
	return ""
}
