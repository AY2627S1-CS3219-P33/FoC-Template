package opsrepo

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

var transitionsT0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func at(minute int) time.Time { return transitionsT0.Add(time.Duration(minute) * time.Minute) }

// transitionLog is two errands' histories:
//
//	E1, requested by req-a: submit, accepted by cour-b, picked up, issue
//	    reported, refunded by admin-1 (minutes 1-5).
//	E2, requested by req-b: submit, accepted by cour-c, withdrawn,
//	    cancelled (minutes 6-9).
type transitionLog struct {
	e1, e2 string
	ids    map[int]int64 // minute → transition ID
}

func seedTransitionLog(t *testing.T, conn *pgx.Conn) transitionLog {
	t.Helper()
	log := transitionLog{
		e1:  insertErrandFor(t, conn, "req-a", "REFUNDED"),
		e2:  insertErrandFor(t, conn, "req-b", "CANCELLED"),
		ids: map[int]int64{},
	}
	rows := []struct {
		minute                    int
		errandID, from, to, event string
		actorType, actor, courier string
	}{
		{1, log.e1, "", "PENDING_RESERVATION", "submit", "REQUESTER", "req-a", ""},
		{2, log.e1, "OPEN", "ASSIGNED", "accept", "COURIER", "cour-b", "cour-b"},
		{3, log.e1, "ASSIGNED", "PICKED_UP", "pick_up", "COURIER", "cour-b", "cour-b"},
		{4, log.e1, "PICKED_UP", "REVIEW_REQUIRED", "report_issue", "COURIER", "cour-b", "cour-b"},
		{5, log.e1, "REVIEW_REQUIRED", "REFUNDED", "admin_refund", "ADMIN", "admin-1", "cour-b"},
		{6, log.e2, "", "PENDING_RESERVATION", "submit", "REQUESTER", "req-b", ""},
		{7, log.e2, "OPEN", "ASSIGNED", "accept", "COURIER", "cour-c", "cour-c"},
		{8, log.e2, "ASSIGNED", "OPEN", "withdraw", "COURIER", "cour-c", "cour-c"},
		{9, log.e2, "OPEN", "CANCELLED", "cancel", "REQUESTER", "req-b", ""},
	}
	for _, row := range rows {
		log.ids[row.minute] = insertTransition(t, conn, row.errandID, row.from, row.to, row.event, row.actorType, row.actor, row.courier, at(row.minute))
	}
	return log
}

func (l transitionLog) minutes(transitions []Transition) []int {
	byID := map[int64]int{}
	for minute, id := range l.ids {
		byID[id] = minute
	}
	minutes := []int{}
	for _, tr := range transitions {
		minutes = append(minutes, byID[tr.ID])
	}
	return minutes
}

func TestListTransitionsFilters_F6_2_2(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	log := seedTransitionLog(t, conn)

	for _, tc := range []struct {
		name   string
		filter TransitionFilter
		want   []int
	}{
		{"no filter, newest first", TransitionFilter{}, []int{9, 8, 7, 6, 5, 4, 3, 2, 1}},
		{"errand", TransitionFilter{ErrandID: log.e1}, []int{5, 4, 3, 2, 1}},
		{"requester sees their whole errand", TransitionFilter{UserID: "req-a"}, []int{5, 4, 3, 2, 1}},
		{"courier sees rows while they were courier", TransitionFilter{UserID: "cour-b"}, []int{5, 4, 3, 2}},
		{"withdrawn courier keeps their own rows", TransitionFilter{UserID: "cour-c"}, []int{8, 7}},
		{"admin sees what they did", TransitionFilter{UserID: "admin-1"}, []int{5}},
		{"unknown user", TransitionFilter{UserID: "nobody"}, []int{}},
		{"type", TransitionFilter{ToStatus: errand.StatusCancelled}, []int{9}},
		{"type and user", TransitionFilter{ToStatus: errand.StatusReviewRequired, UserID: "req-a"}, []int{4}},
		{"errand and user", TransitionFilter{ErrandID: log.e2, UserID: "cour-c"}, []int{8, 7}},
		{"errand and another user", TransitionFilter{ErrandID: log.e1, UserID: "req-b"}, []int{}},
		{"errand and type", TransitionFilter{ErrandID: log.e2, ToStatus: errand.StatusAssigned}, []int{7}},
		{"time range, from inclusive and to exclusive", TransitionFilter{From: at(3), To: at(6)}, []int{5, 4, 3}},
		{"time range and user", TransitionFilter{UserID: "req-b", From: at(7)}, []int{9, 8, 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.filter.Limit = 50
			transitions, err := repo.ListTransitions(ctx, tc.filter)
			require.NoError(t, err)
			require.Equal(t, tc.want, log.minutes(transitions))
		})
	}

	// Row contents.
	transitions, err := repo.ListTransitions(ctx, TransitionFilter{ErrandID: log.e1, ToStatus: errand.StatusPendingReservation, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, []Transition{{
		ID: log.ids[1], ErrandID: log.e1, ToStatus: errand.StatusPendingReservation,
		Event: errand.EventSubmit, ActorType: errand.ActorRequester, ActorID: "req-a",
		ErrandVersion: 1, At: at(1),
	}}, normalizeTimes(transitions))
}

func TestListTransitionsKeysetPagination_F6_2_1(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	log := seedTransitionLog(t, conn)
	// Two more rows at minute 9, so the cursor has to break a tie on at.
	tie1 := insertTransition(t, conn, log.e2, "CANCELLED", "CANCELLED", "cancel", "REQUESTER", "req-b", "", at(9))
	tie2 := insertTransition(t, conn, log.e2, "CANCELLED", "CANCELLED", "cancel", "REQUESTER", "req-b", "", at(9))

	var seen []int64
	var before *TransitionCursor
	for page := 0; ; page++ {
		require.Less(t, page, 10, "pagination does not terminate")
		transitions, err := repo.ListTransitions(ctx, TransitionFilter{Before: before, Limit: 4})
		require.NoError(t, err)
		for _, tr := range transitions {
			seen = append(seen, tr.ID)
		}
		if len(transitions) < 4 {
			break
		}
		last := transitions[len(transitions)-1]
		before = &TransitionCursor{At: last.At, ID: last.ID}
	}
	require.Equal(t, []int64{
		tie2, tie1, log.ids[9], log.ids[8], log.ids[7], log.ids[6], log.ids[5], log.ids[4], log.ids[3], log.ids[2], log.ids[1],
	}, seen, "every row once, newest first, ties by ID")

	// The same cursor works on each access path.
	for _, filter := range []TransitionFilter{
		{ErrandID: log.e1}, {UserID: "req-a"}, {ToStatus: errand.StatusRefunded}, {},
	} {
		filter.Before = &TransitionCursor{At: at(5), ID: log.ids[5]}
		filter.Limit = 50
		transitions, err := repo.ListTransitions(ctx, filter)
		require.NoError(t, err)
		for _, tr := range transitions {
			require.True(t, tr.At.Before(at(5)), "row after the cursor: %+v", tr)
		}
	}
}

func TestListTransitionsRejectsBadFilters(t *testing.T) {
	repo, _ := migratedRepository(t)
	ctx := context.Background()
	for _, filter := range []TransitionFilter{
		{Limit: 0},
		{Limit: MaxPageSize + 1},
		{Limit: 10, ToStatus: "LOST"},
		{Limit: 10, ErrandID: "not-a-uuid"},
	} {
		_, err := repo.ListTransitions(ctx, filter)
		require.ErrorIs(t, err, ErrInvalidArgument, "%+v", filter)
	}
}

func TestActivityCountsAtBelowAndAboveThreshold_F6_3_1_F6_3_2(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	e := insertErrandFor(t, conn, "req-a", "OPEN")
	since := at(10)

	withdraw := func(courier, event, actorType, actor string, minute int) {
		insertTransition(t, conn, e, "ASSIGNED", "OPEN", event, actorType, actor, courier, at(minute))
	}
	// cour-a: 3 in the window, one forced. cour-b: 2 in the window, 1 before.
	withdraw("cour-a", "withdraw", "COURIER", "cour-a", 11)
	withdraw("cour-a", "withdraw", "COURIER", "cour-a", 12)
	withdraw("cour-a", "pickup_deadline_passed", "SYSTEM", "", 13)
	withdraw("cour-b", "withdraw", "COURIER", "cour-b", 9)
	withdraw("cour-b", "withdraw", "COURIER", "cour-b", 10)
	withdraw("cour-b", "withdraw", "COURIER", "cour-b", 14)
	// An accept is not a withdrawal.
	insertTransition(t, conn, e, "OPEN", "ASSIGNED", "accept", "COURIER", "cour-b", "cour-b", at(15))

	for _, tc := range []struct {
		minCount int
		want     []ActivityCount
	}{
		{1, []ActivityCount{{"cour-a", 3}, {"cour-b", 2}}},
		{2, []ActivityCount{{"cour-a", 3}, {"cour-b", 2}}},
		{3, []ActivityCount{{"cour-a", 3}}},
		{4, []ActivityCount{}},
	} {
		counts, err := repo.ListWithdrawalCounts(ctx, since, tc.minCount, 500)
		require.NoError(t, err)
		require.Equal(t, tc.want, counts, "minCount %d", tc.minCount)
	}

	cancel := func(requester string, minute int) {
		insertTransition(t, conn, insertErrandFor(t, conn, requester, "CANCELLED"), "OPEN", "CANCELLED", "cancel", "REQUESTER", requester, "", at(minute))
	}
	cancel("req-x", 11)
	cancel("req-x", 12)
	cancel("req-y", 13)
	cancel("req-y", 9) // before the window
	for _, tc := range []struct {
		minCount int
		want     []ActivityCount
	}{
		{1, []ActivityCount{{"req-x", 2}, {"req-y", 1}}},
		{2, []ActivityCount{{"req-x", 2}}},
		{3, []ActivityCount{}},
	} {
		counts, err := repo.ListCancellationCounts(ctx, since, tc.minCount, 500)
		require.NoError(t, err)
		require.Equal(t, tc.want, counts, "minCount %d", tc.minCount)
	}

	counts, err := repo.ListWithdrawalCounts(ctx, since, 1, 1)
	require.NoError(t, err)
	require.Equal(t, []ActivityCount{{"cour-a", 3}}, counts, "limit keeps the highest")

	_, err = repo.ListWithdrawalCounts(ctx, time.Time{}, 1, 1)
	require.ErrorIs(t, err, ErrInvalidArgument)
	_, err = repo.ListCancellationCounts(ctx, since, 0, 1)
	require.ErrorIs(t, err, ErrInvalidArgument)
	_, err = repo.ListCancellationCounts(ctx, since, 1, MaxActivityRows+1)
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func insertErrandFor(t *testing.T, conn *pgx.Conn, requester, status string) string {
	t.Helper()
	var courier *string
	if status == "ASSIGNED" || status == "PICKED_UP" || status == "DELIVERED" || status == "REVIEW_REQUIRED" {
		c := "courier-1"
		courier = &c
	}
	var id string
	require.NoError(t, conn.QueryRow(context.Background(), `
		INSERT INTO errands (
			status, requester_id, courier_id, supplier_id, supplier_version_id,
			delivery_building_id, delivery_building_snapshot, delivery_detail, items,
			contact_name, contact_mobile, reward, acceptance_deadline, create_idempotency_key
		) VALUES (
			$1, $2, $3, gen_random_uuid(), gen_random_uuid(),
			gen_random_uuid(), '{}', 'Room 1', '[{"name": "coffee"}]',
			'Contact', '91234567', 10, clock_timestamp() + interval '1 hour', gen_random_uuid()::text
		) RETURNING id::text`, status, requester, courier).Scan(&id))
	return id
}

func insertTransition(t *testing.T, conn *pgx.Conn, errandID, from, to, event, actorType, actor, courier string, when time.Time) int64 {
	t.Helper()
	nullable := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	var id int64
	require.NoError(t, conn.QueryRow(context.Background(), `
		INSERT INTO errand_transitions (errand_id, from_status, to_status, event, actor_type, actor_id, courier_id, errand_version, at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8)
		RETURNING id`,
		errandID, nullable(from), to, event, actorType, nullable(actor), nullable(courier), when).Scan(&id))
	return id
}

func normalizeTimes(transitions []Transition) []Transition {
	for i := range transitions {
		transitions[i].At = transitions[i].At.UTC()
	}
	return transitions
}
