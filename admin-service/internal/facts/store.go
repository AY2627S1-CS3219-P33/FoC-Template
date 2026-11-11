// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package facts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/database/admingen"
)

// ErrInvalid means an argument can never be stored: an event that fails
// Validate, or a malformed ID.
var ErrInvalid = errors.New("facts: invalid argument")

// DB is what the store needs from a connection: a *pgxpool.Pool in the
// service, or a single *pgx.Conn.
type DB interface {
	admingen.DBTX
	BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error)
}

// Store writes the read model. It is safe for concurrent use when its DB is.
type Store struct {
	db      DB
	queries *admingen.Queries
}

func NewStore(db DB) *Store {
	return &Store{db: db, queries: admingen.New(db)}
}

// Apply records one event, in one transaction: the transition fact, and the
// merge into the errand's row. It reports false, having changed nothing, when
// the messageId is already stored, which is what a redelivery, a replay after
// a crash between commit and ack, and a backfill of an event the stream
// already brought all look like. The caller acknowledges the message only
// after Apply returns.
func (s *Store) Apply(ctx context.Context, e Event) (applied bool, err error) {
	if err := e.Validate(); err != nil {
		return false, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	errandID, err := parseUUID(e.ErrandID)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.queries.WithTx(tx)

	inserted, err := q.InsertTransitionFact(ctx, admingen.InsertTransitionFactParams{
		MessageID:     e.MessageID,
		ErrandID:      errandID,
		ErrandVersion: int32(e.Version),
		EventType:     e.Type,
		FromStatus:    text(string(e.From)),
		ToStatus:      string(e.To),
		CourierID:     text(e.CourierID),
		Reward:        int32(e.Reward),
		OccurredAt:    timestamp(e.OccurredAt),
	})
	if err != nil {
		return false, err
	}
	if inserted == 0 {
		return false, nil
	}

	fact := NewFact(e)
	created, err := q.InsertErrandFact(ctx, admingen.InsertErrandFactParams{
		ErrandID:        errandID,
		Status:          string(fact.Status),
		Version:         int32(fact.Version),
		LastEventAt:     timestamp(fact.LastEventAt),
		CourierID:       text(fact.CourierID),
		Reward:          int32(fact.Reward),
		OpenedAt:        timestampPtr(fact.OpenedAt),
		FirstAcceptedAt: timestampPtr(fact.FirstAcceptedAt),
		TerminalStatus:  text(string(fact.TerminalStatus)),
		TerminalAt:      timestampPtr(fact.TerminalAt),
		EnrichmentDue:   fact.EnrichmentDue,
	})
	if err != nil {
		return false, err
	}
	if created == 0 {
		// The errand has a row: merge into it while holding it, so another
		// consumer's event for the same errand waits its turn.
		row, err := q.LockErrandFact(ctx, errandID)
		if err != nil {
			return false, err
		}
		merged := factOf(row).Merge(e)
		if err := q.UpdateErrandFact(ctx, admingen.UpdateErrandFactParams{
			ErrandID:        errandID,
			Status:          string(merged.Status),
			Version:         int32(merged.Version),
			LastEventAt:     timestamp(merged.LastEventAt),
			CourierID:       text(merged.CourierID),
			Reward:          int32(merged.Reward),
			OpenedAt:        timestampPtr(merged.OpenedAt),
			FirstAcceptedAt: timestampPtr(merged.FirstAcceptedAt),
			TerminalStatus:  text(string(merged.TerminalStatus)),
			TerminalAt:      timestampPtr(merged.TerminalAt),
			EnrichmentDue:   merged.EnrichmentDue,
		}); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

// Enrichment is what order-service's errand export says about an errand's
// supplier and delivery building. Names are nil when unknown.
type Enrichment struct {
	ErrandID             string
	SupplierID           string
	SupplierName         *string
	DeliveryBuildingID   string
	DeliveryBuildingName *string
}

// EnrichmentDue returns up to limit errands that owe an enrichment: never
// tried, or last tried before retryBefore. Oldest events first.
func (s *Store) EnrichmentDue(ctx context.Context, retryBefore time.Time, limit int) ([]string, error) {
	if limit < 1 {
		return nil, fmt.Errorf("%w: limit must be positive", ErrInvalid)
	}
	ids, err := s.queries.ListEnrichmentDue(ctx, admingen.ListEnrichmentDueParams{
		RetryBefore: timestamp(retryBefore), RowLimit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, uuidString(id))
	}
	return out, nil
}

// StoreEnrichment replaces the errand's supplier and building and records
// that enrichment ran at time at. The errand stays due while the supplier
// name is unknown. It reports false when the errand has no row.
func (s *Store) StoreEnrichment(ctx context.Context, e Enrichment, at time.Time) (bool, error) {
	errandID, err := parseUUID(e.ErrandID)
	if err != nil {
		return false, err
	}
	params := admingen.StoreEnrichmentParams{
		ErrandID:     errandID,
		SupplierName: textPtr(e.SupplierName), DeliveryBuildingName: textPtr(e.DeliveryBuildingName),
		EnrichedAt: timestamp(at),
	}
	if e.SupplierID != "" {
		if params.SupplierID, err = parseUUID(e.SupplierID); err != nil {
			return false, err
		}
	}
	if e.DeliveryBuildingID != "" {
		if params.DeliveryBuildingID, err = parseUUID(e.DeliveryBuildingID); err != nil {
			return false, err
		}
	}
	rows, err := s.queries.StoreEnrichment(ctx, params)
	return rows > 0, err
}

// MarkEnrichmentTried records that enrichment ran at time at for an errand it
// got no data for, leaving what is stored as it is.
func (s *Store) MarkEnrichmentTried(ctx context.Context, errandID string, at time.Time) error {
	id, err := parseUUID(errandID)
	if err != nil {
		return err
	}
	_, err = s.queries.MarkEnrichmentTried(ctx, admingen.MarkEnrichmentTriedParams{ErrandID: id, EnrichedAt: timestamp(at)})
	return err
}

func factOf(row admingen.LockErrandFactRow) Fact {
	return Fact{
		ErrandID:        uuidString(row.ErrandID),
		Status:          Status(row.Status),
		Version:         int(row.Version),
		LastEventAt:     row.LastEventAt.Time,
		CourierID:       row.CourierID.String,
		Reward:          int(row.Reward),
		OpenedAt:        timePtr(row.OpenedAt),
		FirstAcceptedAt: timePtr(row.FirstAcceptedAt),
		TerminalStatus:  Status(row.TerminalStatus.String),
		TerminalAt:      timePtr(row.TerminalAt),
		EnrichmentDue:   row.EnrichmentDue,
	}
}

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: %q is not a UUID", ErrInvalid, value)
	}
	return id, nil
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	value, _ := id.Value()
	s, _ := value.(string)
	return s
}

func text(value string) pgtype.Text { return pgtype.Text{String: value, Valid: value != ""} }

func textPtr(value *string) pgtype.Text {
	if value == nil || *value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: !value.IsZero()}
}

func timestampPtr(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return timestamp(*value)
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}
