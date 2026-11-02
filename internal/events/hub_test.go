// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"fmt"
	"testing"
	"time"
)

// Subscribing to more sticky topics than the channel buffer holds used to
// block while holding the hub lock, freezing every publisher.
func TestAddManyStickyTopicsDoesNotBlock(t *testing.T) {
	h := NewHub()
	var topics []string
	for i := 0; i < 200; i++ {
		tp := fmt.Sprintf("t%d", i)
		h.Sticky(tp)
		h.Publish(tp, i)
		topics = append(topics, tp)
	}
	done := make(chan struct{})
	go func() {
		s := h.add(topics)
		if len(s.topics) > maxTopics {
			t.Errorf("%d topics accepted", len(s.topics))
		}
		h.Publish("t1", 1)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hub deadlocked")
	}
}
