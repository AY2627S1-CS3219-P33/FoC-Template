package testfakes

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/messaging"
)

// FakePublisher is an errand.Publisher that keeps every confirmed message.
// Script failures with FailNext("Publish", …) or FailAlways("Publish", …); a
// failed call is not confirmed and is kept only in Attempts.
type FakePublisher struct {
	Faults

	mu        sync.Mutex
	confirmed []errand.Message
	attempts  []errand.Message
}

var _ errand.Publisher = (*FakePublisher)(nil)

func NewFakePublisher() *FakePublisher { return &FakePublisher{} }

func (p *FakePublisher) Publish(ctx context.Context, m errand.Message) error {
	m = cloneMessage(m)
	p.mu.Lock()
	p.attempts = append(p.attempts, m)
	p.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.fault("Publish"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.confirmed = append(p.confirmed, m)
	return nil
}

// Messages returns the confirmed messages in order.
func (p *FakePublisher) Messages() []errand.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.confirmed)
}

// MessagesTo returns the confirmed messages sent to one exchange.
func (p *FakePublisher) MessagesTo(exchange string) []errand.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []errand.Message
	for _, m := range p.confirmed {
		if m.Exchange == exchange {
			out = append(out, m)
		}
	}
	return out
}

// Attempts returns every Publish call, confirmed or not, in order.
func (p *FakePublisher) Attempts() []errand.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.attempts)
}

// MessageFromOutbox uses the production mapping so fake credit flows exercise
// the same envelope and routing rules as the real dispatcher.
func MessageFromOutbox(r OutboxRecord) (errand.Message, error) {
	data, err := json.Marshal(r.Payload)
	if err != nil {
		return errand.Message{}, fmt.Errorf("testfakes: encode payload: %w", err)
	}
	return messaging.MessageFromOutbox(msgrepo.Row{
		Kind: r.Kind, IdempotencyKey: r.IdempotencyKey, ErrandID: r.ErrandID,
		Payload: data, CreatedAt: r.At,
	})
}

func cloneMessage(m errand.Message) errand.Message {
	m.Envelope.Data = cloneRaw(m.Envelope.Data)
	return m
}
