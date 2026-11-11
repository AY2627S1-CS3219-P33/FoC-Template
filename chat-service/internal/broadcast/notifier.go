// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package broadcast

import (
	"context"
	"log/slog"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/protocol"
)

// Notifier implements chat.Notifier: it turns what happened into WebSocket
// frames and publishes them to the room's two participants. chat calls it
// only after the change is committed.
type Notifier struct {
	broadcaster Broadcaster
	clock       func() time.Time
	logger      *slog.Logger
}

var _ chat.Notifier = (*Notifier)(nil)

func NewNotifier(broadcaster Broadcaster, clock func() time.Time, logger *slog.Logger) *Notifier {
	if broadcaster == nil || logger == nil {
		panic("broadcast: broadcaster and logger are required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &Notifier{broadcaster: broadcaster, clock: clock, logger: logger}
}

// MessageSent sends the message to both participants, including every open
// tab of the sender, so their other tabs show it too.
func (n *Notifier) MessageSent(ctx context.Context, room chat.Room, message chat.MessageView) {
	n.publish(ctx, protocol.TypeMessage, room, []string{room.RequesterID, room.CourierID},
		protocol.Encode(protocol.MessageFrame{Type: protocol.TypeMessage, Message: protocol.FromMessageView(message)}))
}

// RoomOpened tells each participant about the new room. Each gets their own
// frame, because myRole differs.
func (n *Notifier) RoomOpened(ctx context.Context, room chat.Room) {
	now := n.clock()
	for _, userID := range []string{room.RequesterID, room.CourierID} {
		role, _ := room.RoleOf(userID)
		view := chat.RoomView{Room: room, Status: room.Status(now), MyRole: role}
		n.publish(ctx, protocol.TypeRoomOpened, room, []string{userID},
			protocol.Encode(protocol.RoomOpened{Type: protocol.TypeRoomOpened, Room: protocol.FromRoomView(view)}))
	}
}

// RoomClosing tells both participants the errand ended for the room.
func (n *Notifier) RoomClosing(ctx context.Context, room chat.Room) {
	if room.Ending == nil {
		return
	}
	n.publish(ctx, protocol.TypeRoomClosing, room, []string{room.RequesterID, room.CourierID},
		protocol.Encode(protocol.RoomClosing{
			Type: protocol.TypeRoomClosing, RoomID: room.ID, ErrandID: room.ErrandID,
			EndReason: room.Ending.Reason, ClosesAt: room.Ending.ClosesAt.UTC(),
		}))
}

// publish never fails the caller: the change is already saved, and clients
// catch up over REST.
func (n *Notifier) publish(ctx context.Context, frameType string, room chat.Room, recipients []string, payload []byte) {
	if err := n.broadcaster.Publish(ctx, Envelope{Recipients: recipients, Payload: payload}); err != nil {
		n.logger.WarnContext(ctx, "live delivery failed; clients will catch up on reconnect",
			"frame", frameType, "roomId", room.ID.String(), "error", err.Error())
	}
}
