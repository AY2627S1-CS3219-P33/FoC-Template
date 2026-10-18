package review_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth/authtest"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/review"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

const (
	adminID       = "auth0|fixture-user" // authtest.Fixture.Claims' subject
	requesterID   = "auth0|requester-1"
	courierID     = "auth0|courier-1"
	supplierID    = "0b8e6a52-3c1d-4f7e-8a9b-1c2d3e4f5a6b"
	versionID     = "5d2c1b0a-9f8e-4d7c-8b6a-5f4e3d2c1b0a"
	buildingID    = "7a6b5c4d-3e2f-4a1b-9c8d-7e6f5a4b3c2d"
	contactName   = "Alex Tan"
	contactMobile = "+65 9123 4567"

	errandA = "11111111-1111-4111-8111-111111111111"
	errandB = "22222222-2222-4222-8222-222222222222"
	errandC = "33333333-3333-4333-8333-333333333333"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// pickedUpErrand is PICKED_UP by courierID at version 5.
func pickedUpErrand(id string) errand.Errand {
	return errand.Errand{
		ID:          id,
		Status:      errand.StatusPickedUp,
		Version:     5,
		RequesterID: requesterID,
		CourierID:   courierID,
		Details: errand.Details{
			SupplierID:               supplierID,
			SupplierVersionID:        versionID,
			DeliveryBuildingID:       buildingID,
			DeliveryBuildingSnapshot: json.RawMessage(`{"id":"` + buildingID + `","name":"COM1","retired":false}`),
			DeliveryDetail:           "Level 2 lounge",
			Items:                    json.RawMessage(`[{"description":"iced latte","quantity":1}]`),
			Notes:                    "less ice",
			ContactName:              contactName,
			ContactMobile:            contactMobile,
			Reward:                   10,
			AcceptanceDeadline:       t0.Add(-time.Hour),
		},
		PickupDeadline:       ptr(t0.Add(-30 * time.Minute)),
		DeliveryDeadline:     ptr(t0.Add(30 * time.Minute)),
		CreateIdempotencyKey: "create-" + id,
		CreatedAt:            t0.Add(-2 * time.Hour),
		UpdatedAt:            t0.Add(-40 * time.Minute),
	}
}

// reviewErrand is REVIEW_REQUIRED at version 6 with an open review raised
// by the system at updatedAt (FakeStore.Put opens it).
func reviewErrand(id string, reason errand.ReviewReason, updatedAt time.Time) errand.Errand {
	e := pickedUpErrand(id)
	e.Status = errand.StatusReviewRequired
	e.Version = 6
	e.ReviewReason = reason
	e.UpdatedAt = updatedAt
	return e
}

// storeReviews lists the FakeStore's open review rows as opsrepo would,
// with the row's position as its ID.
type storeReviews struct {
	store *testfakes.FakeStore
}

func (r storeReviews) ListOpenReviews(_ context.Context, afterID int64, limit int) ([]opsrepo.OpenReview, error) {
	var open []opsrepo.OpenReview
	for i, row := range r.store.Reviews() {
		id := int64(i + 1)
		if row.Resolution != "" || id <= afterID {
			continue
		}
		e, _ := r.store.Errand(row.ErrandID)
		open = append(open, opsrepo.OpenReview{
			ID: id, ErrandID: row.ErrandID, Reason: row.Reason,
			ReporterID: row.ReporterID, Note: row.Note, CreatedAt: row.CreatedAt,
			Errand: opsrepo.ReviewedErrand{Status: e.Status, Version: e.Version, RequesterID: e.RequesterID, CourierID: e.CourierID},
		})
		if len(open) == limit {
			break
		}
	}
	return open, nil
}

type recordingAudit struct {
	mu      sync.Mutex
	records []opsrepo.AuditRecord
}

func (a *recordingAudit) InsertAudit(_ context.Context, record opsrepo.AuditRecord) (opsrepo.AuditEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, record)
	return opsrepo.AuditEntry{ID: int64(len(a.records)), AuditRecord: record, At: t0}, nil
}

func (a *recordingAudit) all() []opsrepo.AuditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]opsrepo.AuditRecord(nil), a.records...)
}

type harness struct {
	router    http.Handler
	store     *testfakes.FakeStore
	suppliers *testfakes.FakeSupplierGate
	audit     *recordingAudit
	logs      *strings.Builder
	fixture   *authtest.Fixture
	admin     string // a valid user token with errands:review
}

func newHarness(t *testing.T) harness {
	t.Helper()
	f := authtest.New(t)
	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	verifier, err := auth.NewAuth0(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)

	logs := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	store := testfakes.NewFakeStore()
	suppliers := testfakes.NewFakeSupplierGate()
	suppliers.AddSupplier(errand.SupplierVersion{
		SupplierID: supplierID, VersionID: versionID, Name: "Kopi Corner",
		Building: "COM1", Floor: "1", LocationDescription: "next to the lift",
		Latitude: 1.2949, Longitude: 103.7737, OpeningTime: "08:00", ClosingTime: "20:00",
	})
	audit := &recordingAudit{}
	service := review.NewService(review.Dependencies{
		Store:     store,
		Reviews:   storeReviews{store: store},
		Audit:     audit,
		Suppliers: suppliers,
		Clock:     testfakes.NewFakeClock(t0),
		Logger:    logger,
	})
	router := httpapi.NewRouter(httpapi.Dependencies{Logger: logger},
		review.NewHandler(service, auth.NewMiddleware(verifier), logger))
	admin := f.Token(t, f.Claims(auth.OrderAudience, []string{string(auth.ReviewErrands)}), "one")
	return harness{router: router, store: store, suppliers: suppliers, audit: audit, logs: logs, fixture: f, admin: admin}
}

func (h harness) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

func (h harness) resolve(t *testing.T, errandID, body string) *httptest.ResponseRecorder {
	t.Helper()
	return h.do(t, http.MethodPost, "/admin/reviews/"+errandID+"/resolve", h.admin, body)
}

func resolution(outcome string, version int) string {
	return `{"outcome":"` + outcome + `","reason":"Photo shows the items were delivered","expectedVersion":` + jsonInt(version) + `}`
}

func jsonInt(v int) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) apperror.Error {
	t.Helper()
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	var body apperror.Error
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body
}

type listPage struct {
	Items []struct {
		ErrandID string `json:"errandId"`
		Review   struct {
			Reason string `json:"reason"`
		} `json:"review"`
	} `json:"items"`
	NextCursor string `json:"nextCursor"`
}

func (h harness) list(t *testing.T, query string) listPage {
	t.Helper()
	recorder := h.do(t, http.MethodGet, "/admin/reviews"+query, h.admin, "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var page listPage
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &page))
	return page
}

func (p listPage) ids() []string {
	ids := []string{}
	for _, item := range p.Items {
		ids = append(ids, item.ErrandID)
	}
	return ids
}

// ---------------------------------------------------------------------------
// GET /admin/reviews
// ---------------------------------------------------------------------------

func TestListShowsOpenReviewsAsAdminErrandView_F6_1_1(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// A courier-reported review, so reporterId and note are set.
	h.store.Put(pickedUpErrand(errandA))
	_, _, err := h.store.Fire(ctx, errandA, errand.EventReportIssue,
		errand.Actor{Type: errand.ActorCourier, ID: courierID}, errand.EventInput{Reason: "The shop was closed"}, t0)
	require.NoError(t, err)

	recorder := h.do(t, http.MethodGet, "/admin/reviews", h.admin, "")

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	require.JSONEq(t, `{"items":[{
		"errandId":"`+errandA+`",
		"version":6,
		"status":"REVIEW_REQUIRED",
		"supplierId":"`+supplierID+`",
		"supplierVersionId":"`+versionID+`",
		"pickup":{"name":"Kopi Corner","building":"COM1","floor":"1","locationDescription":"next to the lift","latitude":1.2949,"longitude":103.7737},
		"deliveryBuilding":{"id":"`+buildingID+`","name":"COM1"},
		"deliveryDetail":"Level 2 lounge",
		"items":[{"description":"iced latte","quantity":1}],
		"notes":"less ice",
		"reward":10,
		"createdAt":"2026-03-02T07:00:00Z",
		"updatedAt":"2026-03-02T09:00:00Z",
		"acceptanceDeadline":"2026-03-02T08:00:00Z",
		"pickupDeadline":"2026-03-02T08:30:00Z",
		"deliveryDeadline":"2026-03-02T09:30:00Z",
		"reviewReason":"COURIER_ISSUE",
		"viewerRole":"ADMIN",
		"requesterId":"`+requesterID+`",
		"courierId":"`+courierID+`",
		"review":{"reason":"COURIER_ISSUE","reporterId":"`+courierID+`","note":"The shop was closed","createdAt":"2026-03-02T09:00:00Z"}
	}]}`, recorder.Body.String())
	// NFR3.6.3: the contact person never reaches the admin view.
	require.NotContains(t, recorder.Body.String(), contactName)
	require.NotContains(t, recorder.Body.String(), contactMobile)
}

func TestListPaginatesOldestFirstAndSkipsResolved_F6_1(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewDeliveryOverdue, t0.Add(-3*time.Minute)))
	h.store.Put(reviewErrand(errandB, errand.ReviewRequesterDispute, t0.Add(-2*time.Minute)))
	h.store.Put(reviewErrand(errandC, errand.ReviewCourierIssue, t0.Add(-time.Minute)))

	first := h.list(t, "?limit=2")
	require.Equal(t, []string{errandA, errandB}, first.ids())
	require.NotEmpty(t, first.NextCursor)

	second := h.list(t, "?limit=2&cursor="+url.QueryEscape(first.NextCursor))
	require.Equal(t, []string{errandC}, second.ids())
	require.Empty(t, second.NextCursor, "the last page has no cursor")

	// An exactly full last page has no cursor either.
	require.Empty(t, h.list(t, "?limit=3").NextCursor)

	// A resolved review leaves the list.
	require.Equal(t, http.StatusOK, h.resolve(t, errandB, resolution("REFUNDED", 6)).Code)
	require.Equal(t, []string{errandA, errandC}, h.list(t, "").ids())
}

func TestListFiltersByReasonAndBindsCursorToIt_F6_1(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewDeliveryOverdue, t0))
	h.store.Put(reviewErrand(errandB, errand.ReviewRequesterDispute, t0))
	h.store.Put(reviewErrand(errandC, errand.ReviewDeliveryOverdue, t0))

	first := h.list(t, "?reason=DELIVERY_OVERDUE&limit=1")
	require.Equal(t, []string{errandA}, first.ids())
	require.NotEmpty(t, first.NextCursor)

	second := h.list(t, "?reason=DELIVERY_OVERDUE&limit=1&cursor="+url.QueryEscape(first.NextCursor))
	require.Equal(t, []string{errandC}, second.ids())
	require.Empty(t, second.NextCursor)

	disputes := h.list(t, "?reason=REQUESTER_DISPUTE")
	require.Equal(t, []string{errandB}, disputes.ids())
	require.Equal(t, "REQUESTER_DISPUTE", disputes.Items[0].Review.Reason)

	// The cursor was issued for DELIVERY_OVERDUE.
	for _, query := range []string{
		"?cursor=" + url.QueryEscape(first.NextCursor),
		"?reason=REQUESTER_DISPUTE&cursor=" + url.QueryEscape(first.NextCursor),
	} {
		recorder := h.do(t, http.MethodGet, "/admin/reviews"+query, h.admin, "")
		require.Equal(t, http.StatusBadRequest, recorder.Code, query)
		require.Equal(t, apperror.InvalidArgument, decodeError(t, recorder).Code)
	}
}

func TestListRejectsMalformedQuery(t *testing.T) {
	h := newHarness(t)
	for _, query := range []string{
		"?limit=0", "?limit=101", "?limit=ten",
		"?reason=LATE", "?reason=",
		"?cursor=not-a-cursor", "?cursor=" + strings.Repeat("a", 513),
	} {
		t.Run(query, func(t *testing.T) {
			recorder := h.do(t, http.MethodGet, "/admin/reviews"+query, h.admin, "")
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Equal(t, apperror.InvalidArgument, decodeError(t, recorder).Code)
		})
	}
}

func TestListReturnsEmptyItemsArray(t *testing.T) {
	h := newHarness(t)
	recorder := h.do(t, http.MethodGet, "/admin/reviews", h.admin, "")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"items":[]}`, recorder.Body.String())
}

// ---------------------------------------------------------------------------
// POST /admin/reviews/{errandId}/resolve
// ---------------------------------------------------------------------------

func TestResolveCompletedTransfersToCourier_F6_1_2_F6_1_3(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewRequesterDispute, t0.Add(-time.Hour)))

	recorder := h.resolve(t, errandA, resolution("COMPLETED", 6))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	stored, _ := h.store.Errand(errandA)
	require.Equal(t, errand.StatusCompleted, stored.Status)
	require.Equal(t, 7, stored.Version)

	transfers := h.store.OutboxKinds(errand.KindCreditTransfer)
	require.Len(t, transfers, 1)
	require.Equal(t, errandA+":transfer", transfers[0].IdempotencyKey)
	require.Empty(t, h.store.OutboxKinds(errand.KindCreditRelease))

	transitions := h.store.Transitions()
	require.Len(t, transitions, 1)
	require.Equal(t, errand.EventAdminComplete, transitions[0].Event)
	require.Equal(t, errand.Actor{Type: errand.ActorAdmin, ID: adminID}, transitions[0].Actor)
	require.Equal(t, 6, transitions[0].ExpectedVersion)

	// Apply resolved the review row in the same transition.
	reviews := h.store.Reviews()
	require.Len(t, reviews, 1)
	require.Equal(t, errand.StatusCompleted, reviews[0].Resolution)
	require.Equal(t, adminID, reviews[0].ResolverID)
	require.Equal(t, "Photo shows the items were delivered", reviews[0].ResolutionReason)
	require.Equal(t, ptr(t0), reviews[0].ResolvedAt)

	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, "COMPLETED", body["status"])
	require.Equal(t, float64(7), body["version"])
	require.Equal(t, "ADMIN", body["viewerRole"])
	require.Equal(t, map[string]any{
		"reason":           "REQUESTER_DISPUTE",
		"createdAt":        "2026-03-02T08:00:00Z",
		"resolution":       "COMPLETED",
		"resolverId":       adminID,
		"resolutionReason": "Photo shows the items were delivered",
		"resolvedAt":       "2026-03-02T09:00:00Z",
	}, body["review"])
	require.NotContains(t, recorder.Body.String(), contactMobile)
}

func TestResolveRefundedReleasesToRequester_F6_1_3(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewCourierIssue, t0))

	recorder := h.resolve(t, errandA, resolution("REFUNDED", 6))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	stored, _ := h.store.Errand(errandA)
	require.Equal(t, errand.StatusRefunded, stored.Status)
	releases := h.store.OutboxKinds(errand.KindCreditRelease)
	require.Len(t, releases, 1)
	require.Equal(t, errandA+":release", releases[0].IdempotencyKey)
	require.Empty(t, h.store.OutboxKinds(errand.KindCreditTransfer))
	require.Equal(t, errand.StatusRefunded, h.store.Reviews()[0].Resolution)
}

func TestResolveWritesAuditRow_F6_5(t *testing.T) {
	for _, tc := range []struct {
		outcome, action string
	}{
		{"COMPLETED", review.AuditActionComplete},
		{"REFUNDED", review.AuditActionRefund},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			h := newHarness(t)
			h.store.Put(reviewErrand(errandA, errand.ReviewCourierIssue, t0))

			require.Equal(t, http.StatusOK, h.resolve(t, errandA, resolution(tc.outcome, 6)).Code)

			require.Equal(t, []opsrepo.AuditRecord{{
				ActorID:    adminID,
				Action:     tc.action,
				TargetType: review.AuditTargetType,
				TargetID:   errandA,
				Reason:     "Photo shows the items were delivered",
				Outcome:    opsrepo.AuditSucceeded,
			}}, h.audit.all())
			// The free-text reason is audited, never logged.
			require.NotContains(t, h.logs.String(), "Photo shows")
		})
	}
}

func TestResolveTwiceIsUnavailableAndHasNoEffect_F6_1(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewCourierIssue, t0))
	require.Equal(t, http.StatusOK, h.resolve(t, errandA, resolution("COMPLETED", 6)).Code)
	before, _ := h.store.Errand(errandA)

	for _, body := range []string{resolution("COMPLETED", 6), resolution("REFUNDED", 7)} {
		recorder := h.resolve(t, errandA, body)

		require.Equal(t, http.StatusConflict, recorder.Code, body)
		require.Equal(t, apperror.ErrandUnavailable, decodeError(t, recorder).Code)
	}
	after, _ := h.store.Errand(errandA)
	require.Equal(t, before, after)
	require.Len(t, h.store.OutboxKinds(errand.KindCreditTransfer), 1, "no second credit row")
	require.Empty(t, h.store.OutboxKinds(errand.KindCreditRelease))
	require.Len(t, h.store.Transitions(), 1)

	records := h.audit.all()
	require.Len(t, records, 3)
	for _, record := range records[1:] {
		require.Equal(t, opsrepo.AuditRejected, record.Outcome)
		require.Equal(t, string(apperror.ErrandUnavailable), record.Reason)
	}
}

func TestResolveStaleVersionIsChanged_F3_3_7(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewCourierIssue, t0))

	recorder := h.resolve(t, errandA, resolution("COMPLETED", 5))

	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Equal(t, apperror.ErrandChanged, decodeError(t, recorder).Code)
	stored, _ := h.store.Errand(errandA)
	require.Equal(t, errand.StatusReviewRequired, stored.Status)
	require.Empty(t, h.store.Transitions())
	require.Empty(t, h.store.Outbox())
	require.Empty(t, h.store.Reviews()[0].Resolution)
	require.Equal(t, opsrepo.AuditRejected, h.audit.all()[0].Outcome)
}

func TestResolveRequiresReason_F6_1_4(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewCourierIssue, t0))

	for _, tc := range []struct {
		name, body, message string
	}{
		{"missing", `{"outcome":"COMPLETED","expectedVersion":6}`, "is required"},
		{"empty", `{"outcome":"COMPLETED","reason":"","expectedVersion":6}`, "must not be blank"},
		{"blank", `{"outcome":"COMPLETED","reason":" \t\n","expectedVersion":6}`, "must not be blank"},
		{"too long", `{"outcome":"COMPLETED","reason":"` + strings.Repeat("é", 501) + `","expectedVersion":6}`, "must be at most 500 characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := h.resolve(t, errandA, tc.body)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			body := decodeError(t, recorder)
			require.Equal(t, apperror.ValidationFailed, body.Code)
			require.Equal(t, []apperror.Field{{Field: "reason", Message: tc.message}}, body.Fields)
		})
	}
	require.Equal(t, http.StatusOK, h.resolve(t, errandA, `{"outcome":"COMPLETED","reason":"`+strings.Repeat("é", 500)+`","expectedVersion":6}`).Code)
	require.Len(t, h.store.Transitions(), 1, "only the valid request resolved the review")
}

func TestResolveValidatesEveryField_NFR2_2_1(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewCourierIssue, t0))

	for _, tc := range []struct {
		name, body string
		want       []apperror.Field
	}{
		{"all missing", `{}`, []apperror.Field{
			{Field: "outcome", Message: "is required"},
			{Field: "reason", Message: "is required"},
			{Field: "expectedVersion", Message: "is required"},
		}},
		{"bad values", `{"outcome":"PAID","reason":"ok","expectedVersion":0}`, []apperror.Field{
			{Field: "outcome", Message: "must be COMPLETED or REFUNDED"},
			{Field: "expectedVersion", Message: "must be a positive integer"},
		}},
		{"lower-case outcome", `{"outcome":"completed","reason":"ok","expectedVersion":6}`, []apperror.Field{
			{Field: "outcome", Message: "must be COMPLETED or REFUNDED"},
		}},
		{"fractional version", `{"outcome":"COMPLETED","reason":"ok","expectedVersion":6.5}`, []apperror.Field{
			{Field: "expectedVersion", Message: "must be a positive integer"},
		}},
		{"numeric reason", `{"outcome":"COMPLETED","reason":7,"expectedVersion":6}`, []apperror.Field{
			{Field: "reason", Message: "must be a string"},
		}},
		{"unknown field", `{"outcome":"COMPLETED","reason":"ok","expectedVersion":6,"resolverId":"auth0|someone-else"}`, []apperror.Field{
			{Field: "resolverId", Message: "is not allowed"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := h.resolve(t, errandA, tc.body)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			body := decodeError(t, recorder)
			require.Equal(t, apperror.ValidationFailed, body.Code)
			require.Equal(t, tc.want, body.Fields)
		})
	}
	for _, body := range []string{"", "[]", "not json", `{"outcome":"COMPLETED"} {}`} {
		recorder := h.resolve(t, errandA, body)
		require.Equal(t, http.StatusBadRequest, recorder.Code, body)
		require.Equal(t, apperror.InvalidArgument, decodeError(t, recorder).Code, body)
	}
	require.Empty(t, h.store.Transitions())
	require.Empty(t, h.audit.all(), "requests refused before reaching the errand are not audited")
}

func TestResolveRefusesErrandNotInReview_NFR6_1_1(t *testing.T) {
	h := newHarness(t)
	h.store.Put(pickedUpErrand(errandA))

	recorder := h.resolve(t, errandA, resolution("COMPLETED", 5))
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Equal(t, apperror.InvalidTransition, decodeError(t, recorder).Code)

	recorder = h.resolve(t, errandB, resolution("COMPLETED", 5))
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Equal(t, apperror.ErrandNotFound, decodeError(t, recorder).Code)

	recorder = h.resolve(t, "not-a-uuid", resolution("COMPLETED", 5))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, apperror.InvalidArgument, decodeError(t, recorder).Code)

	require.Empty(t, h.store.Transitions())
	require.Empty(t, h.store.Outbox())
}

func TestResolveAcceptsUpperCaseErrandID(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewCourierIssue, t0))

	recorder := h.resolve(t, strings.ToUpper(errandA), resolution("REFUNDED", 6))

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	stored, _ := h.store.Errand(errandA)
	require.Equal(t, errand.StatusRefunded, stored.Status)
}

func TestResolveChangesNothingWhenSupplierServiceIsDown(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewCourierIssue, t0))
	h.suppliers.FailNext("GetVersion", &apperror.Error{Code: apperror.DependencyUnavailable, Message: "supplier-service is unavailable"})

	recorder := h.resolve(t, errandA, resolution("COMPLETED", 6))

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, apperror.DependencyUnavailable, decodeError(t, recorder).Code)
	stored, _ := h.store.Errand(errandA)
	require.Equal(t, errand.StatusReviewRequired, stored.Status)
	require.Empty(t, h.store.Outbox())

	// The retry succeeds.
	require.Equal(t, http.StatusOK, h.resolve(t, errandA, resolution("COMPLETED", 6)).Code)
}

func TestInternalFailureIsGeneric(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewCourierIssue, t0))
	h.store.FailNext("Apply", errors.New("connection reset by peer"))

	recorder := h.resolve(t, errandA, resolution("COMPLETED", 6))

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	body := decodeError(t, recorder)
	require.Equal(t, apperror.Internal, body.Code)
	require.NotContains(t, recorder.Body.String(), "connection reset")
	require.Contains(t, h.logs.String(), "review request failed")
	require.Empty(t, h.audit.all(), "an internal failure is not a refusal")
}

// ---------------------------------------------------------------------------
// Authorization
// ---------------------------------------------------------------------------

func TestRoutesRequireReviewPermission_F1_6_2_NFR3_3(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewCourierIssue, t0))
	f := h.fixture
	user := f.Token(t, f.Claims(auth.OrderAudience, []string{string(auth.UseErrands)}), "one")
	service := f.Token(t, f.ServiceClaims(auth.OrderAudience, []string{string(auth.ReviewErrands)}), "one")

	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/admin/reviews", ""},
		{http.MethodPost, "/admin/reviews/" + errandA + "/resolve", resolution("COMPLETED", 6)},
	}
	for _, route := range routes {
		t.Run(route.method, func(t *testing.T) {
			for _, tc := range []struct {
				name, token string
				status      int
				code        apperror.Code
			}{
				{"no token", "", http.StatusUnauthorized, apperror.Unauthenticated},
				{"user without errands:review", user, http.StatusForbidden, apperror.Forbidden},
				{"service token with errands:review", service, http.StatusForbidden, apperror.Forbidden},
			} {
				recorder := h.do(t, route.method, route.path, tc.token, route.body)
				require.Equal(t, tc.status, recorder.Code, tc.name)
				require.Equal(t, tc.code, decodeError(t, recorder).Code, tc.name)
			}
		})
	}
	stored, _ := h.store.Errand(errandA)
	require.Equal(t, errand.StatusReviewRequired, stored.Status)
	require.Empty(t, h.store.Transitions())
	require.Empty(t, h.audit.all())
}

func TestResolverIsTheTokenSubject_NFR3_3_1(t *testing.T) {
	h := newHarness(t)
	h.store.Put(reviewErrand(errandA, errand.ReviewCourierIssue, t0))

	require.Equal(t, http.StatusOK, h.do(t, http.MethodPost, "/admin/reviews/"+errandA+"/resolve?resolverId=auth0|someone-else",
		h.admin, resolution("COMPLETED", 6)).Code)

	require.Equal(t, adminID, h.store.Reviews()[0].ResolverID)
	require.Equal(t, adminID, h.audit.all()[0].ActorID)
}
