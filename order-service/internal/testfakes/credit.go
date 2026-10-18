package testfakes

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// FakeCreditService plays credit-service in the proposed credit message
// contract (commit-plan-b.md, gate C1). It consumes credit commands, keeps
// balances and one reservation per errand, and returns result messages for
// the test to feed to the result consumer.
//
// It follows the contract's rules:
//   - Idempotent by messageId: a command seen before re-returns its original
//     results and changes nothing, even if that command's reply was dropped.
//   - Release tombstone: a release for an errand with no reservation records
//     a tombstone, and a later reserve is refused with RESERVATION_RELEASED.
//   - Adjust ordering: newAmount is absolute, and an adjust whose seq is not
//     newer than the last one applied is acknowledged with the current
//     amount and changes nothing.
//
// Each command can be scripted (Script) to be refused, fail, lose its reply
// or be answered twice. SetDown leaves commands unconsumed, as if
// credit-service were stopped.
type FakeCreditService struct {
	mu           sync.Mutex
	clock        errand.Clock
	balances     map[string]int
	reservations map[string]*CreditReservation
	tombstones   map[string]bool
	seen         map[string][]errand.Message
	scripts      map[string][]CreditScript
	received     []errand.Message
	down         bool
	consumed     map[*FakePublisher]int
}

// CreditReservation is the credit held for one errand.
type CreditReservation struct {
	UserID string
	Amount int
	State  CreditReservationState
	// LastSeq is the seq of the last applied adjust (0 before any).
	LastSeq int
}

type CreditReservationState string

const (
	ReservationActive      CreditReservationState = "ACTIVE"
	ReservationReleased    CreditReservationState = "RELEASED"
	ReservationTransferred CreditReservationState = "TRANSFERRED"
)

// CreditOutcome is how FakeCreditService answers one command.
type CreditOutcome int

const (
	// CreditApply processes the command by the contract's rules and answers
	// once. It is the default.
	CreditApply CreditOutcome = iota
	// CreditRefuse changes nothing and answers with the command's refusal
	// result and Code: credit.reservation_refused, credit.adjust_refused, or
	// credit.operation_failed for release and transfer, which have no
	// refusal.
	CreditRefuse
	// CreditNoReply processes the command but loses the reply. A redelivery
	// of the same messageId returns the result that was lost.
	CreditNoReply
	// CreditDuplicateReply processes the command and answers twice with the
	// same result message, as a broker redelivery would.
	CreditDuplicateReply
	// CreditOperationFailed changes nothing and answers
	// credit.operation_failed with Code.
	CreditOperationFailed
)

// CreditScript is one scripted answer.
type CreditScript struct {
	Outcome CreditOutcome
	// Code is the refusal or failure code; it defaults to
	// INSUFFICIENT_CREDITS for CreditRefuse and RESERVATION_NOT_ACTIVE for
	// CreditOperationFailed.
	Code string
}

// NewFakeCreditService returns a service with no accounts. clock stamps the
// results' occurredAt.
func NewFakeCreditService(clock errand.Clock) *FakeCreditService {
	return &FakeCreditService{
		clock:        clock,
		balances:     map[string]int{},
		reservations: map[string]*CreditReservation{},
		tombstones:   map[string]bool{},
		seen:         map[string][]errand.Message{},
		scripts:      map[string][]CreditScript{},
		consumed:     map[*FakePublisher]int{},
	}
}

// SetBalance creates or overwrites userID's available balance.
func (c *FakeCreditService) SetBalance(userID string, amount int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.balances[userID] = amount
}

// Balance returns userID's available balance (reserved credit excluded).
func (c *FakeCreditService) Balance(userID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.balances[userID]
}

// Reservation returns the reservation for errandID, if any.
func (c *FakeCreditService) Reservation(errandID string) (CreditReservation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.reservations[errandID]
	if !ok {
		return CreditReservation{}, false
	}
	return *r, true
}

// Tombstoned reports whether a release arrived for errandID before any
// reservation.
func (c *FakeCreditService) Tombstoned(errandID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tombstones[errandID]
}

// Received returns every command consumed, including redeliveries.
func (c *FakeCreditService) Received() []errand.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.received)
}

// Script queues answers for the next commands with routingKey (e.g.
// errand.CommandCreditReserve), used one per new messageId in order. A
// redelivered messageId never consumes a script.
func (c *FakeCreditService) Script(routingKey string, answers ...CreditScript) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scripts[routingKey] = append(c.scripts[routingKey], answers...)
}

// SetDown stops (true) or restarts (false) consumption by ConsumeFrom.
func (c *FakeCreditService) SetDown(down bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.down = down
}

// ConsumeFrom consumes the foc.commands messages pub has confirmed since the
// last call and returns the results in order. While the service is down it
// consumes nothing, so the commands wait as they would in the durable queue.
func (c *FakeCreditService) ConsumeFrom(pub *FakePublisher) ([]errand.Message, error) {
	c.mu.Lock()
	if c.down {
		c.mu.Unlock()
		return nil, nil
	}
	all := pub.Messages()
	pending := all[c.consumed[pub]:]
	c.consumed[pub] = len(all)
	c.mu.Unlock()

	var results []errand.Message
	for _, m := range pending {
		if m.Exchange != errand.ExchangeCommands {
			continue
		}
		out, err := c.Handle(m)
		if err != nil {
			return results, err
		}
		results = append(results, out...)
	}
	return results, nil
}

// Handle consumes one command and returns its result messages (none for a
// dropped reply). It ignores SetDown. An error means the message breaks the
// contract (unknown routing key, undecodable data) and would be
// dead-lettered.
func (c *FakeCreditService) Handle(cmd errand.Message) ([]errand.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cmd = cloneMessage(cmd)
	c.received = append(c.received, cmd)

	if !slices.Contains(errand.CreditCommands(), cmd.RoutingKey) {
		return nil, fmt.Errorf("testfakes: unknown credit command %q", cmd.RoutingKey)
	}
	if prior, ok := c.seen[cmd.Envelope.MessageID]; ok {
		return cloneMessages(prior), nil
	}
	var data errand.CreditCommand
	if err := json.Unmarshal(cmd.Envelope.Data, &data); err != nil {
		return nil, fmt.Errorf("testfakes: decode %s %q: %w", cmd.RoutingKey, cmd.Envelope.MessageID, err)
	}
	if data.ErrandID == "" {
		data.ErrandID = cmd.Envelope.ErrandID
	}

	script := CreditScript{Outcome: CreditApply}
	if queue := c.scripts[cmd.RoutingKey]; len(queue) > 0 {
		script, c.scripts[cmd.RoutingKey] = queue[0], queue[1:]
	}

	var resultType string
	var result errand.CreditResult
	switch script.Outcome {
	case CreditRefuse:
		resultType, result = c.refusal(cmd.RoutingKey, data, cmp.Or(script.Code, errand.CreditCodeInsufficientCredits))
	case CreditOperationFailed:
		resultType = errand.ResultCreditOperationFailed
		result = errand.CreditResult{Code: cmp.Or(script.Code, errand.CreditCodeReservationNotActive)}
	default:
		resultType, result = c.apply(cmd.RoutingKey, data)
	}

	msg := c.resultMessage(cmd, resultType, result)
	c.seen[cmd.Envelope.MessageID] = []errand.Message{msg}
	switch script.Outcome {
	case CreditNoReply:
		return nil, nil
	case CreditDuplicateReply:
		return []errand.Message{msg, cloneMessage(msg)}, nil
	default:
		return []errand.Message{msg}, nil
	}
}

// apply runs one command by the contract's rules. c.mu must be held.
func (c *FakeCreditService) apply(routingKey string, cmd errand.CreditCommand) (string, errand.CreditResult) {
	r := c.reservations[cmd.ErrandID]
	switch routingKey {
	case errand.CommandCreditReserve:
		switch {
		case c.tombstones[cmd.ErrandID]:
			return errand.ResultCreditReservationRefused, errand.CreditResult{Code: errand.CreditCodeReservationReleased}
		case r != nil && r.State == ReservationActive:
			return errand.ResultCreditReserved, errand.CreditResult{Amount: r.Amount}
		case r != nil:
			return errand.ResultCreditReservationRefused, errand.CreditResult{Code: errand.CreditCodeReservationReleased}
		}
		balance, ok := c.balances[cmd.RequesterID]
		if !ok {
			return errand.ResultCreditReservationRefused, errand.CreditResult{Code: errand.CreditCodeAccountNotFound}
		}
		if balance < cmd.Amount {
			return errand.ResultCreditReservationRefused, errand.CreditResult{Code: errand.CreditCodeInsufficientCredits}
		}
		c.balances[cmd.RequesterID] = balance - cmd.Amount
		c.reservations[cmd.ErrandID] = &CreditReservation{UserID: cmd.RequesterID, Amount: cmd.Amount, State: ReservationActive}
		return errand.ResultCreditReserved, errand.CreditResult{Amount: cmd.Amount}

	case errand.CommandCreditAdjust:
		if r == nil || r.State != ReservationActive {
			return errand.ResultCreditAdjustRefused, errand.CreditResult{Code: errand.CreditCodeReservationNotActive, Seq: cmd.Seq}
		}
		if cmd.Seq <= r.LastSeq {
			return errand.ResultCreditAdjusted, errand.CreditResult{Amount: r.Amount, Seq: cmd.Seq}
		}
		delta := cmd.NewAmount - r.Amount
		if delta > c.balances[r.UserID] {
			return errand.ResultCreditAdjustRefused, errand.CreditResult{Code: errand.CreditCodeInsufficientCredits, Seq: cmd.Seq}
		}
		c.balances[r.UserID] -= delta
		r.Amount, r.LastSeq = cmd.NewAmount, cmd.Seq
		return errand.ResultCreditAdjusted, errand.CreditResult{Amount: r.Amount, Seq: cmd.Seq}

	case errand.CommandCreditRelease:
		switch {
		case r == nil:
			c.tombstones[cmd.ErrandID] = true
		case r.State == ReservationActive:
			c.balances[r.UserID] += r.Amount
			r.State = ReservationReleased
		case r.State == ReservationTransferred:
			return errand.ResultCreditOperationFailed, errand.CreditResult{Code: errand.CreditCodeReservationAlreadyTransferred}
		}
		return errand.ResultCreditReleased, errand.CreditResult{}

	default: // transfer
		switch {
		case r == nil && c.tombstones[cmd.ErrandID], r != nil && r.State == ReservationReleased:
			return errand.ResultCreditOperationFailed, errand.CreditResult{Code: errand.CreditCodeReservationAlreadyReleased}
		case r == nil:
			return errand.ResultCreditOperationFailed, errand.CreditResult{Code: errand.CreditCodeReservationNotActive}
		case r.State == ReservationActive:
			c.balances[cmd.CourierID] += r.Amount
			r.State = ReservationTransferred
		}
		return errand.ResultCreditTransferred, errand.CreditResult{}
	}
}

// refusal is a scripted refusal: nothing changes.
func (c *FakeCreditService) refusal(routingKey string, cmd errand.CreditCommand, code string) (string, errand.CreditResult) {
	switch routingKey {
	case errand.CommandCreditReserve:
		return errand.ResultCreditReservationRefused, errand.CreditResult{Code: code}
	case errand.CommandCreditAdjust:
		return errand.ResultCreditAdjustRefused, errand.CreditResult{Code: code, Seq: cmd.Seq}
	default:
		return errand.ResultCreditOperationFailed, errand.CreditResult{Code: code}
	}
}

func (c *FakeCreditService) resultMessage(cmd errand.Message, resultType string, result errand.CreditResult) errand.Message {
	data, _ := json.Marshal(result) // CreditResult always encodes
	return errand.Message{
		Exchange:   errand.ExchangeEvents,
		RoutingKey: resultType,
		Envelope: errand.Envelope{
			MessageID:     cmd.Envelope.MessageID + "/" + resultType,
			CorrelationID: cmd.Envelope.MessageID,
			Type:          resultType,
			OccurredAt:    c.clock.Now(),
			ErrandID:      cmd.Envelope.ErrandID,
			ErrandVersion: cmd.Envelope.ErrandVersion,
			Data:          data,
		},
	}
}

func cloneMessages(ms []errand.Message) []errand.Message {
	out := make([]errand.Message, len(ms))
	for i, m := range ms {
		out[i] = cloneMessage(m)
	}
	return out
}
