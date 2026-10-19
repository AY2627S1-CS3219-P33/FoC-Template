package opsrepo

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

func TestCountActiveErrandsForUserPerRole_F1_2_2_F1_2_3(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	const user, other = "auth0|user-1", "auth0|user-2"

	counts, err := repo.CountActiveErrandsForUser(ctx, user)
	require.NoError(t, err)
	require.Equal(t, ActiveErrandCounts{}, counts, "an unknown user has zero of both")

	// As requester: every active state counts; terminal states do not.
	for _, status := range errand.Statuses() {
		insertErrandFor(t, conn, user, string(status))
	}
	// As courier: only errands currently assigned to the user, in an active
	// state that has a courier. A terminal errand the user delivered does
	// not count.
	for _, status := range []string{"ASSIGNED", "PICKED_UP", "DELIVERED", "REVIEW_REQUIRED", "COMPLETED", "REFUNDED"} {
		insertErrandWithCourier(t, conn, other, user, status)
	}
	// Another courier's errand and another requester's open errand do not
	// count for user.
	insertErrandWithCourier(t, conn, other, "auth0|courier-3", "ASSIGNED")
	insertErrandFor(t, conn, other, "OPEN")

	counts, err = repo.CountActiveErrandsForUser(ctx, user)
	require.NoError(t, err)
	require.Equal(t, ActiveErrandCounts{AsRequester: 6, AsCourier: 4}, counts)

	// insertErrandFor assigns "courier-1" to its courier states, so user
	// requested four errands that courier-1 is delivering.
	counts, err = repo.CountActiveErrandsForUser(ctx, "courier-1")
	require.NoError(t, err)
	require.Equal(t, ActiveErrandCounts{AsRequester: 0, AsCourier: 4}, counts)

	counts, err = repo.CountActiveErrandsForUser(ctx, other)
	require.NoError(t, err)
	require.Equal(t, ActiveErrandCounts{AsRequester: 6, AsCourier: 0}, counts)

	_, err = repo.CountActiveErrandsForUser(ctx, "")
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func TestCountActiveErrandsForUserCountsBothRolesOnce(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	const user = "auth0|both"

	insertErrandFor(t, conn, user, "OPEN")
	insertErrandWithCourier(t, conn, "auth0|someone", user, "PICKED_UP")
	insertErrandWithCourier(t, conn, "auth0|someone", user, "EXPIRED")

	counts, err := repo.CountActiveErrandsForUser(ctx, user)
	require.NoError(t, err)
	require.Equal(t, ActiveErrandCounts{AsRequester: 1, AsCourier: 1}, counts)
}

// insertErrandWithCourier is insertErrandFor with a chosen courier.
func insertErrandWithCourier(t *testing.T, conn *pgx.Conn, requester, courier, status string) {
	t.Helper()
	id := insertErrandFor(t, conn, requester, status)
	_, err := conn.Exec(context.Background(), `UPDATE errands SET courier_id = $2 WHERE id = $1`, id, courier)
	require.NoError(t, err)
}
