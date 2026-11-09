package backfill

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/orderclient"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/testdb"
)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// exportServer serves the transition export for items, like order-service.
func exportServer(t *testing.T, items []orderclient.Transition) *orderclient.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page := map[string]any{"items": []orderclient.Transition{}}
		var out []orderclient.Transition
		for _, item := range items {
			if item.ID <= after {
				continue
			}
			if len(out) == limit {
				page["nextCursor"] = strconv.FormatInt(out[limit-1].ID, 10)
				break
			}
			out = append(out, item)
		}
		if out != nil {
			page["items"] = out
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	t.Cleanup(server.Close)
	client, err := orderclient.New(server.URL, staticToken("t"), 2*time.Second)
	require.NoError(t, err)
	return client
}

// tables dumps both fact tables, every column, in a fixed order.
func tables(t *testing.T, db testdb.DB) string {
	t.Helper()
	var transitions, errands string
	require.NoError(t, db.Conn.QueryRow(context.Background(),
		`SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY message_id), '[]')::text FROM transition_facts t`).Scan(&transitions))
	require.NoError(t, db.Conn.QueryRow(context.Background(),
		`SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY errand_id), '[]')::text FROM errand_facts e`).Scan(&errands))
	return transitions + "\n" + errands
}

// history is several errands' events, interleaved, with a withdraw and
// re-accept and the withdrawn/expired pair.
func history() []orderclient.Transition {
	var items []orderclient.Transition
	id := int64(0)
	add := func(errand string, eventType string, version int, from, to string, minute int) {
		id += 10
		items = append(items, orderclient.Transition{
			ID: id, MessageID: fmt.Sprintf("%s:%s:%d", errand, eventType[len("errand."):], version), Type: eventType,
			ErrandID: errand, ErrandVersion: version, From: facts.Status(from), To: facts.Status(to),
			CourierID: "auth0|cou1", Reward: 8, OccurredAt: time.Date(2026, 3, 2, 9, minute, 0, 0, time.UTC),
		})
	}
	a, b := errandID, "7a2d3c1f-9e5b-4f2c-8d4a-3b8e6f9c0d21"
	add(a, "errand.placed", 1, "", "PENDING_RESERVATION", 0)
	add(b, "errand.placed", 1, "", "PENDING_RESERVATION", 1)
	add(a, "errand.opened", 2, "PENDING_RESERVATION", "OPEN", 2)
	add(b, "errand.opened", 2, "PENDING_RESERVATION", "OPEN", 3)
	add(a, "errand.accepted", 3, "OPEN", "ASSIGNED", 10)
	add(a, "errand.withdrawn", 4, "ASSIGNED", "OPEN", 20)
	add(a, "errand.accepted", 5, "OPEN", "ASSIGNED", 30)
	add(b, "errand.accepted", 3, "OPEN", "ASSIGNED", 31)
	add(b, "errand.withdrawn", 4, "ASSIGNED", "OPEN", 40)
	add(b, "errand.expired", 4, "ASSIGNED", "EXPIRED", 40) // the pair: same version and time
	add(a, "errand.picked_up", 6, "ASSIGNED", "PICKED_UP", 41)
	add(a, "errand.delivered", 7, "PICKED_UP", "DELIVERED", 50)
	add(a, "errand.completed", 8, "DELIVERED", "COMPLETED", 60)
	return items
}

func TestBackfillTwiceGivesIdenticalTables(t *testing.T) {
	db := testdb.Migrated(t)
	store := facts.NewStore(db.Conn)
	source := exportServer(t, history())

	first, err := Run(context.Background(), source, store, logger, Config{PageSize: 4})
	require.NoError(t, err)
	afterFirst := tables(t, db)
	second, err := Run(context.Background(), source, store, logger, Config{PageSize: 4})
	require.NoError(t, err)

	require.Equal(t, Result{Pages: 4, Events: 13, Applied: 13}, first)
	require.Equal(t, Result{Pages: 4, Events: 13, Applied: 0}, second)
	require.Equal(t, afterFirst, tables(t, db), "the second run changed nothing, not even a timestamp")
}

func TestBackfillMeetsTheLiveStreamAnyway(t *testing.T) {
	// The same events reach the read model by the stream (here: newest first,
	// some twice), by the backfill, or both in any order: the tables match.
	all := history()
	reference := testdb.Migrated(t)
	_, err := Run(context.Background(), exportServer(t, all), facts.NewStore(reference.Conn), logger, Config{})
	require.NoError(t, err)
	want := tables(t, reference)

	for name, run := range map[string]func(store *facts.Store, source *orderclient.Client){
		"stream backwards, then backfill": func(store *facts.Store, source *orderclient.Client) {
			for i := len(all) - 1; i >= 0; i-- {
				_, err := store.Apply(context.Background(), all[i].FactsEvent())
				require.NoError(t, err)
			}
			_, err := Run(context.Background(), source, store, logger, Config{})
			require.NoError(t, err)
		},
		"backfill, then the stream again": func(store *facts.Store, source *orderclient.Client) {
			_, err := Run(context.Background(), source, store, logger, Config{})
			require.NoError(t, err)
			for _, item := range all {
				applied, err := store.Apply(context.Background(), item.FactsEvent())
				require.NoError(t, err)
				require.False(t, applied)
			}
		},
		"half by the stream, the rest by backfill": func(store *facts.Store, source *orderclient.Client) {
			for i := 0; i < len(all); i += 2 {
				_, err := store.Apply(context.Background(), all[i].FactsEvent())
				require.NoError(t, err)
			}
			_, err := Run(context.Background(), source, store, logger, Config{PageSize: 3})
			require.NoError(t, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			db := testdb.Migrated(t)
			run(facts.NewStore(db.Conn), exportServer(t, all))

			got := tables(t, db)
			// recorded_at is the wall-clock time a row was written, which
			// differs with the route taken; everything else must match.
			require.JSONEq(t, stripRecordedAt(t, want), stripRecordedAt(t, got))
		})
	}
}

func stripRecordedAt(t *testing.T, dump string) string {
	t.Helper()
	var transitions, errands []map[string]any
	parts := splitLines(dump)
	require.NoError(t, json.Unmarshal([]byte(parts[0]), &transitions))
	require.NoError(t, json.Unmarshal([]byte(parts[1]), &errands))
	for _, row := range transitions {
		delete(row, "recorded_at")
	}
	out, err := json.Marshal(map[string]any{"transitions": transitions, "errands": errands})
	require.NoError(t, err)
	return string(out)
}

func splitLines(s string) [2]string {
	for i := range s {
		if s[i] == '\n' {
			return [2]string{s[:i], s[i+1:]}
		}
	}
	return [2]string{s, "[]"}
}

func TestBackfillBuildsTheErrandRows(t *testing.T) {
	db := testdb.Migrated(t)
	_, err := Run(context.Background(), exportServer(t, history()), facts.NewStore(db.Conn), logger, Config{})
	require.NoError(t, err)

	type row struct {
		status, terminal string
		firstAccepted    time.Time
		due              bool
	}
	read := func(id string) row {
		var r row
		var terminal *string
		require.NoError(t, db.Conn.QueryRow(context.Background(),
			`SELECT status, terminal_status, first_accepted_at, enrichment_due FROM errand_facts WHERE errand_id = $1`, id).
			Scan(&r.status, &terminal, &r.firstAccepted, &r.due))
		if terminal != nil {
			r.terminal = *terminal
		}
		return r
	}

	a := read(errandID)
	require.Equal(t, "COMPLETED", a.status)
	require.Equal(t, "COMPLETED", a.terminal)
	require.True(t, a.firstAccepted.Equal(time.Date(2026, 3, 2, 9, 10, 0, 0, time.UTC)), "the first accept, not the re-accept")
	require.True(t, a.due, "left for the service's enrichment worker")
	b := read("7a2d3c1f-9e5b-4f2c-8d4a-3b8e6f9c0d21")
	require.Equal(t, "EXPIRED", b.status, "the withdrawn/expired pair ends EXPIRED")
	require.Equal(t, "EXPIRED", b.terminal)
}
