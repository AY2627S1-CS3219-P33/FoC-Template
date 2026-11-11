// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package chat

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Store is what Service needs from the database. internal/store implements
// it; tests use an in-memory fake, so the rules are tested without a network.
type Store interface {
	GetRoom(ctx context.Context, roomID uuid.UUID) (Room, error)
	ListRoomsForUser(ctx context.Context, userID string, limit int) ([]RoomSummary, error)
	ListRoomsForErrandAndUser(ctx context.Context, errandID uuid.UUID, userID string) ([]Room, error)
	ListMessagesAfter(ctx context.Context, roomID uuid.UUID, afterID int64, limit int) ([]Message, error)
	ListMessagesBefore(ctx context.Context, roomID uuid.UUID, beforeID int64, limit int) ([]Message, error)
	InsertMessage(ctx context.Context, message NewMessage) (Message, bool, error)
}

const (
	// DefaultPageSize and MaxPageSize bound a history page.
	DefaultPageSize = 50
	MaxPageSize     = 100
	// MaxRoomsListed bounds MyRooms. A user would need hundreds of errands
	// to reach it; older rooms are then left out.
	MaxRoomsListed = 200
	// MaxClientMsgIDLength matches chat_messages_client_msg_id_length.
	MaxClientMsgIDLength = 64
)

// Notifier tells connected browsers what changed. It is called only after
// the change is committed ("save, then push"), and never fails the caller:
// a browser that misses a push catches up over REST when it reconnects.
// internal/broadcast implements it.
type Notifier interface {
	MessageSent(ctx context.Context, room Room, message MessageView)
	RoomOpened(ctx context.Context, room Room)
	RoomClosing(ctx context.Context, room Room)
}

// NoNotifier pushes nothing.
type NoNotifier struct{}

func (NoNotifier) MessageSent(context.Context, Room, MessageView) {}
func (NoNotifier) RoomOpened(context.Context, Room)               {}
func (NoNotifier) RoomClosing(context.Context, Room)              {}

// Options configure a Service. Zero values select the defaults.
type Options struct {
	// Clock returns the current time; tests pass a fake to jump ahead.
	Clock func() time.Time
	// MaxMessageLength is CHAT_MAX_MESSAGE_LENGTH, at most 2000.
	MaxMessageLength int
	// Notifier pushes new messages to connected browsers; nil pushes nothing.
	Notifier Notifier
}

// Service is the single place that decides what is allowed. REST, and later
// the WebSocket and the event consumer, all call it, so their rules cannot
// drift apart. Callers pass the user ID from the verified token, never one
// taken from the request.
type Service struct {
	store     Store
	clock     func() time.Time
	maxLength int
	notifier  Notifier
}

func NewService(store Store, options Options) *Service {
	if store == nil {
		panic("chat: store is required")
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.MaxMessageLength <= 0 || options.MaxMessageLength > 2000 {
		options.MaxMessageLength = 2000
	}
	if options.Notifier == nil {
		options.Notifier = NoNotifier{}
	}
	return &Service{store: store, clock: options.Clock, maxLength: options.MaxMessageLength, notifier: options.Notifier}
}

// RoomView is a room as one participant sees it at one moment.
type RoomView struct {
	Room
	// Status is calculated with the server's clock when the view is made.
	Status Status
	// MyRole is the caller's side of the room.
	MyRole Role
	// LastMessage is set only by MyRooms (nil when the room has no messages).
	LastMessage *Message
}

// MessageView is a message with its sender's side of the room, so clients
// can show "you" and "them" without knowing who is who.
type MessageView struct {
	Message
	SenderRole Role
}

// Page selects a slice of a room's history. At most one of After and Before
// may be set. With neither, the newest messages are returned.
type Page struct {
	// After returns messages newer than this ID (catch-up after a reconnect).
	After *int64
	// Before returns messages older than this ID (scrolling back).
	Before *int64
	// Limit is 1 to MaxPageSize; 0 means DefaultPageSize.
	Limit int
}

// MyRooms lists the caller's rooms on every errand, newest first.
func (s *Service) MyRooms(ctx context.Context, userID string) ([]RoomView, error) {
	summaries, err := s.store.ListRoomsForUser(ctx, userID, MaxRoomsListed)
	if err != nil {
		return nil, err
	}
	now := s.clock()
	views := make([]RoomView, 0, len(summaries))
	for _, summary := range summaries {
		view, ok := s.view(summary.Room, userID, now)
		if !ok {
			continue // the query only returns the caller's rooms; never trust that blindly
		}
		view.LastMessage = summary.LastMessage
		views = append(views, view)
	}
	return views, nil
}

// RoomsForErrand lists the caller's rooms for one errand, newest first: at
// most one for a courier, one per accepting courier for the requester. An
// outsider gets an empty list, the same as an errand without rooms.
func (s *Service) RoomsForErrand(ctx context.Context, userID string, errandID uuid.UUID) ([]RoomView, error) {
	rooms, err := s.store.ListRoomsForErrandAndUser(ctx, errandID, userID)
	if err != nil {
		return nil, err
	}
	now := s.clock()
	views := make([]RoomView, 0, len(rooms))
	for _, room := range rooms {
		if view, ok := s.view(room, userID, now); ok {
			views = append(views, view)
		}
	}
	return views, nil
}

// Room returns one room the caller takes part in. ErrRoomNotFound if there is
// no such room or the caller is not a participant: the two look the same, so
// outsiders cannot tell whether a room exists.
func (s *Service) Room(ctx context.Context, userID string, roomID uuid.UUID) (RoomView, error) {
	room, err := s.participantRoom(ctx, userID, roomID)
	if err != nil {
		return RoomView{}, err
	}
	view, _ := s.view(room, userID, s.clock())
	return view, nil
}

// History returns a page of the room's messages, oldest first. Reading is
// allowed in every state, including CLOSED.
func (s *Service) History(ctx context.Context, userID string, roomID uuid.UUID, page Page) ([]MessageView, error) {
	limit, err := checkPage(page)
	if err != nil {
		return nil, err
	}
	room, err := s.participantRoom(ctx, userID, roomID)
	if err != nil {
		return nil, err
	}
	var messages []Message
	switch {
	case page.After != nil:
		messages, err = s.store.ListMessagesAfter(ctx, roomID, *page.After, limit)
	case page.Before != nil:
		messages, err = s.store.ListMessagesBefore(ctx, roomID, *page.Before, limit)
	default:
		messages, err = s.store.ListMessagesBefore(ctx, roomID, math.MaxInt64, limit)
	}
	if err != nil {
		return nil, err
	}
	views := make([]MessageView, len(messages))
	for i, message := range messages {
		views[i] = messageView(room, message)
	}
	return views, nil
}

// Send stores a message from the caller. It returns the stored message and
// whether this call created it: a retry with the same clientMsgID returns the
// first message with created false and stores nothing. Errors:
// ErrInvalidMessage (bad body or clientMsgID), ErrRoomNotFound (no such room,
// or not a participant), ErrRoomClosed (the room is read-only).
//
// The message is pushed to both participants only after it is saved, and
// only the first time: a retry pushes nothing.
func (s *Service) Send(ctx context.Context, userID string, roomID uuid.UUID, clientMsgID, body string) (MessageView, bool, error) {
	body, err := s.cleanBody(body)
	if err != nil {
		return MessageView{}, false, err
	}
	if err := checkClientMsgID(clientMsgID); err != nil {
		return MessageView{}, false, err
	}
	room, err := s.participantRoom(ctx, userID, roomID)
	if err != nil {
		return MessageView{}, false, err
	}
	// One clock, the server's. A send in the same instant the room closes may
	// go either way; 24 hours have passed by then.
	if room.Status(s.clock()) == StatusClosed {
		return MessageView{}, false, ErrRoomClosed
	}
	message, created, err := s.store.InsertMessage(ctx, NewMessage{
		RoomID: roomID, SenderID: userID, Body: body, ClientMsgID: clientMsgID,
	})
	if err != nil {
		return MessageView{}, false, err
	}
	if !created && message.SenderID != userID {
		// The other participant already used this ID. Browser-made UUIDs
		// never collide, so this is a client bug; do not hand back their message
		// as if it were the caller's.
		return MessageView{}, false, fmt.Errorf("%w: clientMsgId is already used in this room", ErrInvalidMessage)
	}
	view := messageView(room, message)
	if created {
		s.notifier.MessageSent(ctx, room, view)
	}
	return view, created, nil
}

// participantRoom loads the room and checks the caller takes part in it.
func (s *Service) participantRoom(ctx context.Context, userID string, roomID uuid.UUID) (Room, error) {
	room, err := s.store.GetRoom(ctx, roomID)
	if err != nil {
		return Room{}, err
	}
	if _, ok := room.RoleOf(userID); !ok {
		return Room{}, ErrRoomNotFound
	}
	return room, nil
}

func (s *Service) view(room Room, userID string, now time.Time) (RoomView, bool) {
	role, ok := room.RoleOf(userID)
	if !ok {
		return RoomView{}, false
	}
	return RoomView{Room: room, Status: room.Status(now), MyRole: role}, true
}

func messageView(room Room, message Message) MessageView {
	role, _ := room.RoleOf(message.SenderID)
	return MessageView{Message: message, SenderRole: role}
}

// cleanBody trims surrounding whitespace and checks the body: 1 to
// maxLength characters, valid UTF-8, and no control characters other than
// newline and tab. Windows line endings become newlines. The text is
// otherwise stored exactly as typed; escaping happens when it is displayed.
func (s *Service) cleanBody(body string) (string, error) {
	if !utf8.ValidString(body) {
		return "", fmt.Errorf("%w: body must be valid UTF-8 text", ErrInvalidMessage)
	}
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	length := utf8.RuneCountInString(body)
	if length < 1 || length > s.maxLength {
		return "", fmt.Errorf("%w: body must be 1 to %d characters", ErrInvalidMessage, s.maxLength)
	}
	for _, r := range body {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", fmt.Errorf("%w: body must not contain control characters", ErrInvalidMessage)
		}
	}
	return body, nil
}

// clientMsgIDPattern allows UUIDs and other simple identifiers.
var clientMsgIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func checkClientMsgID(id string) error {
	if !clientMsgIDPattern.MatchString(id) {
		return fmt.Errorf("%w: clientMsgId must be 1 to %d letters, digits or . _ : -", ErrInvalidMessage, MaxClientMsgIDLength)
	}
	return nil
}

func checkPage(page Page) (int, error) {
	if page.After != nil && page.Before != nil {
		return 0, fmt.Errorf("%w: use after or before, not both", ErrInvalidPage)
	}
	if (page.After != nil && *page.After < 0) || (page.Before != nil && *page.Before < 1) {
		return 0, fmt.Errorf("%w: after must be 0 or more, and before 1 or more", ErrInvalidPage)
	}
	limit := page.Limit
	if limit == 0 {
		limit = DefaultPageSize
	}
	if limit < 1 || limit > MaxPageSize {
		return 0, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidPage, MaxPageSize)
	}
	return limit, nil
}
