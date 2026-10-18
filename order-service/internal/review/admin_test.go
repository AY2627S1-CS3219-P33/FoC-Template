package review_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/review"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeSettingsStore keeps the settings in memory and writes the audit row
// of an update into the shared recordingAudit, as the transaction would.
type fakeSettingsStore struct {
	mu        sync.Mutex
	settings  errand.SystemSettings
	updatedBy []string
	getErr    error
	audit     *recordingAudit
}

func newFakeSettingsStore(audit *recordingAudit) *fakeSettingsStore {
	return &fakeSettingsStore{settings: errand.DefaultSystemSettings(), audit: audit}
}

func (s *fakeSettingsStore) Get(context.Context) (errand.SystemSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return errand.SystemSettings{}, s.getErr
	}
	return s.settings, nil
}

func (s *fakeSettingsStore) UpdateAudited(ctx context.Context, settings errand.SystemSettings, updatedBy string, record opsrepo.AuditRecord) error {
	// opsrepo.Repository.Update validates again.
	if err := settings.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.settings = settings
	s.getErr = nil
	s.updatedBy = append(s.updatedBy, updatedBy)
	s.mu.Unlock()
	_, err := s.audit.InsertAudit(ctx, record)
	return err
}

func (s *fakeSettingsStore) current() errand.SystemSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// fakeTransitions filters in memory with the TransitionFilter semantics;
// the SQL itself is tested against Postgres in opsrepo.
type fakeTransitions struct {
	mu      sync.Mutex
	rows    []opsrepo.Transition
	filters []opsrepo.TransitionFilter
}

func (f *fakeTransitions) ListTransitions(_ context.Context, filter opsrepo.TransitionFilter) ([]opsrepo.Transition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filters = append(f.filters, filter)
	rows := slices.Clone(f.rows)
	slices.SortFunc(rows, func(a, b opsrepo.Transition) int {
		return cmp.Or(b.At.Compare(a.At), cmp.Compare(b.ID, a.ID))
	})
	out := []opsrepo.Transition{}
	for _, row := range rows {
		switch {
		case filter.ErrandID != "" && row.ErrandID != filter.ErrandID,
			filter.UserID != "" && row.ActorID != filter.UserID && row.CourierID != filter.UserID,
			filter.ToStatus != "" && row.ToStatus != filter.ToStatus,
			!filter.From.IsZero() && row.At.Before(filter.From),
			!filter.To.IsZero() && !row.At.Before(filter.To),
			filter.Before != nil && !(row.At.Before(filter.Before.At) || row.At.Equal(filter.Before.At) && row.ID < filter.Before.ID):
			continue
		}
		out = append(out, row)
		if len(out) == filter.Limit {
			break
		}
	}
	return out, nil
}

func (f *fakeTransitions) lastFilter() opsrepo.TransitionFilter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.filters[len(f.filters)-1]
}

type activityCall struct {
	kind     string
	since    time.Time
	minCount int
	limit    int
}

// fakeActivity holds each user's count within the window and applies the
// query's HAVING count >= minCount.
type fakeActivity struct {
	mu            sync.Mutex
	withdrawals   []opsrepo.ActivityCount
	cancellations []opsrepo.ActivityCount
	calls         []activityCall
}

func (f *fakeActivity) ListWithdrawalCounts(_ context.Context, since time.Time, minCount, limit int) ([]opsrepo.ActivityCount, error) {
	return f.list("withdrawals", f.withdrawals, since, minCount, limit), nil
}

func (f *fakeActivity) ListCancellationCounts(_ context.Context, since time.Time, minCount, limit int) ([]opsrepo.ActivityCount, error) {
	return f.list("cancellations", f.cancellations, since, minCount, limit), nil
}

func (f *fakeActivity) list(kind string, counts []opsrepo.ActivityCount, since time.Time, minCount, limit int) []opsrepo.ActivityCount {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, activityCall{kind, since, minCount, limit})
	out := []opsrepo.ActivityCount{}
	for _, c := range counts {
		if c.Count >= minCount && len(out) < limit {
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// GET/PUT /admin/settings
// ---------------------------------------------------------------------------

const defaultSettingsJSON = `{
	"pickupAfter": 7200,
	"deliveryAfter": 7200,
	"verificationAfter": 1800,
	"verificationReminderAfter": 300,
	"withdrawalRestriction": {"count": 3, "window": 900, "duration": 900},
	"cancellationFlag": {"count": 20, "window": 3600},
	"reservationTimeout": 300,
	"editTimeout": 300,
	"outboxConfirmTimeout": 600
}`

func settingsJSON(edit func(map[string]any)) string {
	var body map[string]any
	_ = json.Unmarshal([]byte(defaultSettingsJSON), &body)
	edit(body)
	raw, _ := json.Marshal(body)
	return string(raw)
}

func (h harness) putSettings(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return h.do(t, http.MethodPut, "/admin/settings", h.admin, body)
}

func TestGetSettingsReturnsSecondsAndCounts_F6_3_5(t *testing.T) {
	h := newHarness(t)

	recorder := h.do(t, http.MethodGet, "/admin/settings", h.admin, "")

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, defaultSettingsJSON, recorder.Body.String())
	require.Empty(t, h.audit.all(), "reading is not audited")
}

func TestPutSettingsStoresAndAuditsTheChange_F6_3_5_F6_5(t *testing.T) {
	h := newHarness(t)
	body := settingsJSON(func(b map[string]any) {
		b["pickupAfter"] = 3600
		b["cancellationFlag"] = map[string]any{"count": 10, "window": 1800}
	})

	recorder := h.putSettings(t, body)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, body, recorder.Body.String())
	stored := h.settingsStore.current()
	require.Equal(t, time.Hour, stored.Deadlines.PickupAfter)
	require.Equal(t, errand.CancellationFlag{Count: 10, Window: 30 * time.Minute}, stored.CancellationFlag)
	require.Equal(t, []string{adminID}, h.settingsStore.updatedBy)
	require.Equal(t, []opsrepo.AuditRecord{{
		ActorID:    adminID,
		Action:     review.AuditActionSettings,
		TargetType: review.AuditTargetSettings,
		TargetID:   "settings",
		Reason:     "changed: pickupAfter, cancellationFlag.count, cancellationFlag.window",
		Outcome:    opsrepo.AuditSucceeded,
	}}, h.audit.all())

	// The GET that follows shows the new values.
	require.JSONEq(t, body, h.do(t, http.MethodGet, "/admin/settings", h.admin, "").Body.String())
}

func TestPutSettingsWithoutChangeIsStillAudited_F6_5(t *testing.T) {
	h := newHarness(t)

	require.Equal(t, http.StatusOK, h.putSettings(t, defaultSettingsJSON).Code)

	records := h.audit.all()
	require.Len(t, records, 1)
	require.Equal(t, "no change", records[0].Reason)
	require.Equal(t, opsrepo.AuditSucceeded, records[0].Outcome)
}

func TestPutSettingsRejectsInvalidValuesAndAuditsThem_F6_3_5(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want []apperror.Field
	}{
		{"zero and negative", func(b map[string]any) {
			b["pickupAfter"] = 0
			b["withdrawalRestriction"] = map[string]any{"count": -1, "window": 900, "duration": 900}
			b["editTimeout"] = -5
		}, []apperror.Field{
			{Field: "pickupAfter", Message: "must be positive"},
			{Field: "withdrawalRestriction.count", Message: "must be positive"},
			{Field: "editTimeout", Message: "must be positive"},
		}},
		{"reminder not before auto-complete", func(b map[string]any) {
			b["verificationReminderAfter"] = 1800
		}, []apperror.Field{
			{Field: "verificationReminderAfter", Message: "must be before verificationAfter"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)

			recorder := h.putSettings(t, settingsJSON(tc.edit))

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			body := decodeError(t, recorder)
			require.Equal(t, apperror.ValidationFailed, body.Code)
			require.Equal(t, tc.want, body.Fields)
			require.Equal(t, errand.DefaultSystemSettings(), h.settingsStore.current(), "nothing stored")
			require.Equal(t, []opsrepo.AuditRecord{{
				ActorID:    adminID,
				Action:     review.AuditActionSettings,
				TargetType: review.AuditTargetSettings,
				TargetID:   "settings",
				Reason:     string(apperror.ValidationFailed),
				Outcome:    opsrepo.AuditRejected,
			}}, h.audit.all())
		})
	}
}

func TestPutSettingsRejectsMalformedBody_NFR2_2_1(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		name, body string
		want       []apperror.Field
	}{
		{"missing fields", `{"pickupAfter": 60, "withdrawalRestriction": {"count": 3}}`, []apperror.Field{
			{Field: "deliveryAfter", Message: "is required"},
			{Field: "verificationAfter", Message: "is required"},
			{Field: "verificationReminderAfter", Message: "is required"},
			{Field: "withdrawalRestriction.window", Message: "is required"},
			{Field: "withdrawalRestriction.duration", Message: "is required"},
			{Field: "cancellationFlag", Message: "is required"},
			{Field: "reservationTimeout", Message: "is required"},
			{Field: "editTimeout", Message: "is required"},
			{Field: "outboxConfirmTimeout", Message: "is required"},
		}},
		{"fractional seconds", settingsJSON(func(b map[string]any) { b["editTimeout"] = 1.5 }), []apperror.Field{
			{Field: "editTimeout", Message: "must be an integer"},
		}},
		{"string count", settingsJSON(func(b map[string]any) {
			b["cancellationFlag"] = map[string]any{"count": "20", "window": 3600}
		}), []apperror.Field{
			{Field: "cancellationFlag.count", Message: "must be an integer"},
		}},
		{"object expected", settingsJSON(func(b map[string]any) { b["withdrawalRestriction"] = 3 }), []apperror.Field{
			{Field: "withdrawalRestriction", Message: "must be an object"},
		}},
		{"too large", settingsJSON(func(b map[string]any) { b["pickupAfter"] = int64(1) << 40 }), []apperror.Field{
			{Field: "pickupAfter", Message: "is too large"},
		}},
		{"unknown field", settingsJSON(func(b map[string]any) { b["autoBan"] = true }), []apperror.Field{
			{Field: "autoBan", Message: "is not allowed"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := h.putSettings(t, tc.body)

			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			body := decodeError(t, recorder)
			require.Equal(t, apperror.ValidationFailed, body.Code)
			require.Equal(t, tc.want, body.Fields)
		})
	}
	for _, body := range []string{"", "[]", "nope"} {
		recorder := h.putSettings(t, body)
		require.Equal(t, http.StatusBadRequest, recorder.Code, body)
		require.Equal(t, apperror.InvalidArgument, decodeError(t, recorder).Code, body)
	}
	require.Equal(t, errand.DefaultSystemSettings(), h.settingsStore.current())
	require.Empty(t, h.audit.all(), "a body that never reached the service is not audited")
}

func TestPutSettingsRepairsUnreadableSettings(t *testing.T) {
	h := newHarness(t)
	h.settingsStore.getErr = errors.New("setting \"editTimeout\": want a duration string")

	require.Equal(t, http.StatusInternalServerError, h.do(t, http.MethodGet, "/admin/settings", h.admin, "").Code)
	require.Equal(t, http.StatusOK, h.putSettings(t, defaultSettingsJSON).Code)

	require.Equal(t, http.StatusOK, h.do(t, http.MethodGet, "/admin/settings", h.admin, "").Code)
	require.Equal(t, "changed: all (the stored settings could not be read)", h.audit.all()[0].Reason)
}

// ---------------------------------------------------------------------------
// GET /admin/errand-events
// ---------------------------------------------------------------------------

type eventPage struct {
	Items []struct {
		ID int64 `json:"id"`
	} `json:"items"`
	NextCursor string `json:"nextCursor"`
}

func (h harness) events(t *testing.T, query string) eventPage {
	t.Helper()
	recorder := h.do(t, http.MethodGet, "/admin/errand-events"+query, h.admin, "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var page eventPage
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &page))
	return page
}

func (p eventPage) ids() []int64 {
	ids := []int64{}
	for _, item := range p.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

func seedEvents(h harness) {
	minute := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Minute) }
	h.transitions.rows = []opsrepo.Transition{
		{ID: 1, ErrandID: errandA, ToStatus: errand.StatusPendingReservation, Event: errand.EventSubmit,
			ActorType: errand.ActorRequester, ActorID: requesterID, ErrandVersion: 1, At: minute(1)},
		{ID: 2, ErrandID: errandA, FromStatus: errand.StatusPendingReservation, ToStatus: errand.StatusOpen,
			Event: errand.EventReservationConfirmed, ActorType: errand.ActorSystem, ErrandVersion: 2, At: minute(2)},
		{ID: 3, ErrandID: errandA, FromStatus: errand.StatusOpen, ToStatus: errand.StatusAssigned, Event: errand.EventAccept,
			ActorType: errand.ActorCourier, ActorID: courierID, CourierID: courierID, ErrandVersion: 3, At: minute(3)},
		{ID: 4, ErrandID: errandA, FromStatus: errand.StatusAssigned, ToStatus: errand.StatusOpen, Event: errand.EventPickupDeadlinePassed,
			ActorType: errand.ActorSystem, CourierID: courierID, ErrandVersion: 4, At: minute(4)},
		{ID: 5, ErrandID: errandA, FromStatus: errand.StatusOpen, ToStatus: errand.StatusCancelled, Event: errand.EventCancel,
			ActorType: errand.ActorRequester, ActorID: requesterID, Reason: "changed my mind", ErrandVersion: 5, At: minute(5)},
	}
}

func TestEventsShapeNewestFirst_F6_2_1(t *testing.T) {
	h := newHarness(t)
	seedEvents(h)

	recorder := h.do(t, http.MethodGet, "/admin/errand-events?limit=2", h.admin, "")

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.NotEmpty(t, body["nextCursor"])
	items, _ := json.Marshal(body["items"])
	require.JSONEq(t, `[
		{"id":5,"errandId":"`+errandA+`","fromStatus":"OPEN","toStatus":"CANCELLED","event":"cancel",
		 "actorType":"REQUESTER","actorId":"`+requesterID+`","reason":"changed my mind","errandVersion":5,"at":"2026-03-02T09:05:00Z"},
		{"id":4,"errandId":"`+errandA+`","fromStatus":"ASSIGNED","toStatus":"OPEN","event":"pickup_deadline_passed",
		 "actorType":"SYSTEM","errandVersion":4,"at":"2026-03-02T09:04:00Z"}
	]`, string(items), "actorId omitted for SYSTEM; courier_id is not part of TransitionRecord")

	first := h.events(t, "?limit=1&type=PENDING_RESERVATION")
	raw, _ := json.Marshal(first.Items)
	require.NotContains(t, string(raw), "fromStatus", "the creation transition has no fromStatus")
}

func TestEventsPaginateWithCursor_F6_2_1(t *testing.T) {
	h := newHarness(t)
	seedEvents(h)

	var seen []int64
	query := "?limit=2"
	for page := 0; ; page++ {
		require.Less(t, page, 5)
		result := h.events(t, query)
		seen = append(seen, result.ids()...)
		if result.NextCursor == "" {
			break
		}
		query = "?limit=2&cursor=" + url.QueryEscape(result.NextCursor)
	}
	require.Equal(t, []int64{5, 4, 3, 2, 1}, seen)

	// An exactly full last page has no cursor.
	require.Empty(t, h.events(t, "?limit=5").NextCursor)
	require.Equal(t, 6, h.transitions.lastFilter().Limit, "one extra row shows whether another page exists")
}

func TestEventsPassFiltersToRepository_F6_2_2(t *testing.T) {
	h := newHarness(t)
	seedEvents(h)
	// from has an offset: it is 09:02Z.
	from, to := "2026-03-02T17:02:00+08:00", "2026-03-02T09:04:30Z"

	h.events(t, "?userId="+url.QueryEscape(courierID)+"&errandId="+strings.ToUpper(errandA)+"&type=OPEN&from="+url.QueryEscape(from)+"&to="+to+"&limit=10")

	filter := h.transitions.lastFilter()
	require.Equal(t, courierID, filter.UserID)
	require.Equal(t, errandA, filter.ErrandID, "errandId is normalised to lower case")
	require.Equal(t, errand.StatusOpen, filter.ToStatus)
	require.True(t, filter.From.Equal(time.Date(2026, 3, 2, 9, 2, 0, 0, time.UTC)))
	require.True(t, filter.To.Equal(time.Date(2026, 3, 2, 9, 4, 30, 0, time.UTC)))
	require.Nil(t, filter.Before)
	require.Equal(t, 11, filter.Limit)

	for _, tc := range []struct {
		query string
		want  []int64
	}{
		{"?userId=" + url.QueryEscape(courierID), []int64{4, 3}},
		{"?type=OPEN", []int64{4, 2}},
		{"?from=2026-03-02T09:02:00Z&to=2026-03-02T09:04:00Z", []int64{3, 2}},
		{"?errandId=" + errandB, []int64{}},
	} {
		require.Equal(t, tc.want, h.events(t, tc.query).ids(), tc.query)
	}
}

func TestEventsCursorIsBoundToFilters(t *testing.T) {
	h := newHarness(t)
	seedEvents(h)
	cursor := h.events(t, "?type=OPEN&limit=1").NextCursor
	require.NotEmpty(t, cursor)

	require.Equal(t, []int64{2}, h.events(t, "?type=OPEN&limit=1&cursor="+url.QueryEscape(cursor)).ids())
	for _, query := range []string{
		"?cursor=" + url.QueryEscape(cursor),
		"?type=CANCELLED&cursor=" + url.QueryEscape(cursor),
		"?type=OPEN&from=2026-03-02T09:00:00Z&cursor=" + url.QueryEscape(cursor),
		"?type=OPEN&cursor=" + url.QueryEscape(cursor[:len(cursor)-2]),
	} {
		recorder := h.do(t, http.MethodGet, "/admin/errand-events"+query, h.admin, "")
		require.Equal(t, http.StatusBadRequest, recorder.Code, query)
		require.Equal(t, apperror.InvalidArgument, decodeError(t, recorder).Code, query)
	}
}

func TestEventsRejectMalformedQuery(t *testing.T) {
	h := newHarness(t)
	for _, query := range []string{
		"?userId=", "?userId=" + strings.Repeat("u", 256),
		"?errandId=abc", "?type=LOST", "?type=open",
		"?from=yesterday", "?to=2026-03-02",
		"?from=2026-03-02T10:00:00Z&to=2026-03-02T10:00:00Z",
		"?limit=0", "?limit=101",
	} {
		recorder := h.do(t, http.MethodGet, "/admin/errand-events"+query, h.admin, "")
		require.Equal(t, http.StatusBadRequest, recorder.Code, query)
		require.Equal(t, apperror.InvalidArgument, decodeError(t, recorder).Code, query)
	}
	require.Empty(t, h.transitions.filters)
}

// ---------------------------------------------------------------------------
// GET /admin/flags
// ---------------------------------------------------------------------------

func TestFlagsAtBelowAndAboveThresholds_F6_3_1_F6_3_2(t *testing.T) {
	h := newHarness(t)
	// Defaults: a courier is restricted at 3 withdrawals in 15 min; a
	// requester is flagged above 20 cancellations in 1 h.
	h.activity.withdrawals = []opsrepo.ActivityCount{{UserID: "cour-above", Count: 4}, {UserID: "cour-at", Count: 3}, {UserID: "cour-below", Count: 2}}
	h.activity.cancellations = []opsrepo.ActivityCount{{UserID: "req-above", Count: 21}, {UserID: "req-at", Count: 20}, {UserID: "req-below", Count: 19}}

	recorder := h.do(t, http.MethodGet, "/admin/flags", h.admin, "")

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{"items":[
		{"userId":"req-above","kind":"REQUESTER_CANCELLATIONS","count":21,"threshold":20,"window":3600,
		 "since":"2026-03-02T08:00:00Z","summary":"High cancellation rate, 21 in 1 h"},
		{"userId":"cour-above","kind":"COURIER_WITHDRAWALS","count":4,"threshold":3,"window":900,
		 "since":"2026-03-02T08:45:00Z","summary":"High withdrawal rate, 4 in 15 min"},
		{"userId":"cour-at","kind":"COURIER_WITHDRAWALS","count":3,"threshold":3,"window":900,
		 "since":"2026-03-02T08:45:00Z","summary":"High withdrawal rate, 3 in 15 min"}
	]}`, recorder.Body.String())
	require.Equal(t, []activityCall{
		{"withdrawals", t0.Add(-15 * time.Minute), 3, review.MaxFlags},
		{"cancellations", t0.Add(-time.Hour), 21, review.MaxFlags},
	}, h.activity.calls)
}

func TestFlagsFollowCurrentSettings_F6_3_5(t *testing.T) {
	h := newHarness(t)
	settings := errand.DefaultSystemSettings()
	settings.WithdrawalRestriction = errand.WithdrawalRestriction{Count: 5, Window: 90 * time.Second, Duration: time.Minute}
	settings.CancellationFlag = errand.CancellationFlag{Count: 1, Window: 90 * time.Minute}
	h.settings.Set(settings)
	h.activity.withdrawals = []opsrepo.ActivityCount{{UserID: "cour-a", Count: 5}}
	h.activity.cancellations = []opsrepo.ActivityCount{{UserID: "req-a", Count: 2}, {UserID: "req-b", Count: 1}}

	var body struct {
		Items []struct {
			UserID  string `json:"userId"`
			Window  int    `json:"window"`
			Summary string `json:"summary"`
		} `json:"items"`
	}
	recorder := h.do(t, http.MethodGet, "/admin/flags", h.admin, "")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))

	require.Len(t, body.Items, 2)
	require.Equal(t, "cour-a", body.Items[0].UserID)
	require.Equal(t, 90, body.Items[0].Window)
	require.Equal(t, "High withdrawal rate, 5 in 90 s", body.Items[0].Summary)
	require.Equal(t, "req-a", body.Items[1].UserID)
	require.Equal(t, "High cancellation rate, 2 in 90 min", body.Items[1].Summary)
	require.Equal(t, 5, h.activity.calls[0].minCount)
	require.Equal(t, 2, h.activity.calls[1].minCount)
}

func TestFlagsEmptyList(t *testing.T) {
	h := newHarness(t)
	recorder := h.do(t, http.MethodGet, "/admin/flags", h.admin, "")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"items":[]}`, recorder.Body.String())
}

// ---------------------------------------------------------------------------
// Authorization
// ---------------------------------------------------------------------------

func TestAdminRoutesRequireReviewPermission_F1_6_2_NFR3_3(t *testing.T) {
	h := newHarness(t)
	f := h.fixture
	user := f.Token(t, f.Claims(auth.OrderAudience, []string{string(auth.UseErrands)}), "one")
	service := f.Token(t, f.ServiceClaims(auth.OrderAudience, []string{string(auth.ReviewErrands)}), "one")

	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/settings", ""},
		{http.MethodPut, "/admin/settings", defaultSettingsJSON},
		{http.MethodGet, "/admin/errand-events", ""},
		{http.MethodGet, "/admin/flags", ""},
	} {
		for _, tc := range []struct {
			name, token string
			status      int
		}{
			{"no token", "", http.StatusUnauthorized},
			{"user without errands:review", user, http.StatusForbidden},
			{"service token", service, http.StatusForbidden},
		} {
			recorder := h.do(t, route.method, route.path, tc.token, route.body)
			require.Equal(t, tc.status, recorder.Code, "%s %s: %s", route.method, route.path, tc.name)
		}
	}
	require.Empty(t, h.settingsStore.updatedBy)
	require.Empty(t, h.transitions.filters)
	require.Empty(t, h.activity.calls)
	require.Empty(t, h.audit.all())
}
