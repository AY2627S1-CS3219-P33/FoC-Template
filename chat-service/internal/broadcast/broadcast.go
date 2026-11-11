// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package broadcast answers "deliver this frame to these users, wherever they
// are connected". It is the only part that changes when chat-service runs as
// several copies: LocalBroadcaster hands frames straight to this copy's hub;
// a RedisBroadcaster (later) will send them through Redis so every copy can
// deliver to the connections it holds.
package broadcast

import (
	"context"
	"encoding/json"
)

// Envelope is what travels between copies (and, later, through Redis).
type Envelope struct {
	// Recipients are Auth0 subjects, not connections, so any copy can
	// deliver.
	Recipients []string `json:"recipients"`
	// Payload is one server-to-client WebSocket frame.
	Payload json.RawMessage `json:"payload"`
}

// Broadcaster delivers envelopes. Publish is called only after the data in
// the frame is committed to PostgreSQL. Losing an envelope is acceptable:
// clients catch up over REST when they reconnect.
type Broadcaster interface {
	Publish(ctx context.Context, env Envelope) error
	// Run blocks until ctx ends. A RedisBroadcaster subscribes here and
	// forwards what it receives to the hub.
	Run(ctx context.Context) error
}

// Deliverer is what a broadcaster needs from the hub.
type Deliverer interface {
	Deliver(userIDs []string, payload []byte)
}

// Local delivers within this process. It is correct only while one copy of
// the service runs.
type Local struct {
	hub Deliverer
}

func NewLocal(hub Deliverer) *Local {
	if hub == nil {
		panic("broadcast: hub is required")
	}
	return &Local{hub: hub}
}

func (l *Local) Publish(_ context.Context, env Envelope) error {
	l.hub.Deliver(env.Recipients, env.Payload)
	return nil
}

// Run has nothing to do for Local; it waits so every Broadcaster runs as a
// worker the same way.
func (l *Local) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
