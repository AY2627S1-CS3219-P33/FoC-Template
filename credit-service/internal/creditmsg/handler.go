package creditmsg

import (
	"context"
	"errors"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

// Commands is the part of the wallet service the handler calls. A real
// service must have a result recorder (wallet.Service.WithResultRecorder),
// or the results of the commands are never sent.
type Commands interface {
	Reserve(ctx context.Context, messageID string, c wallet.ReserveCommand) (wallet.Result, error)
	Adjust(ctx context.Context, messageID string, c wallet.AdjustCommand) (wallet.Result, error)
	Release(ctx context.Context, messageID string, c wallet.ReleaseCommand) (wallet.Result, error)
	Transfer(ctx context.Context, messageID string, c wallet.TransferCommand) (wallet.Result, error)
}

type Handler struct {
	commands Commands
}

func NewHandler(commands Commands) Handler {
	if commands == nil {
		panic("creditmsg: commands are required")
	}
	return Handler{commands: commands}
}

// Handle processes one command message. routingKey is the key the message
// arrived with. When it returns nil the command is applied, or was applied
// before, and its result is stored for delivery. Otherwise use
// DispositionOf(err) to decide what to do with the message.
func (h Handler) Handle(ctx context.Context, routingKey string, body []byte) error {
	env, err := decodeEnvelope(routingKey, body)
	if err != nil {
		return err
	}
	switch routingKey {
	case CommandReserve:
		var d reserveData
		if err := decodeData(env, &d); err != nil {
			return err
		}
		_, err = h.commands.Reserve(ctx, env.MessageID, wallet.ReserveCommand{
			OrderID: env.ErrandID, ErrandVersion: env.ErrandVersion,
			RequesterSubject: d.RequesterID, Amount: d.Amount,
		})
	case CommandAdjust:
		var d adjustData
		if err := decodeData(env, &d); err != nil {
			return err
		}
		_, err = h.commands.Adjust(ctx, env.MessageID, wallet.AdjustCommand{
			OrderID: env.ErrandID, ErrandVersion: env.ErrandVersion,
			RequesterSubject: d.RequesterID, NewAmount: d.NewAmount, Seq: d.Seq,
		})
	case CommandRelease:
		var d releaseData
		if err := decodeData(env, &d); err != nil {
			return err
		}
		_, err = h.commands.Release(ctx, env.MessageID, wallet.ReleaseCommand{
			OrderID: env.ErrandID, ErrandVersion: env.ErrandVersion,
			RequesterSubject: d.RequesterID,
		})
	case CommandTransfer:
		var d transferData
		if err := decodeData(env, &d); err != nil {
			return err
		}
		_, err = h.commands.Transfer(ctx, env.MessageID, wallet.TransferCommand{
			OrderID: env.ErrandID, ErrandVersion: env.ErrandVersion,
			RequesterSubject: d.RequesterID, CourierSubject: d.CourierID, Amount: d.Amount,
		})
	default:
		return undecodable("unknown routing key %q", routingKey)
	}
	return err
}

// Disposition is what the consumer does with a message after Handle.
type Disposition int

const (
	// Ack: done. The command is applied and its result is stored.
	Ack Disposition = iota
	// Reject: the message can never succeed. Reject without requeue, so it
	// goes to the dead-letter queue.
	Reject
	// Requeue: a temporary failure such as the database being down. Nack
	// with requeue; the queue's delivery limit stops an endless loop.
	Requeue
)

// DispositionOf maps the error from Handle to what the consumer should do.
func DispositionOf(err error) Disposition {
	switch {
	case err == nil:
		return Ack
	case errors.Is(err, ErrUndecodable),
		errors.Is(err, wallet.ErrInvalid),
		errors.Is(err, wallet.ErrConflict):
		return Reject
	default:
		return Requeue
	}
}
