package messaging

import (
	"context"
	"log/slog"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// LogPublisher writes each message to a structured log instead of RabbitMQ.
// It is for unit tests and local experiments only: nothing consumes what it
// "publishes", so credit commands would never be answered. A running service
// always uses Publisher.
type LogPublisher struct{ logger *slog.Logger }

var _ errand.Publisher = (*LogPublisher)(nil)

func NewLogPublisher(logger *slog.Logger) *LogPublisher {
	if logger == nil {
		panic("messaging: logger is required")
	}
	return &LogPublisher{logger: logger}
}

// Publish validates m exactly as Publisher does and logs its routing and
// identity. The body is never logged, because it can hold user IDs.
func (p *LogPublisher) Publish(ctx context.Context, m errand.Message) error {
	if _, err := encode(m); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.logger.InfoContext(ctx, "message published to log",
		slog.String("exchange", m.Exchange), slog.String("routing_key", m.RoutingKey),
		slog.String("message_id", m.Envelope.MessageID), slog.String("errand_id", m.Envelope.ErrandID),
		slog.Int("errand_version", m.Envelope.ErrandVersion))
	return nil
}

// Ping always succeeds.
func (p *LogPublisher) Ping(context.Context) error { return nil }
