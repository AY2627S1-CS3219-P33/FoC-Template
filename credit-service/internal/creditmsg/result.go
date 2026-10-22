package creditmsg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database/outboxrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

// resultData is the data of a result message. Fields that a result does not
// use are omitted, so a released or transferred result has data {}.
type resultData struct {
	Amount int64  `json:"amount,omitempty"`
	Seq    int64  `json:"seq,omitempty"`
	Code   string `json:"code,omitempty"`
}

// BuildResult makes the result message for a command. The correlationId is
// the command's messageId, and the messageId is "{command messageId}:{type}",
// so a result published twice is a duplicate the receiver drops.
func BuildResult(reply wallet.Reply, result wallet.Result, occurredAt time.Time) (Envelope, error) {
	data, err := json.Marshal(resultData{Amount: result.Amount, Seq: result.Seq, Code: result.Code})
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		MessageID:     reply.CommandMessageID + ":" + result.Type,
		CorrelationID: reply.CommandMessageID,
		Type:          result.Type,
		OccurredAt:    occurredAt.UTC().Truncate(time.Second),
		ErrandID:      reply.OrderID,
		ErrandVersion: reply.ErrandVersion,
		Data:          data,
	}, nil
}

// OutboxRecorder stores results in the outbox table. It implements
// wallet.ResultRecorder.
type OutboxRecorder struct {
	now func() time.Time
}

func NewOutboxRecorder() OutboxRecorder { return OutboxRecorder{now: time.Now} }

func (r OutboxRecorder) RecordResult(ctx context.Context, tx pgx.Tx, reply wallet.Reply, result wallet.Result) error {
	if reply.ErrandVersion < 1 {
		return errors.New("creditmsg: a result message needs the errand version of the command")
	}
	env, err := BuildResult(reply, result, r.now())
	if err != nil {
		return err
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return outboxrepo.Enqueue(ctx, tx, env.MessageID, env.Type, payload)
}
