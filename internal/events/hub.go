// SPDX-License-Identifier: AGPL-3.0-only

// Package events is an in-process pub/sub hub fanned out to browsers over
// a single multiplexed WebSocket.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Message is what clients receive.
type Message struct {
	Topic string          `json:"topic"`
	Data  json.RawMessage `json:"data"`
}

type subscriber struct {
	topics map[string]bool
	ch     chan []byte
}

// Hub fans out messages to subscribers by topic.
type Hub struct {
	mu   sync.RWMutex
	subs map[*subscriber]struct{}
	// last keeps the most recent message per "sticky" topic so new
	// subscribers get an immediate snapshot.
	last   map[string][]byte
	sticky map[string]bool
	// OriginPatterns are passed to websocket.Accept.
	OriginPatterns []string
}

func NewHub() *Hub {
	return &Hub{subs: map[*subscriber]struct{}{}, last: map[string][]byte{}, sticky: map[string]bool{}}
}

// Sticky marks topics whose latest message is replayed to new subscribers.
func (h *Hub) Sticky(topics ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range topics {
		h.sticky[t] = true
	}
}

// Publish sends v (JSON-encoded) to every subscriber of topic.
func (h *Hub) Publish(topic string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("events: marshal", "topic", topic, "err", err)
		return
	}
	msg, _ := json.Marshal(Message{Topic: topic, Data: data})
	h.mu.Lock()
	if h.sticky[topic] {
		h.last[topic] = msg
	}
	h.mu.Unlock()
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs {
		if !s.topics[topic] {
			continue
		}
		select {
		case s.ch <- msg:
		default: // slow consumer: drop
		}
	}
}

// Subscribers returns how many clients listen to topic. Pollers use it to
// stay idle when nobody is watching.
func (h *Hub) Subscribers(topic string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for s := range h.subs {
		if s.topics[topic] {
			n++
		}
	}
	return n
}

// maxTopics bounds the topics one connection may subscribe to.
const maxTopics = 64

func topicSet(topics []string) map[string]bool {
	out := map[string]bool{}
	for _, t := range topics {
		if t = strings.TrimSpace(t); t != "" && len(t) <= 128 && len(out) < maxTopics {
			out[t] = true
		}
	}
	return out
}

func (h *Hub) add(topics []string) *subscriber {
	s := &subscriber{topics: topicSet(topics), ch: make(chan []byte, 64)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	for t := range s.topics {
		if m, ok := h.last[t]; ok {
			// Never block while holding the hub lock (a full buffer
			// would otherwise stall every publisher).
			select {
			case s.ch <- m:
			default:
			}
		}
	}
	h.mu.Unlock()
	return s
}

func (h *Hub) remove(s *subscriber) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

// ServeWS upgrades and streams messages for the topics in ?topics=a,b.
// Clients may change subscriptions by sending {"topics":[...]}.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.OriginPatterns})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	s := h.add(strings.Split(r.URL.Query().Get("topics"), ","))
	defer h.remove(s)

	go func() {
		defer cancel()
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			var req struct {
				Topics []string `json:"topics"`
			}
			if json.Unmarshal(b, &req) == nil && req.Topics != nil {
				h.mu.Lock()
				s.topics = topicSet(req.Topics)
				var replay [][]byte
				for t := range s.topics {
					if m, ok := h.last[t]; ok {
						replay = append(replay, m)
					}
				}
				h.mu.Unlock()
				for _, m := range replay {
					select {
					case s.ch <- m:
					default:
					}
				}
			}
		}
	}()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case m := <-s.ch:
			wctx, wc := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageText, m)
			wc()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, pc := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			pc()
			if err != nil {
				return
			}
		}
	}
}
