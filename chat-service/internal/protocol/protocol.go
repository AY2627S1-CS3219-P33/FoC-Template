// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package protocol is chat-service's wire format: the JSON shapes of rooms and
// messages, used by both the REST API and the WebSocket, and every WebSocket
// frame. docs/websocket-protocol.md describes the same frames for client
// authors; keep the two in step.
package protocol

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
)

// ---- Rooms and messages (also the REST response bodies) ----

// Room is a room as one participant sees it.
type Room struct {
	ID          uuid.UUID   `json:"id"`
	ErrandID    uuid.UUID   `json:"errandId"`
	RequesterID string      `json:"requesterId"`
	CourierID   string      `json:"courierId"`
	MyRole      chat.Role   `json:"myRole"`
	Status      chat.Status `json:"status"`
	EndedAt     *time.Time  `json:"endedAt"`
	EndReason   *string     `json:"endReason"`
	ClosesAt    *time.Time  `json:"closesAt"`
	CreatedAt   time.Time   `json:"createdAt"`
	LastMessage *Message    `json:"lastMessage,omitempty"`
}

type Message struct {
	ID          int64     `json:"id"`
	RoomID      uuid.UUID `json:"roomId"`
	SenderID    string    `json:"senderId"`
	SenderRole  chat.Role `json:"senderRole"`
	Body        string    `json:"body"`
	ClientMsgID string    `json:"clientMsgId"`
	CreatedAt   time.Time `json:"createdAt"`
}

func FromRoomView(view chat.RoomView) Room {
	out := Room{
		ID:          view.ID,
		ErrandID:    view.ErrandID,
		RequesterID: view.RequesterID,
		CourierID:   view.CourierID,
		MyRole:      view.MyRole,
		Status:      view.Status,
		CreatedAt:   view.CreatedAt.UTC(),
	}
	if ending := view.Ending; ending != nil {
		endedAt, closesAt, reason := ending.EndedAt.UTC(), ending.ClosesAt.UTC(), string(ending.Reason)
		out.EndedAt, out.ClosesAt, out.EndReason = &endedAt, &closesAt, &reason
	}
	if last := view.LastMessage; last != nil {
		role, _ := view.Room.RoleOf(last.SenderID)
		message := FromMessageView(chat.MessageView{Message: *last, SenderRole: role})
		out.LastMessage = &message
	}
	return out
}

func FromMessageView(view chat.MessageView) Message {
	return Message{
		ID:          view.ID,
		RoomID:      view.RoomID,
		SenderID:    view.SenderID,
		SenderRole:  view.SenderRole,
		Body:        view.Body,
		ClientMsgID: view.ClientMsgID,
		CreatedAt:   view.CreatedAt.UTC(),
	}
}

// ---- WebSocket frames: client to server ----

// Client frame types.
const (
	TypeAuth = "auth" // must be the first frame
	TypeSend = "send"
)

// ClientFrame is any frame a client sends; Type says which fields apply.
type ClientFrame struct {
	Type string `json:"type"`
	// auth
	Token string `json:"token,omitempty"`
	// send
	RoomID      string `json:"roomId,omitempty"`
	ClientMsgID string `json:"clientMsgId,omitempty"`
	Body        string `json:"body,omitempty"`
}

// ---- WebSocket frames: server to client ----

// Server frame types.
const (
	TypeReady       = "ready"
	TypeMessage     = "message"
	TypeAck         = "ack"
	TypeError       = "error"
	TypeRoomOpened  = "room_opened"
	TypeRoomClosing = "room_closing"
)

// Ready: authenticated. The client now fetches history over REST to catch up.
type Ready struct {
	Type   string `json:"type"`
	UserID string `json:"userId"`
}

// MessageFrame: a new message in one of the user's rooms, sent to both
// participants (all their open tabs, including the sender's).
type MessageFrame struct {
	Type    string  `json:"type"`
	Message Message `json:"message"`
}

// Ack: the client's send was saved.
type Ack struct {
	Type        string  `json:"type"`
	ClientMsgID string  `json:"clientMsgId"`
	Message     Message `json:"message"`
}

// Error: a frame was refused. Codes are the REST API's.
type Error struct {
	Type        string        `json:"type"`
	Code        apperror.Code `json:"code"`
	Message     string        `json:"message"`
	ClientMsgID string        `json:"clientMsgId,omitempty"`
}

// RoomOpened: a courier accepted one of the user's errands, or the user
// accepted one.
type RoomOpened struct {
	Type string `json:"type"`
	Room Room   `json:"room"`
}

// RoomClosing: the errand ended for the room; it becomes read-only at
// ClosesAt.
type RoomClosing struct {
	Type      string         `json:"type"`
	RoomID    uuid.UUID      `json:"roomId"`
	ErrandID  uuid.UUID      `json:"errandId"`
	EndReason chat.EndReason `json:"endReason"`
	ClosesAt  time.Time      `json:"closesAt"`
}

// Encode marshals a server frame. The frames above always marshal.
func Encode(frame any) []byte {
	data, err := json.Marshal(frame)
	if err != nil {
		panic("protocol: frame does not marshal: " + err.Error())
	}
	return data
}

// WebSocket close codes. 1000–1015 are standard; 4000–4999 are for
// applications.
const (
	CloseNormal          = 1000
	ClosePolicyViolation = 1008 // a malformed frame, or too slow to keep up
	CloseServiceRestart  = 1012 // the service is shutting down; reconnect
	CloseTryAgainLater   = 1013 // Auth0 is unreachable; reconnect later
	CloseUnauthenticated = 4401 // no valid auth frame in time, or the token expired
	CloseForbidden       = 4403 // a valid token without chats:use, or not a user token
	CloseTooManyConns    = 4429 // the user has too many open connections
)
