// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/kushalpandya/wargo/internal/httpx"
)

const (
	maxLineLen  = 16 << 10
	maxFrameLen = 1 << 20
)

// LogLine is one line of container output.
type LogLine struct {
	TS     string `json:"ts,omitempty"`
	Stream string `json:"stream"` // "out" | "err"
	Text   string `json:"text"`
}

// LineReader turns a Docker log stream into lines. For non-TTY containers
// the stream is multiplexed: 8-byte headers [stream, 0, 0, 0, size(BE32)]
// followed by size bytes of payload.
type LineReader struct {
	r          *bufio.Reader
	mux        bool
	timestamps bool
	partial    [3][]byte
	queue      []LogLine
	frame      []byte
}

func NewLineReader(r io.Reader, tty, timestamps bool) *LineReader {
	return &LineReader{r: bufio.NewReaderSize(r, 32<<10), mux: !tty, timestamps: timestamps}
}

func streamName(s byte) string {
	if s == 2 {
		return "err"
	}
	return "out"
}

func (l *LineReader) emit(stream byte, line []byte) {
	line = bytes.TrimRight(line, "\r")
	if len(line) > maxLineLen {
		line = line[:maxLineLen]
	}
	ll := LogLine{Stream: streamName(stream)}
	text := strings.ToValidUTF8(string(line), "\uFFFD")
	if l.timestamps {
		if ts, rest, ok := strings.Cut(text, " "); ok && len(ts) >= 20 && ts[4] == '-' {
			ll.TS, text = ts, rest
		}
	}
	ll.Text = text
	l.queue = append(l.queue, ll)
}

// feed splits payload for stream into complete lines; the remainder is kept.
func (l *LineReader) feed(stream byte, p []byte) {
	if stream > 2 {
		stream = 1
	}
	buf := append(l.partial[stream], p...)
	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		l.emit(stream, buf[:i])
		buf = buf[i+1:]
	}
	if len(buf) > maxLineLen {
		l.emit(stream, buf)
		buf = nil
	}
	l.partial[stream] = append([]byte(nil), buf...)
}

func (l *LineReader) flush() {
	for s := range l.partial {
		if len(l.partial[s]) > 0 {
			l.emit(byte(s), l.partial[s])
			l.partial[s] = nil
		}
	}
}

// Next returns the next line or io.EOF.
func (l *LineReader) Next() (LogLine, error) {
	for len(l.queue) == 0 {
		if err := l.fill(); err != nil {
			l.flush()
			if len(l.queue) > 0 {
				break
			}
			return LogLine{}, err
		}
	}
	ll := l.queue[0]
	l.queue = l.queue[1:]
	return ll, nil
}

func (l *LineReader) fill() error {
	if !l.mux {
		chunk := make([]byte, 16<<10)
		n, err := l.r.Read(chunk)
		if n > 0 {
			l.feed(1, chunk[:n])
		}
		if n > 0 && err == io.EOF {
			return nil
		}
		return err
	}
	var hdr [8]byte
	if _, err := io.ReadFull(l.r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return io.EOF
		}
		return err
	}
	size := int(binary.BigEndian.Uint32(hdr[4:]))
	keep := min(size, maxFrameLen)
	if cap(l.frame) < keep {
		l.frame = make([]byte, keep)
	}
	buf := l.frame[:keep]
	if _, err := io.ReadFull(l.r, buf); err != nil {
		return io.EOF
	}
	if size > keep {
		if _, err := io.CopyN(io.Discard, l.r, int64(size-keep)); err != nil {
			return io.EOF
		}
	}
	l.feed(hdr[0], buf)
	return nil
}

// Logs opens a log stream for container id. tail <= 0 means "all".
func (s *Service) Logs(ctx context.Context, id string, tail int, follow, timestamps bool) (*LineReader, io.Closer, error) {
	var in inspect
	if err := s.c.call(ctx, http.MethodGet, "/containers/"+id+"/json", nil, &in); err != nil {
		return nil, nil, err
	}
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}}
	if tail > 0 {
		q.Set("tail", strconv.Itoa(tail))
	} else {
		q.Set("tail", "all")
	}
	if follow {
		q.Set("follow", "1")
	}
	if timestamps {
		q.Set("timestamps", "1")
	}
	body, err := s.c.stream(ctx, http.MethodGet, "/containers/"+in.ID+"/logs?"+q.Encode(), nil)
	if err != nil {
		return nil, nil, err
	}
	return NewLineReader(body, in.Config.Tty, timestamps), body, nil
}

func (s *Service) handleLogs(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	tail := min(max(httpx.QueryInt(r, "tail", 500), 1), 10000)
	ts := r.URL.Query().Get("timestamps") != "0"
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	lr, closer, err := s.Logs(ctx, id, tail, false, ts)
	if err != nil {
		return dockerErr(err)
	}
	defer closer.Close()
	lines := make([]LogLine, 0, min(tail, 1000))
	total, truncated := 0, false
	for {
		ll, err := lr.Next()
		if err != nil {
			break
		}
		total += len(ll.Text) + len(ll.TS)
		if total > 16<<20 {
			truncated = true
			break
		}
		lines = append(lines, ll)
	}
	httpx.OK(w, map[string]any{"lines": lines, "truncated": truncated})
	return nil
}

// handleLogsWS follows logs over a WebSocket. Messages: {"lines":[...]},
// finally {"eof":true} when the container's stream ends.
func (s *Service) handleLogsWS(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	tail := min(max(httpx.QueryInt(r, "tail", 200), 0), 5000)
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.originPatterns()})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	lr, closer, err := s.Logs(ctx, id, tail, true, true)
	if err != nil {
		msg, _ := json.Marshal(map[string]string{"error": err.Error()})
		_ = c.Write(ctx, websocket.MessageText, msg)
		c.Close(websocket.StatusNormalClosure, "")
		return
	}
	defer closer.Close()

	ch := make(chan LogLine, 256)
	go func() {
		defer close(ch)
		for {
			ll, err := lr.Next()
			if err != nil {
				return
			}
			select {
			case ch <- ll:
			case <-ctx.Done():
				return
			}
		}
	}()
	send := func(v any) bool {
		b, _ := json.Marshal(v)
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return c.Write(wctx, websocket.MessageText, b) == nil
	}
	batch := make([]LogLine, 0, 256)
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ll, ok := <-ch:
			if !ok {
				if len(batch) > 0 && !send(map[string]any{"lines": batch}) {
					return
				}
				send(map[string]bool{"eof": true})
				c.Close(websocket.StatusNormalClosure, "")
				return
			}
			batch = append(batch, ll)
			if len(batch) >= 500 {
				if !send(map[string]any{"lines": batch}) {
					return
				}
				batch = batch[:0]
			}
		case <-tick.C:
			if len(batch) > 0 {
				if !send(map[string]any{"lines": batch}) {
					return
				}
				batch = batch[:0]
			}
		}
	}
}
