// SPDX-License-Identifier: AGPL-3.0-only

package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// RequestIDHeader carries each request's ID in the response, so an audit
// entry can be matched with Wargo's own log lines (journal or docker logs).
const RequestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// NewRequestID returns a random 16-character ID.
func NewRequestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// WithRequestID tags ctx with a request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns ctx's request ID, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// RequestIDs gives every request a new ID (incoming headers are not
// trusted) and returns it in X-Request-ID.
func RequestIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := NewRequestID()
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}
