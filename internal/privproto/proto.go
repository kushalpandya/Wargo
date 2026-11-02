// SPDX-License-Identifier: AGPL-3.0-only

// Package privproto defines the wire protocol between wargo and the
// optional root helper wargo-privd: one JSON request and one JSON
// response per unix-socket connection.
package privproto

import "encoding/json"

const DefaultSocket = "/run/wargo-privd.sock"

type Request struct {
	Op string `json:"op"` // "status" | "challenge" | "run"

	Action string `json:"action,omitempty"`
	Target string `json:"target,omitempty"`
	Origin string `json:"origin,omitempty"`

	ChallengeID string          `json:"challengeId,omitempty"`
	Assertion   json.RawMessage `json:"assertion,omitempty"`
	Argv        []string        `json:"argv,omitempty"`
	Stdin       []byte          `json:"stdin,omitempty"`
}

type Response struct {
	OK          bool            `json:"ok"`
	Error       string          `json:"error,omitempty"`
	Rejected    bool            `json:"rejected,omitempty"` // assertion invalid
	ChallengeID string          `json:"challengeId,omitempty"`
	Options     json.RawMessage `json:"options,omitempty"`
	Output      []byte          `json:"output,omitempty"`
	Credentials int             `json:"credentials,omitempty"`
}
