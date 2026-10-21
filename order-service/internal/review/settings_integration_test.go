package review_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/review"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/migrations"
)

func TestSettingsUpdateAndAuditCommitTogether_F6_5(t *testing.T) {
	repo := migratedRepository(t)
	ctx := context.Background()
	service := review.NewSettingsService(review.NewRepositorySettings(repo), repo, slog.New(slog.NewTextHandler(io.Discard, nil)))

	settings := errand.DefaultSystemSettings()
	settings.CancellationFlag.Count = 10
	stored, err := service.Put(ctx, "auth0|admin-1", settings)
	require.NoError(t, err)
	require.Equal(t, settings, stored)

	read, err := service.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, 10, read.CancellationFlag.Count)
	entries, err := repo.ListAudit(ctx, opsrepo.AuditFilter{TargetType: review.AuditTargetSettings, Limit: 10})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "auth0|admin-1", entries[0].ActorID)
	require.Equal(t, "changed: cancellationFlag.count", entries[0].Reason)
	require.Equal(t, opsrepo.AuditSucceeded, entries[0].Outcome)

	// An audit row that cannot be written rolls the settings back.
	settings.CancellationFlag.Count = 30
	err = review.NewRepositorySettings(repo).UpdateAudited(ctx, settings, "auth0|admin-1", opsrepo.AuditRecord{
		ActorID: "auth0|admin-1", Action: review.AuditActionSettings, TargetType: review.AuditTargetSettings,
		TargetID: "settings", Outcome: "MAYBE",
	})
	require.ErrorIs(t, err, opsrepo.ErrInvalidArgument)
	read, err = service.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, 10, read.CancellationFlag.Count, "the update rolled back with the audit row")

	// A refused update stores nothing and is audited as REJECTED.
	settings.CancellationFlag.Count = 0
	_, err = service.Put(ctx, "auth0|admin-1", settings)
	require.Error(t, err)
	entries, err = repo.ListAudit(ctx, opsrepo.AuditFilter{TargetType: review.AuditTargetSettings, Limit: 10})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, opsrepo.AuditRejected, entries[0].Outcome)
	require.Equal(t, "VALIDATION_FAILED", entries[0].Reason)
}

// migratedRepository returns a repository on a new schema with every
// migration applied.
func migratedRepository(t *testing.T) *opsrepo.Repository {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL review integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	suffix := make([]byte, 8)
	_, err = rand.Read(suffix)
	require.NoError(t, err)
	schema := pgx.Identifier{"order_review_" + hex.EncodeToString(suffix)}.Sanitize()
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = conn.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = conn.Close(cleanupCtx)
	})
	_, err = conn.Exec(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	up, err := migrations.Up()
	require.NoError(t, err)
	_, err = conn.Exec(ctx, up)
	require.NoError(t, err)
	return opsrepo.New(conn)
}
