// SPDX-License-Identifier: AGPL-3.0-only

package term

// ring is a fixed-size byte ring keeping the most recent output of a
// session so a reattaching browser can redraw its scrollback.
// Not safe for concurrent use; the session lock guards it.
type ring struct {
	buf  []byte
	pos  int // next write index
	full bool
}

func newRing(size int) *ring { return &ring{buf: make([]byte, size)} }

func (r *ring) Write(p []byte) {
	n := len(r.buf)
	if len(p) >= n {
		copy(r.buf, p[len(p)-n:])
		r.pos, r.full = 0, true
		return
	}
	c := copy(r.buf[r.pos:], p)
	if c < len(p) {
		copy(r.buf, p[c:])
		r.full = true
	}
	r.pos = (r.pos + len(p)) % n
	if r.pos == 0 && len(p) > 0 {
		r.full = true
	}
}

// Len is the number of buffered bytes.
func (r *ring) Len() int {
	if r.full {
		return len(r.buf)
	}
	return r.pos
}

// Bytes returns a copy of the buffered data, oldest first.
func (r *ring) Bytes() []byte {
	if !r.full {
		return append([]byte(nil), r.buf[:r.pos]...)
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.pos:]...)
	return append(out, r.buf[:r.pos]...)
}

func (r *ring) Reset() { r.pos, r.full = 0, false }
