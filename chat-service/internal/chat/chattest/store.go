// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package chattest provides an in-memory chat.Store for tests of chat.Service
// and the HTTP handlers. It returns the same results as internal/store, whose
// own integration tests check them against PostgreSQL.
package chattest

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
)

// Store is safe for concurrent use. The zero value is empty and ready.
type Store struct {
	mu       sync.Mutex
	rooms    []chat.Room
	messages []chat.Message
	err      error
}

var _ chat.Store = (*Store)(nil)

// AddRoom adds an open room. Later rooms count as newer.
func (s *Store) AddRoom(errandID uuid.UUID, requesterID, courierID string) chat.Room {
	s.mu.Lock()
	defer s.mu.Unlock()
	version := 1
	room := chat.Room{
		ID: uuid.New(), ErrandID: errandID, RequesterID: requesterID, CourierID: courierID,
		OpenedVersion: &version,
		CreatedAt:     time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC).Add(time.Duration(len(s.rooms)) * time.Minute),
	}
	s.rooms = append(s.rooms, room)
	return room
}

// EndRoom gives the room an ending that closes delay after endedAt.
func (s *Store) EndRoom(roomID uuid.UUID, reason chat.EndReason, endedAt time.Time, delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rooms {
		if s.rooms[i].ID == roomID {
			s.rooms[i].Ending = &chat.Ending{Reason: reason, EndedAt: endedAt, ClosesAt: endedAt.Add(delay)}
		}
	}
}

// Messages returns every stored message in order.
func (s *Store) Messages() []chat.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.messages)
}

// FailWith makes every later call return err; nil restores normal behaviour.
func (s *Store) FailWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *Store) GetRoom(_ context.Context, roomID uuid.UUID) (chat.Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return chat.Room{}, s.err
	}
	for _, room := range s.rooms {
		if room.ID == roomID {
			return room, nil
		}
	}
	return chat.Room{}, chat.ErrRoomNotFound
}

func (s *Store) ListRoomsForUser(_ context.Context, userID string, limit int) ([]chat.RoomSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := []chat.RoomSummary{}
	for i := len(s.rooms) - 1; i >= 0 && len(out) < limit; i-- {
		room := s.rooms[i]
		if room.RequesterID != userID && room.CourierID != userID {
			continue
		}
		summary := chat.RoomSummary{Room: room}
		for j := len(s.messages) - 1; j >= 0; j-- {
			if s.messages[j].RoomID == room.ID {
				message := s.messages[j]
				summary.LastMessage = &message
				break
			}
		}
		out = append(out, summary)
	}
	return out, nil
}

func (s *Store) ListRoomsForErrandAndUser(_ context.Context, errandID uuid.UUID, userID string) ([]chat.Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := []chat.Room{}
	for i := len(s.rooms) - 1; i >= 0; i-- {
		room := s.rooms[i]
		if room.ErrandID == errandID && (room.RequesterID == userID || room.CourierID == userID) {
			out = append(out, room)
		}
	}
	return out, nil
}

func (s *Store) ListMessagesAfter(_ context.Context, roomID uuid.UUID, afterID int64, limit int) ([]chat.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := []chat.Message{}
	for _, m := range s.messages {
		if m.RoomID == roomID && m.ID > afterID && len(out) < limit {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Store) ListMessagesBefore(_ context.Context, roomID uuid.UUID, beforeID int64, limit int) ([]chat.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := []chat.Message{}
	for i := len(s.messages) - 1; i >= 0 && len(out) < limit; i-- {
		if m := s.messages[i]; m.RoomID == roomID && m.ID < beforeID {
			out = append(out, m)
		}
	}
	slices.Reverse(out)
	return out, nil
}

func (s *Store) InsertMessage(_ context.Context, message chat.NewMessage) (chat.Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return chat.Message{}, false, s.err
	}
	found := false
	for _, room := range s.rooms {
		found = found || room.ID == message.RoomID
	}
	if !found {
		return chat.Message{}, false, chat.ErrRoomNotFound
	}
	for _, m := range s.messages {
		if m.RoomID == message.RoomID && m.ClientMsgID == message.ClientMsgID {
			return m, false, nil
		}
	}
	stored := chat.Message{
		ID: int64(len(s.messages) + 1), RoomID: message.RoomID, SenderID: message.SenderID,
		Body: message.Body, ClientMsgID: message.ClientMsgID,
		CreatedAt: time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC).Add(time.Duration(len(s.messages)) * time.Second),
	}
	s.messages = append(s.messages, stored)
	return stored, true, nil
}

// Clock is a clock tests move by hand.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock(now time.Time) *Clock { return &Clock{now: now} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}
