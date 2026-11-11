// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package store

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/database/chatgen"
)

// ---- Room lifecycle (event consumer) ----

// LockErrand serialises one errand's events, even across copies of the
// service. It must run inside WithTx: the lock is released when the
// transaction ends.
func (q *Queries) LockErrand(ctx context.Context, errandID uuid.UUID) error {
	return translate("lock errand", q.q.LockErrand(ctx, errandID))
}

// UpsertRoomOnAccept creates the room for errand.accepted, or returns the
// existing one. inserted is true only when this call created it. A room that
// errand.withdrawn created first gets its OpenedVersion and stays ended.
func (q *Queries) UpsertRoomOnAccept(ctx context.Context, room chat.AcceptedRoom) (chat.Room, bool, error) {
	row, err := q.q.UpsertRoomOnAccept(ctx, chatgen.UpsertRoomOnAcceptParams{
		ErrandID:      room.ErrandID,
		RequesterID:   room.RequesterID,
		CourierID:     room.CourierID,
		OpenedVersion: int32(room.OpenedVersion),
	})
	if err != nil {
		return chat.Room{}, false, translate("upsert room", err)
	}
	return roomFromRow(chatgen.ChatRoom{
		ID:            row.ID,
		ErrandID:      row.ErrandID,
		RequesterID:   row.RequesterID,
		CourierID:     row.CourierID,
		OpenedVersion: row.OpenedVersion,
		EndedAt:       row.EndedAt,
		EndReason:     row.EndReason,
		ClosesAt:      row.ClosesAt,
		CreatedAt:     row.CreatedAt,
	}), row.Inserted, nil
}

// EnsureRoom returns the (errand, courier) room, creating it without an
// OpenedVersion if errand.withdrawn arrived before errand.accepted.
func (q *Queries) EnsureRoom(ctx context.Context, errandID uuid.UUID, requesterID, courierID string) (chat.Room, error) {
	err := q.q.EnsureRoom(ctx, chatgen.EnsureRoomParams{
		ErrandID: errandID, RequesterID: requesterID, CourierID: courierID,
	})
	if err != nil {
		return chat.Room{}, translate("ensure room", err)
	}
	row, err := q.q.GetRoomByAssignment(ctx, chatgen.GetRoomByAssignmentParams{
		ErrandID: errandID, CourierID: courierID,
	})
	if err != nil {
		return chat.Room{}, translate("ensure room", err)
	}
	return roomFromRow(row), nil
}

// EndRoom sets the room's ending unless it already has one at the same time
// or earlier. changed reports whether it did; the room is returned either
// way. chat.ErrRoomNotFound if there is no such room.
func (q *Queries) EndRoom(ctx context.Context, roomID uuid.UUID, ending chat.Ending) (chat.Room, bool, error) {
	row, err := q.q.EndRoom(ctx, chatgen.EndRoomParams{
		ID:        roomID,
		EndedAt:   ending.EndedAt,
		EndReason: string(ending.Reason),
		ClosesAt:  ending.ClosesAt,
	})
	if err == nil {
		return roomFromRow(row), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return chat.Room{}, false, translate("end room", err)
	}
	room, err := q.GetRoom(ctx, roomID)
	return room, false, err
}

// EndRoomsForErrand is EndRoom for every room of an errand. It returns the
// rooms it changed; rooms that already ended earlier keep their ending.
func (q *Queries) EndRoomsForErrand(ctx context.Context, errandID uuid.UUID, ending chat.Ending) ([]chat.Room, error) {
	rows, err := q.q.EndRoomsForErrand(ctx, chatgen.EndRoomsForErrandParams{
		ErrandID:  errandID,
		EndedAt:   ending.EndedAt,
		EndReason: string(ending.Reason),
		ClosesAt:  ending.ClosesAt,
	})
	if err != nil {
		return nil, translate("end rooms for errand", err)
	}
	return roomsFromRows(rows), nil
}

// ErrandEnding returns the recorded completion or refund of an errand, or nil
// if there is none.
func (q *Queries) ErrandEnding(ctx context.Context, errandID uuid.UUID) (*chat.ErrandEnding, error) {
	row, err := q.q.GetErrandEnding(ctx, errandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, translate("get errand ending", err)
	}
	return &chat.ErrandEnding{ErrandID: row.ErrandID, Reason: chat.EndReason(row.Reason), EndedAt: row.EndedAt}, nil
}

// RecordErrandEnding remembers a completion or refund, keeping the earliest
// if one is already recorded.
func (q *Queries) RecordErrandEnding(ctx context.Context, ending chat.ErrandEnding) error {
	return translate("record errand ending", q.q.InsertErrandEnding(ctx, chatgen.InsertErrandEndingParams{
		ErrandID: ending.ErrandID,
		Reason:   string(ending.Reason),
		EndedAt:  ending.EndedAt,
	}))
}

// ---- Rooms ----

// GetRoom returns chat.ErrRoomNotFound if there is no such room.
func (q *Queries) GetRoom(ctx context.Context, roomID uuid.UUID) (chat.Room, error) {
	row, err := q.q.GetRoom(ctx, roomID)
	if errors.Is(err, pgx.ErrNoRows) {
		return chat.Room{}, chat.ErrRoomNotFound
	}
	if err != nil {
		return chat.Room{}, translate("get room", err)
	}
	return roomFromRow(row), nil
}

// ListRoomsForErrandAndUser returns userID's rooms for one errand, newest
// first.
func (q *Queries) ListRoomsForErrandAndUser(ctx context.Context, errandID uuid.UUID, userID string) ([]chat.Room, error) {
	rows, err := q.q.ListRoomsForErrandAndUser(ctx, chatgen.ListRoomsForErrandAndUserParams{
		ErrandID: errandID, UserID: userID,
	})
	if err != nil {
		return nil, translate("list rooms for errand and user", err)
	}
	return roomsFromRows(rows), nil
}

// ListRoomsForUser returns up to limit of userID's rooms, newest first, each
// with its newest message.
func (q *Queries) ListRoomsForUser(ctx context.Context, userID string, limit int) ([]chat.RoomSummary, error) {
	rowLimit, err := rowLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := q.q.ListRoomsForUser(ctx, chatgen.ListRoomsForUserParams{UserID: userID, RowLimit: rowLimit})
	if err != nil {
		return nil, translate("list rooms for user", err)
	}
	summaries := make([]chat.RoomSummary, len(rows))
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		summaries[i].Room = roomFromRow(row)
		ids[i] = row.ID
	}
	if len(ids) == 0 {
		return summaries, nil
	}
	last, err := q.q.ListLastMessages(ctx, ids)
	if err != nil {
		return nil, translate("list last messages", err)
	}
	byRoom := make(map[uuid.UUID]chat.Message, len(last))
	for _, row := range last {
		byRoom[row.RoomID] = messageFromRow(row)
	}
	for i := range summaries {
		if message, ok := byRoom[summaries[i].Room.ID]; ok {
			summaries[i].LastMessage = &message
		}
	}
	return summaries, nil
}

// ListRoomsForErrand returns every room of an errand, oldest first, including
// withdrawn couriers' rooms. No route exposes it; tests use it to inspect rooms.
func (q *Queries) ListRoomsForErrand(ctx context.Context, errandID uuid.UUID) ([]chat.Room, error) {
	rows, err := q.q.ListRoomsForErrand(ctx, errandID)
	if err != nil {
		return nil, translate("list rooms for errand", err)
	}
	return roomsFromRows(rows), nil
}

// ---- Messages ----

// InsertMessage stores the message. If the room already has one with the
// same ClientMsgID (a retried send), it returns that one with inserted false
// and stores nothing. chat.ErrRoomNotFound if the room does not exist,
// chat.ErrInvalidMessage if the body or ClientMsgID length is wrong.
func (q *Queries) InsertMessage(ctx context.Context, message chat.NewMessage) (chat.Message, bool, error) {
	row, err := q.q.InsertMessage(ctx, chatgen.InsertMessageParams{
		RoomID:      message.RoomID,
		SenderID:    message.SenderID,
		Body:        message.Body,
		ClientMsgID: message.ClientMsgID,
	})
	if err == nil {
		return messageFromRow(row), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return chat.Message{}, false, translate("insert message", err)
	}
	existing, err := q.q.GetMessageByClientID(ctx, chatgen.GetMessageByClientIDParams{
		RoomID: message.RoomID, ClientMsgID: message.ClientMsgID,
	})
	if err != nil {
		return chat.Message{}, false, translate("get message by client ID", err)
	}
	return messageFromRow(existing), false, nil
}

// ListMessagesAfter returns up to limit messages with an ID above afterID,
// oldest first. afterID 0 starts from the beginning.
func (q *Queries) ListMessagesAfter(ctx context.Context, roomID uuid.UUID, afterID int64, limit int) ([]chat.Message, error) {
	rowLimit, err := rowLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := q.q.ListMessagesAfter(ctx, chatgen.ListMessagesAfterParams{RoomID: roomID, AfterID: afterID, RowLimit: rowLimit})
	if err != nil {
		return nil, translate("list messages after", err)
	}
	return messagesFromRows(rows), nil
}

// ListMessagesBefore returns the limit messages just below beforeID, oldest
// first. beforeID math.MaxInt64 returns the newest messages.
func (q *Queries) ListMessagesBefore(ctx context.Context, roomID uuid.UUID, beforeID int64, limit int) ([]chat.Message, error) {
	rowLimit, err := rowLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := q.q.ListMessagesBefore(ctx, chatgen.ListMessagesBeforeParams{RoomID: roomID, BeforeID: beforeID, RowLimit: rowLimit})
	if err != nil {
		return nil, translate("list messages before", err)
	}
	messages := messagesFromRows(rows)
	slices.Reverse(messages) // the query returns newest first
	return messages, nil
}
