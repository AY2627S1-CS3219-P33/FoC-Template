package opsrepo

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsgen"
)

// AuditOutcome is the stored admin_audit.outcome.
type AuditOutcome string

const (
	AuditSucceeded AuditOutcome = "SUCCEEDED"
	AuditRejected  AuditOutcome = "REJECTED"
	AuditFailed    AuditOutcome = "FAILED"
)

func (o AuditOutcome) valid() bool {
	return o == AuditSucceeded || o == AuditRejected || o == AuditFailed
}

// AuditRecord is one admin or service action to record (F6.5).
type AuditRecord struct {
	ActorID    string
	Action     string
	TargetType string
	TargetID   string
	// Reason is optional.
	Reason  string
	Outcome AuditOutcome
}

type AuditEntry struct {
	ID int64
	AuditRecord
	At time.Time
}

// AuditFilter selects audit entries. Zero fields match everything. BeforeID
// is the last ID of the previous page (0 for the first page).
type AuditFilter struct {
	ActorID    string
	Action     string
	TargetType string
	TargetID   string
	Outcome    AuditOutcome
	Since      time.Time // inclusive
	Until      time.Time // exclusive
	BeforeID   int64
	Limit      int
}

func (r *Repository) InsertAudit(ctx context.Context, record AuditRecord) (AuditEntry, error) {
	if record.ActorID == "" || record.Action == "" || record.TargetType == "" || record.TargetID == "" {
		return AuditEntry{}, invalid("actor, action, target type and target ID are required")
	}
	if !record.Outcome.valid() {
		return AuditEntry{}, invalid("%q is not an audit outcome", record.Outcome)
	}
	row, err := r.queries.InsertAudit(ctx, opsgen.InsertAuditParams{
		ActorID:    record.ActorID,
		Action:     record.Action,
		TargetType: record.TargetType,
		TargetID:   record.TargetID,
		Reason:     text(record.Reason),
		Outcome:    string(record.Outcome),
	})
	if err != nil {
		return AuditEntry{}, mapError(err)
	}
	return auditFromRow(row), nil
}

// ListAudit returns matching entries, newest first.
func (r *Repository) ListAudit(ctx context.Context, filter AuditFilter) ([]AuditEntry, error) {
	if err := checkLimit(filter.Limit); err != nil {
		return nil, err
	}
	if filter.Outcome != "" && !filter.Outcome.valid() {
		return nil, invalid("%q is not an audit outcome", filter.Outcome)
	}
	rows, err := r.queries.ListAudit(ctx, opsgen.ListAuditParams{
		ActorID:    text(filter.ActorID),
		Action:     text(filter.Action),
		TargetType: text(filter.TargetType),
		TargetID:   text(filter.TargetID),
		Outcome:    text(string(filter.Outcome)),
		Since:      timestamp(filter.Since),
		Until:      timestamp(filter.Until),
		BeforeID:   pgtype.Int8{Int64: filter.BeforeID, Valid: filter.BeforeID > 0},
		RowLimit:   int32(filter.Limit),
	})
	if err != nil {
		return nil, mapError(err)
	}
	entries := make([]AuditEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, auditFromRow(row))
	}
	return entries, nil
}

func auditFromRow(row opsgen.AdminAudit) AuditEntry {
	return AuditEntry{
		ID: row.ID,
		AuditRecord: AuditRecord{
			ActorID:    row.ActorID,
			Action:     row.Action,
			TargetType: row.TargetType,
			TargetID:   row.TargetID,
			Reason:     row.Reason.String,
			Outcome:    AuditOutcome(row.Outcome),
		},
		At: timeOrZero(row.At),
	}
}
