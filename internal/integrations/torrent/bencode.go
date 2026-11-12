// SPDX-License-Identifier: AGPL-3.0-only

// Package torrent parses .torrent files (bencode) with strict limits and
// builds magnet links from them.
package torrent

import (
	"errors"
	"fmt"
	"strconv"
)

const (
	// MaxSize is the largest .torrent file accepted.
	MaxSize = 10 << 20
	// MaxDepth limits list/dict nesting.
	MaxDepth = 64
	// maxIntDigits bounds integer literals (int64 fits in 19 digits + sign).
	maxIntDigits = 20
)

var (
	ErrTooLarge      = errors.New("bencode: input too large")
	ErrTooDeep       = errors.New("bencode: nesting too deep")
	errTooManyValues = errors.New("bencode: too many values")
)

// SyntaxError reports malformed input at a byte offset.
type SyntaxError struct {
	Offset int
	Msg    string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("bencode: %s at offset %d", e.Msg, e.Offset) }

// Dict is a decoded dictionary. Byte strings are returned as []byte, integers
// as int64, lists as []any and dictionaries as Dict.
type Dict map[string]any

type decoder struct {
	buf   []byte
	pos   int
	depth int
	count int
	// span of the value stored under the top-level "info" key
	infoStart, infoEnd int
}

// maxValues bounds how many values one document may contain.
const maxValues = 1_000_000

func (d *decoder) fail(msg string) error { return &SyntaxError{Offset: d.pos, Msg: msg} }

// Decode parses one bencoded value occupying all of b.
func Decode(b []byte) (any, error) {
	v, _, err := decode(b)
	return v, err
}

func decode(b []byte) (any, *decoder, error) {
	if len(b) > MaxSize {
		return nil, nil, ErrTooLarge
	}
	d := &decoder{buf: b, infoStart: -1}
	v, err := d.value()
	if err != nil {
		return nil, nil, err
	}
	if d.pos != len(b) {
		return nil, nil, d.fail("trailing data")
	}
	return v, d, nil
}

func (d *decoder) value() (any, error) {
	d.count++
	if d.count > maxValues {
		return nil, errTooManyValues
	}
	if d.pos >= len(d.buf) {
		return nil, d.fail("unexpected end of input")
	}
	switch c := d.buf[d.pos]; {
	case c == 'i':
		return d.integer()
	case c >= '0' && c <= '9':
		return d.bytes()
	case c == 'l':
		return d.list()
	case c == 'd':
		return d.dict()
	default:
		return nil, d.fail(fmt.Sprintf("unexpected byte %q", c))
	}
}

func (d *decoder) integer() (int64, error) {
	d.pos++ // 'i'
	start := d.pos
	for d.pos < len(d.buf) && d.buf[d.pos] != 'e' {
		if d.pos-start > maxIntDigits {
			return 0, d.fail("integer too long")
		}
		d.pos++
	}
	if d.pos >= len(d.buf) {
		return 0, d.fail("unterminated integer")
	}
	s := string(d.buf[start:d.pos])
	if s == "" || s == "-" || s == "-0" || (len(s) > 1 && s[0] == '0') || (len(s) > 2 && s[0] == '-' && s[1] == '0') {
		return 0, d.fail("invalid integer")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, d.fail("invalid integer")
	}
	d.pos++ // 'e'
	return n, nil
}

func (d *decoder) bytes() ([]byte, error) {
	start := d.pos
	n := 0
	for d.pos < len(d.buf) && d.buf[d.pos] != ':' {
		c := d.buf[d.pos]
		if c < '0' || c > '9' || d.pos-start >= 9 {
			return nil, d.fail("invalid string length")
		}
		n = n*10 + int(c-'0')
		d.pos++
	}
	if d.pos >= len(d.buf) {
		return nil, d.fail("unterminated string length")
	}
	if d.pos-start > 1 && d.buf[start] == '0' {
		return nil, d.fail("invalid string length")
	}
	d.pos++ // ':'
	if n > len(d.buf)-d.pos {
		return nil, d.fail("string exceeds input")
	}
	s := d.buf[d.pos : d.pos+n]
	d.pos += n
	return s, nil
}

func (d *decoder) enter() error {
	d.depth++
	if d.depth > MaxDepth {
		return ErrTooDeep
	}
	return nil
}

func (d *decoder) list() ([]any, error) {
	if err := d.enter(); err != nil {
		return nil, err
	}
	d.pos++ // 'l'
	out := []any{}
	for {
		if d.pos >= len(d.buf) {
			return nil, d.fail("unterminated list")
		}
		if d.buf[d.pos] == 'e' {
			d.pos++
			d.depth--
			return out, nil
		}
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

func (d *decoder) dict() (Dict, error) {
	if err := d.enter(); err != nil {
		return nil, err
	}
	d.pos++ // 'd'
	out := Dict{}
	for {
		if d.pos >= len(d.buf) {
			return nil, d.fail("unterminated dictionary")
		}
		if d.buf[d.pos] == 'e' {
			d.pos++
			d.depth--
			return out, nil
		}
		if c := d.buf[d.pos]; c < '0' || c > '9' {
			return nil, d.fail("dictionary key must be a string")
		}
		k, err := d.bytes()
		if err != nil {
			return nil, err
		}
		key := string(k)
		if _, dup := out[key]; dup {
			return nil, d.fail("duplicate dictionary key")
		}
		start := d.pos
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		if d.depth == 1 && key == "info" {
			d.infoStart, d.infoEnd = start, d.pos
		}
		out[key] = v
	}
}
