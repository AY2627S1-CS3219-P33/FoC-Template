package metrics

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/testdb"
)

const (
	supplierA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	supplierB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	supplierC = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	bldgCOM1  = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	bldgUTown = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
)

var seq int

func nextID() string {
	seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
}

// sg parses a Singapore wall-clock time.
func sg(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, sgt)
	if err != nil {
		panic(err)
	}
	return t
}

type errandSpec struct {
	opened, accepted, terminal time.Time
	terminalStatus             string
	reward                     int
	supplier, supplierName     string
	building, buildingName     string
}

func seedErrand(t *testing.T, db testdb.DB, e errandSpec) string {
	t.Helper()
	id := nextID()
	status := "OPEN"
	if e.terminalStatus != "" {
		status = e.terminalStatus
	}
	nullable := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	nullTime := func(v time.Time) *time.Time {
		if v.IsZero() {
			return nil
		}
		return &v
	}
	reward := e.reward
	if reward == 0 {
		reward = 5
	}
	var terminal *string
	if e.terminalStatus != "" {
		terminal = &e.terminalStatus
	}
	_, err := db.Conn.Exec(context.Background(), `
		INSERT INTO errand_facts (errand_id, status, version, last_event_at, reward, opened_at, first_accepted_at,
			terminal_status, terminal_at, supplier_id, supplier_name, delivery_building_id, delivery_building_name)
		VALUES ($1, $2, 5, now(), $3, $4, $5, $6, $7, $8::uuid, $9, $10::uuid, $11)`,
		id, status, reward, nullTime(e.opened), nullTime(e.accepted), terminal, nullTime(e.terminal),
		nullable(e.supplier), nullable(e.supplierName), nullable(e.building), nullable(e.buildingName))
	require.NoError(t, err)
	return id
}

func seedAccept(t *testing.T, db testdb.DB, at time.Time, courier string) {
	t.Helper()
	seedTransition(t, db, "errand.accepted", "OPEN", "ASSIGNED", at, courier)
}

func seedTransition(t *testing.T, db testdb.DB, eventType, from, to string, at time.Time, courier string) {
	t.Helper()
	id := nextID()
	var c *string
	if courier != "" {
		c = &courier
	}
	_, err := db.Conn.Exec(context.Background(), `
		INSERT INTO transition_facts (message_id, errand_id, errand_version, event_type, from_status, to_status, courier_id, reward, occurred_at)
		VALUES ($1, $2, 3, $3, $4, $5, $6, 5, $7)`, id+":"+eventType, id, eventType, from, to, c, at)
	require.NoError(t, err)
}

func newService(t *testing.T) (*Service, testdb.DB) {
	t.Helper()
	db := testdb.Migrated(t)
	return NewService(db.Conn, sgt), db
}

func period(t *testing.T, from, to string) Period {
	t.Helper()
	p, err := NewPeriod(from, to, time.Now(), sgt)
	require.NoError(t, err)
	return p
}

func TestErrandsPerDayCountsByOpenedTimeInSingaporeDays(t *testing.T) {
	svc, db := newService(t)
	// 2026-03-01 16:30 UTC is 2026-03-02 00:30 in Singapore.
	seedErrand(t, db, errandSpec{opened: sg("2026-03-02 00:30")})
	seedErrand(t, db, errandSpec{opened: sg("2026-03-01 23:59")})
	seedErrand(t, db, errandSpec{opened: sg("2026-03-01 08:00")})
	seedErrand(t, db, errandSpec{opened: sg("2026-03-02 23:59")})
	seedErrand(t, db, errandSpec{opened: sg("2026-03-04 00:00")}) // after the period
	seedErrand(t, db, errandSpec{opened: sg("2026-02-28 23:59")}) // before the period
	seedErrand(t, db, errandSpec{})                               // never opened (rejected at reservation)

	days, err := svc.ErrandsPerDay(context.Background(), period(t, "2026-03-01", "2026-03-03"))

	require.NoError(t, err)
	require.Equal(t, []DayCount{
		{"2026-03-01", 2},
		{"2026-03-02", 2},
		{"2026-03-03", 0},
	}, days, "a day with no errands is listed with zero; 00:30 SGT counts on its Singapore date")
}

func TestTimeToAcceptMedianAndP90(t *testing.T) {
	svc, db := newService(t)
	open := sg("2026-03-02 09:00")
	for _, seconds := range []int{60, 120, 180, 240, 600} {
		seedErrand(t, db, errandSpec{opened: open, accepted: open.Add(time.Duration(seconds) * time.Second)})
	}
	seedErrand(t, db, errandSpec{opened: open})                                   // never accepted: excluded
	seedErrand(t, db, errandSpec{accepted: open.Add(time.Minute)})                // never opened: excluded
	seedErrand(t, db, errandSpec{opened: open, accepted: sg("2026-03-09 09:00")}) // accepted outside the period

	result, err := svc.TimeToAccept(context.Background(), period(t, "2026-03-02", "2026-03-02"))

	require.NoError(t, err)
	require.Equal(t, 5, result.Accepted)
	require.InDelta(t, 180, *result.MedianSeconds, 0.001)
	require.InDelta(t, 456, *result.P90Seconds, 0.001, "interpolated between 240 and 600")
}

func TestTimeToAcceptWithNothingAccepted(t *testing.T) {
	svc, db := newService(t)
	seedErrand(t, db, errandSpec{opened: sg("2026-03-02 09:00")})

	result, err := svc.TimeToAccept(context.Background(), period(t, "2026-03-02", "2026-03-02"))

	require.NoError(t, err)
	require.Zero(t, result.Accepted)
	require.Nil(t, result.MedianSeconds)
	require.Nil(t, result.P90Seconds)
}

func TestOutcomesCoverAllFiveTerminalStatusesAndAddUp(t *testing.T) {
	svc, db := newService(t)
	finish := func(status string, n int, at string) {
		for range n {
			seedErrand(t, db, errandSpec{terminalStatus: status, terminal: sg(at)})
		}
	}
	finish("COMPLETED", 6, "2026-03-02 10:00")
	finish("CANCELLED", 1, "2026-03-02 11:00")
	finish("EXPIRED", 2, "2026-03-02 12:00")
	finish("REJECTED", 0, "2026-03-02 13:00")
	finish("REFUNDED", 1, "2026-03-03 00:00") // finished on the next day: outside
	finish("COMPLETED", 3, "2026-03-05 10:00")
	seedErrand(t, db, errandSpec{opened: sg("2026-03-02 09:00")}) // still open: not finished

	result, err := svc.Outcomes(context.Background(), period(t, "2026-03-02", "2026-03-02"))

	require.NoError(t, err)
	require.Equal(t, 9, result.Finished)
	require.Equal(t, []Outcome{
		{"COMPLETED", 6, 6.0 / 9},
		{"CANCELLED", 1, 1.0 / 9},
		{"EXPIRED", 2, 2.0 / 9},
		{"REJECTED", 0, 0},
		{"REFUNDED", 0, 0},
	}, result.Items)
	sum := 0.0
	for _, item := range result.Items {
		sum += item.Rate
	}
	require.InDelta(t, 1.0, sum, 1e-9, "the shares add to 100%")
}

func TestOutcomesWithNothingFinished(t *testing.T) {
	svc, _ := newService(t)

	result, err := svc.Outcomes(context.Background(), period(t, "2026-03-02", "2026-03-02"))

	require.NoError(t, err)
	require.Zero(t, result.Finished)
	require.Len(t, result.Items, 5, "all five segments are still listed")
	for _, item := range result.Items {
		require.Zero(t, item.Count)
		require.Zero(t, item.Rate)
	}
}

func TestTopSuppliersRankByFinishedErrands(t *testing.T) {
	svc, db := newService(t)
	finish := func(supplier, name, status string, at string) {
		seedErrand(t, db, errandSpec{terminalStatus: status, terminal: sg(at), supplier: supplier, supplierName: name})
	}
	// A: 4 finished (2 completed); B: 4 finished (3 completed); C: 1.
	finish(supplierA, "Kopi Corner", "COMPLETED", "2026-03-02 10:00")
	finish(supplierA, "Kopi Corner", "COMPLETED", "2026-03-02 11:00")
	finish(supplierA, "Kopi Corner", "CANCELLED", "2026-03-02 12:00")
	finish(supplierA, "Kopi Corner", "EXPIRED", "2026-03-02 13:00")
	finish(supplierB, "Old Name", "COMPLETED", "2026-03-02 09:00")
	finish(supplierB, "New Name", "COMPLETED", "2026-03-02 14:00")
	finish(supplierB, "", "COMPLETED", "2026-03-02 15:00") // no name on the latest: the previous name is kept
	finish(supplierB, "New Name", "REFUNDED", "2026-03-02 08:00")
	finish(supplierC, "", "COMPLETED", "2026-03-02 16:00") // never had a name
	// Outside the period, and finished but not yet enriched.
	finish(supplierC, "Kopi Corner", "COMPLETED", "2026-03-09 10:00")
	seedErrand(t, db, errandSpec{terminalStatus: "COMPLETED", terminal: sg("2026-03-02 17:00")})

	ranking, err := svc.TopSuppliers(context.Background(), period(t, "2026-03-02", "2026-03-02"), 10)

	require.NoError(t, err)
	require.Equal(t, 1, ranking.Unattributed, "one finished errand has no supplier yet")
	require.Len(t, ranking.Items, 3)
	require.Equal(t, supplierB, ranking.Items[0].ID, "equal finished counts: more completed first")
	require.Equal(t, 4, ranking.Items[0].Finished)
	require.Equal(t, 3, ranking.Items[0].Completed)
	require.Equal(t, "New Name", *ranking.Items[0].Name, "the most recent errand that has a name")
	require.Equal(t, supplierA, ranking.Items[1].ID)
	require.Equal(t, 2, ranking.Items[1].Completed)
	require.Equal(t, "Kopi Corner", *ranking.Items[1].Name)
	require.Equal(t, supplierC, ranking.Items[2].ID)
	require.Nil(t, ranking.Items[2].Name, "a supplier with no name anywhere has none")

	limited, err := svc.TopSuppliers(context.Background(), period(t, "2026-03-02", "2026-03-02"), 2)
	require.NoError(t, err)
	require.Len(t, limited.Items, 2)
	_, err = svc.TopSuppliers(context.Background(), period(t, "2026-03-02", "2026-03-02"), 0)
	require.Error(t, err)
	_, err = svc.TopSuppliers(context.Background(), period(t, "2026-03-02", "2026-03-02"), MaxRankLimit+1)
	require.Error(t, err)
}

func TestTopBuildingsRankByFinishedErrands(t *testing.T) {
	svc, db := newService(t)
	finish := func(building, name, status string) {
		seedErrand(t, db, errandSpec{terminalStatus: status, terminal: sg("2026-03-02 10:00"), building: building, buildingName: name})
	}
	finish(bldgUTown, "UTown", "COMPLETED")
	finish(bldgCOM1, "COM1", "COMPLETED")
	finish(bldgCOM1, "COM1", "CANCELLED")
	finish(bldgCOM1, "COM1", "EXPIRED")
	seedErrand(t, db, errandSpec{terminalStatus: "COMPLETED", terminal: sg("2026-03-02 10:00")}) // not enriched

	ranking, err := svc.TopBuildings(context.Background(), period(t, "2026-03-02", "2026-03-02"), 10)

	require.NoError(t, err)
	require.Equal(t, 1, ranking.Unattributed)
	require.Equal(t, []Ranked{
		{ID: bldgCOM1, Name: strp("COM1"), Finished: 3, Completed: 1},
		{ID: bldgUTown, Name: strp("UTown"), Finished: 1, Completed: 1},
	}, ranking.Items)
}

func strp(s string) *string { return &s }

func TestActiveCouriersCountsDistinctCouriersWithAnAccept(t *testing.T) {
	service, db := newService(t)
	seedAccept(t, db, sg("2026-03-02 09:00"), "auth0|cou1")
	seedAccept(t, db, sg("2026-03-02 15:00"), "auth0|cou1") // the same courier again
	seedAccept(t, db, sg("2026-03-03 09:00"), "auth0|cou2")
	seedAccept(t, db, sg("2026-03-03 23:59"), "auth0|cou3")
	seedAccept(t, db, sg("2026-03-04 00:00"), "auth0|cou4") // outside the period
	seedAccept(t, db, sg("2026-03-01 23:59"), "auth0|cou5") // outside the period
	// Other events by couriers are not accepts.
	seedTransition(t, db, "errand.picked_up", "ASSIGNED", "PICKED_UP", sg("2026-03-02 10:00"), "auth0|cou6")
	seedTransition(t, db, "errand.withdrawn", "ASSIGNED", "OPEN", sg("2026-03-02 10:00"), "auth0|cou7")

	n, err := service.ActiveCouriers(context.Background(), period(t, "2026-03-02", "2026-03-03"))

	require.NoError(t, err)
	require.Equal(t, 3, n)
	none, err := service.ActiveCouriers(context.Background(), period(t, "2026-06-01", "2026-06-02"))
	require.NoError(t, err)
	require.Zero(t, none)
}

func TestRewardVolumeSumsCompletedErrandsByTerminalDay(t *testing.T) {
	service, db := newService(t)
	complete := func(at string, reward int) {
		seedErrand(t, db, errandSpec{terminalStatus: "COMPLETED", terminal: sg(at), reward: reward})
	}
	complete("2026-03-02 00:30", 10) // 2026-03-01 in UTC; the Singapore day is the 2nd
	complete("2026-03-02 22:00", 7)
	complete("2026-03-04 12:00", 5)
	seedErrand(t, db, errandSpec{terminalStatus: "REFUNDED", terminal: sg("2026-03-02 12:00"), reward: 100})  // not completed
	seedErrand(t, db, errandSpec{terminalStatus: "CANCELLED", terminal: sg("2026-03-03 12:00"), reward: 100}) // not completed
	complete("2026-03-05 00:00", 100)                                                                         // outside

	days, err := service.RewardVolume(context.Background(), period(t, "2026-03-02", "2026-03-04"))

	require.NoError(t, err)
	require.Equal(t, []DayReward{
		{"2026-03-02", 17, 2},
		{"2026-03-03", 0, 0},
		{"2026-03-04", 5, 1},
	}, days)
}

func TestMetricsOfAnEmptyReadModel(t *testing.T) {
	service, _ := newService(t)
	p := period(t, "2026-03-02", "2026-03-04")

	days, err := service.ErrandsPerDay(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, days, 3)
	rewards, err := service.RewardVolume(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, rewards, 3)
	suppliers, err := service.TopSuppliers(context.Background(), p, 5)
	require.NoError(t, err)
	require.Empty(t, suppliers.Items)
	require.NotNil(t, suppliers.Items, "an empty list, not null")
}
