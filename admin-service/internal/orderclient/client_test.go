package orderclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

const errandA = "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10"

func newClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL, staticToken("m2m-token"), 2*time.Second)
	require.NoError(t, err)
	return client
}

func TestErrandsByIDSendsTheTokenAndDecodesNames(t *testing.T) {
	var gotPath, gotIDs, gotAuth string
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotIDs, gotAuth = r.URL.Path, r.URL.Query().Get("ids"), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"items":[
			{"errandId":"` + errandA + `","status":"ASSIGNED","version":3,"courierId":"auth0|cou1","reward":8,
			 "supplierId":"s1","supplierName":"Kopi Corner","deliveryBuildingId":"b1","deliveryBuildingName":"COM1",
			 "createdAt":"2026-03-02T09:00:00Z","updatedAt":"2026-03-02T10:00:00Z"},
			{"errandId":"7a2d3c1f-9e5b-4f2c-8d4a-3b8e6f9c0d21","status":"OPEN","version":2,"courierId":null,"reward":5,
			 "supplierId":"s2","supplierName":null,"deliveryBuildingId":"b2","deliveryBuildingName":null,
			 "createdAt":"2026-03-02T09:00:00Z","updatedAt":"2026-03-02T10:00:00Z"}]}`))
	})

	errands, err := client.ErrandsByID(context.Background(), []string{errandA, "7a2d3c1f-9e5b-4f2c-8d4a-3b8e6f9c0d21"})

	require.NoError(t, err)
	require.Equal(t, "/internal/export/errands", gotPath)
	require.Equal(t, errandA+",7a2d3c1f-9e5b-4f2c-8d4a-3b8e6f9c0d21", gotIDs)
	require.Equal(t, "Bearer m2m-token", gotAuth)
	require.Len(t, errands, 2)
	require.Equal(t, "Kopi Corner", *errands[0].SupplierName)
	require.Equal(t, "auth0|cou1", *errands[0].CourierID)
	require.Nil(t, errands[1].SupplierName, "a null name stays nil")
	require.Nil(t, errands[1].CourierID)
	require.Nil(t, errands[1].DeliveryBuildingName)
}

func TestErrandsByIDRejectsBadListsWithoutCalling(t *testing.T) {
	var calls atomic.Int32
	client := newClient(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })

	_, err := client.ErrandsByID(context.Background(), nil)
	require.Error(t, err)
	_, err = client.ErrandsByID(context.Background(), make([]string, MaxIDs+1))
	require.Error(t, err)
	require.Zero(t, calls.Load())
}

func TestTransitionsAndErrandPages(t *testing.T) {
	var queries []string
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Path+"?"+r.URL.RawQuery)
		if strings.HasSuffix(r.URL.Path, "transitions") {
			_, _ = w.Write([]byte(`{"items":[{"id":4,"messageId":"` + errandA + `:accepted:3","type":"errand.accepted","errandId":"` + errandA + `",
				"errandVersion":3,"event":"accept","from":"OPEN","to":"ASSIGNED","courierId":"auth0|cou1","reward":8,
				"occurredAt":"2026-03-02T09:01:00Z"}],"nextCursor":"4"}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[],"nextCursor":"` + errandA + `"}`))
	})

	transitions, err := client.Transitions(context.Background(), "", 200)
	require.NoError(t, err)
	require.Equal(t, "4", transitions.NextCursor)
	event := transitions.Items[0].FactsEvent()
	require.Equal(t, errandA+":accepted:3", event.MessageID)
	require.Equal(t, "errand.accepted", event.Type)
	require.Equal(t, 3, event.Version)
	require.Equal(t, "OPEN", string(event.From))
	require.Equal(t, "auth0|cou1", event.CourierID)
	require.NoError(t, event.Validate())

	_, err = client.Transitions(context.Background(), "4", 200)
	require.NoError(t, err)
	page, err := client.Errands(context.Background(), errandA, 50)
	require.NoError(t, err)
	require.Equal(t, errandA, page.NextCursor)

	require.Equal(t, []string{
		"/internal/export/transitions?limit=200",
		"/internal/export/transitions?after=4&limit=200",
		"/internal/export/errands?after=" + errandA + "&limit=50",
	}, queries)
}

func TestUnavailableOrderServiceIsDistinguishedFromARefusal(t *testing.T) {
	for status, wantUnavailable := range map[int]bool{
		http.StatusInternalServerError: true, http.StatusServiceUnavailable: true, http.StatusBadGateway: true,
		http.StatusTooManyRequests: true,
		http.StatusUnauthorized:    false, http.StatusForbidden: false, http.StatusBadRequest: false,
	} {
		client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"code":"X","message":"secret-detail"}`))
		})

		_, err := client.ErrandsByID(context.Background(), []string{errandA})

		require.Error(t, err, status)
		require.Equal(t, wantUnavailable, errors.Is(err, ErrUnavailable), "status %d: %v", status, err)
		require.NotContains(t, err.Error(), "secret-detail")
	}
}

func TestConnectionFailureIsUnavailableAndHidesTheURL(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	client, err := New(url, staticToken("t"), time.Second)
	require.NoError(t, err)

	_, err = client.ErrandsByID(context.Background(), []string{errandA})

	require.ErrorIs(t, err, ErrUnavailable)
	require.NotContains(t, err.Error(), url)
}

func TestMalformedResponses(t *testing.T) {
	for name, body := range map[string]string{"not json": "<html>", "wrong shape": `{"items": 5}`} {
		client := newClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })

		_, err := client.ErrandsByID(context.Background(), []string{errandA})

		require.Error(t, err, name)
		require.NotErrorIs(t, err, ErrUnavailable, name)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	t.Cleanup(other.Close)
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	})

	_, err := client.ErrandsByID(context.Background(), []string{errandA})

	require.Error(t, err)
	require.Zero(t, elsewhere.Load(), "the bearer token is never sent to a redirect target")
}

func TestCancelledContextIsReturnedAsIs(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.ErrandsByID(ctx, []string{errandA})

	require.ErrorIs(t, err, context.Canceled)
}

func TestNewValidates(t *testing.T) {
	_, err := New("", staticToken("t"), time.Second)
	require.Error(t, err)
	_, err = New("http://x", nil, time.Second)
	require.Error(t, err)
	_, err = New("http://x", staticToken("t"), 0)
	require.Error(t, err)
}
