// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package hub remembers which WebSocket connections are open in this copy of
// the service, keyed by user, and hands payloads to them. It is the only
// state chat-service keeps in memory; everything that must be correct lives
// in PostgreSQL.
//
// The hub knows nothing about WebSockets: a Client is a buffered outbox plus
// a function to call when the client cannot keep up. Package ws drains the
// outbox to the socket.
package hub

import (
	"sync"
)

// DefaultBuffer is how many frames may wait for a slow client before it is
// dropped.
const DefaultBuffer = 32

// Client is one open connection (one browser tab) of one user.
type Client struct {
	userID string
	outbox chan []byte
	// tooSlow is called once, in its own goroutine, when the outbox is full.
	tooSlow  func()
	slowOnce sync.Once
}

// NewClient makes a client with room for buffer waiting frames. tooSlow is
// called (once) when a delivery finds the outbox full; it should close the
// connection so the browser reconnects and catches up over REST.
func NewClient(userID string, buffer int, tooSlow func()) *Client {
	if userID == "" || buffer < 1 || tooSlow == nil {
		panic("hub: user ID, a positive buffer and tooSlow are required")
	}
	return &Client{userID: userID, outbox: make(chan []byte, buffer), tooSlow: tooSlow}
}

// UserID is the Auth0 subject the connection authenticated as.
func (c *Client) UserID() string { return c.userID }

// Outbox delivers the frames for this client, in order. It is never closed;
// the reader stops when its connection ends.
func (c *Client) Outbox() <-chan []byte { return c.outbox }

// Hub is safe for concurrent use.
type Hub struct {
	mu         sync.RWMutex
	users      map[string]map[*Client]struct{}
	maxPerUser int
}

// New makes a hub that allows maxPerUser connections per user in this copy.
// With several copies the limit applies per copy, so it is approximate.
func New(maxPerUser int) *Hub {
	if maxPerUser < 1 {
		panic("hub: maxPerUser must be positive")
	}
	return &Hub{users: map[string]map[*Client]struct{}{}, maxPerUser: maxPerUser}
}

// Register adds the client. It returns false, and adds nothing, when the
// user already has maxPerUser connections.
func (h *Hub) Register(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	clients := h.users[c.userID]
	if len(clients) >= h.maxPerUser {
		return false
	}
	if clients == nil {
		clients = map[*Client]struct{}{}
		h.users[c.userID] = clients
	}
	clients[c] = struct{}{}
	return true
}

// Unregister removes the client. Removing it twice is harmless.
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	clients := h.users[c.userID]
	delete(clients, c)
	if len(clients) == 0 {
		delete(h.users, c.userID)
	}
}

// Deliver queues payload for every open connection of each user. It never
// blocks: a client whose outbox is full is dropped (tooSlow) instead of
// holding up everyone else. Users with no connection here are skipped; they
// catch up over REST when they next connect. payload must not be modified
// afterwards, because every client shares it.
func (h *Hub) Deliver(userIDs []string, payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	seen := make(map[string]bool, len(userIDs))
	for _, userID := range userIDs {
		if seen[userID] {
			continue
		}
		seen[userID] = true
		for c := range h.users[userID] {
			select {
			case c.outbox <- payload:
			default:
				c.slowOnce.Do(func() { go c.tooSlow() })
			}
		}
	}
}

// Connections returns how many connections the user has open here.
func (h *Hub) Connections(userID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.users[userID])
}
