// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package opsrepo

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestOverviewEmptyDatabase(t *testing.T) {
	repo, _ := migratedRepository(t)

	counts, err := repo.Overview(context.Background())

	require.NoError(t, err)
	require.Equal(t, OverviewCounts{}, counts)
	require.Nil(t, counts.OldestReviewAt)
}

func TestOverviewCountsEachStatusOnce(t *testing.T) {
	repo, conn := migratedRepository(t)
	for status, n := range map[string]int{
		"PENDING_RESERVATION": 2, "OPEN": 3, "ASSIGNED": 2, "PICKED_UP": 1, "DELIVERED": 4,
		"COMPLETED": 5, "REFUNDED": 1, "REJECTED": 1, "CANCELLED": 2, "EXPIRED": 3,
	} {
		for range n {
			insertErrandFor(t, conn, "requester-1", status)
		}
	}

	counts, err := repo.Overview(context.Background())

	require.NoError(t, err)
	require.Equal(t, 7, counts.Ongoing, "ASSIGNED, PICKED_UP and DELIVERED")
	require.Equal(t, 3, counts.WaitingForCourier, "OPEN only")
	require.Zero(t, counts.OpenReviews)
	require.Nil(t, counts.OldestReviewAt)
}

func TestOverviewOpenReviews(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

	// Two open reviews; the older one is not the first inserted.
	newer := insertErrandFor(t, conn, "requester-1", "REVIEW_REQUIRED")
	older := insertErrandFor(t, conn, "requester-1", "REVIEW_REQUIRED")
	insertReview(t, conn, newer, base.Add(-time.Hour), false)
	insertReview(t, conn, older, base.Add(-3*time.Hour), false)
	// A resolved review on an errand that left review is not counted.
	resolved := insertErrandFor(t, conn, "requester-1", "COMPLETED")
	insertReview(t, conn, resolved, base.Add(-10*time.Hour), true)
	// An open review row whose errand is no longer REVIEW_REQUIRED is not counted.
	left := insertErrandFor(t, conn, "requester-1", "REFUNDED")
	insertReview(t, conn, left, base.Add(-20*time.Hour), false)

	counts, err := repo.Overview(ctx)

	require.NoError(t, err)
	require.Equal(t, 2, counts.OpenReviews)
	require.NotNil(t, counts.OldestReviewAt)
	require.True(t, counts.OldestReviewAt.Equal(base.Add(-3*time.Hour)), "got %s", counts.OldestReviewAt)
	require.Zero(t, counts.Ongoing, "REVIEW_REQUIRED is not ongoing")
	require.Zero(t, counts.WaitingForCourier)
}

func insertReview(t *testing.T, conn *pgx.Conn, errandID string, createdAt time.Time, resolved bool) {
	t.Helper()
	query := `INSERT INTO errand_reviews (errand_id, reason, created_at) VALUES ($1, 'COURIER_ISSUE', $2)`
	if resolved {
		query = `INSERT INTO errand_reviews (errand_id, reason, created_at, resolution, resolver_id, resolution_reason, resolved_at)
		         VALUES ($1, 'COURIER_ISSUE', $2::timestamptz, 'COMPLETED', 'admin-1', 'checked', $2::timestamptz + interval '1 hour')`
	}
	_, err := conn.Exec(context.Background(), query, errandID, createdAt)
	require.NoError(t, err)
}
