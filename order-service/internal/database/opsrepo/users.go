// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-22
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package opsrepo

import (
	"context"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsgen"
)

// ActiveErrandCounts is how many active (non-terminal) errands one user
// takes part in, by role.
type ActiveErrandCounts struct {
	// AsRequester counts the errands the user requested, each holding a
	// credit reservation (F1.2.2).
	AsRequester int
	// AsCourier counts the errands currently assigned to the user (F1.2.3).
	AsCourier int
}

// CountActiveErrandsForUser counts userID's active errands as requester and
// as courier, for user-service's account-deletion check. An unknown user has
// zero of both.
func (r *Repository) CountActiveErrandsForUser(ctx context.Context, userID string) (ActiveErrandCounts, error) {
	if userID == "" {
		return ActiveErrandCounts{}, invalid("userId is required")
	}
	row, err := r.queries.CountActiveErrandsForUser(ctx, opsgen.CountActiveErrandsForUserParams{
		UserID:         userID,
		ActiveStatuses: activeStatuses(),
	})
	if err != nil {
		return ActiveErrandCounts{}, mapError(err)
	}
	return ActiveErrandCounts{AsRequester: int(row.AsRequester), AsCourier: int(row.AsCourier)}, nil
}
