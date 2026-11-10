package servicehealth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func serve(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL
}

func respond(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func check(t *testing.T, targets []Target, notChecked []NotChecked, timeout time.Duration) Report {
	t.Helper()
	checker, err := New(targets, notChecked, timeout)
	require.NoError(t, err)
	return checker.Check(context.Background())
}

func TestReadyServiceWithoutDependencyDetail(t *testing.T) {
	// supplier-service and credit-service answer only {"status":"ready"}.
	url := serve(t, respond(http.StatusOK, `{"status":"ready"}`))

	report := check(t, []Target{{"supplier-service", url}}, nil, time.Second)

	require.Len(t, report.Services, 1)
	s := report.Services[0]
	require.Equal(t, "supplier-service", s.Name)
	require.Equal(t, Up, s.State)
	require.Equal(t, "ready", s.Status)
	require.Nil(t, s.Dependencies, "nothing invented: the service reports none")
	require.Empty(t, s.Reason)
}

func TestDegradedServiceIsUpAndKeepsItsDependencyDetail(t *testing.T) {
	// order-service's shape (internal/readiness).
	url := serve(t, respond(http.StatusOK, `{"status":"degraded","dependencies":{"postgres":"ok","redis":"disabled",
		"rabbitmq":"degraded","creditResults":"stopped","supplier":"half_open"}}`))

	report := check(t, []Target{{"order-service", url}}, nil, time.Second)

	s := report.Services[0]
	require.Equal(t, Up, s.State, "degraded still serves")
	require.Equal(t, "degraded", s.Status)
	require.Equal(t, map[string]string{
		"postgres": "ok", "redis": "disabled", "rabbitmq": "degraded", "creditResults": "stopped", "supplier": "half_open",
	}, s.Dependencies)
}

func TestNotReadyServiceIsDown(t *testing.T) {
	url := serve(t, respond(http.StatusServiceUnavailable, `{"status":"not_ready"}`))

	s := check(t, []Target{{"credit-service", url}}, nil, time.Second).Services[0]

	require.Equal(t, Down, s.State)
	require.Equal(t, ReasonNotReady, s.Reason)
	require.Empty(t, s.Status)
	require.Nil(t, s.Dependencies)
}

func TestOtherStatusesAreDown(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusNotFound, http.StatusBadGateway, http.StatusUnauthorized, http.StatusNoContent} {
		url := serve(t, respond(status, `{"status":"ready"}`))

		s := check(t, []Target{{"x", url}}, nil, time.Second).Services[0]

		require.Equal(t, Down, s.State, status)
		require.Equal(t, ReasonUnexpected, s.Reason, status)
	}
}

func TestUnreachableAndSlowServices(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	slow := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})

	report := check(t, []Target{{"gone", closedURL}, {"slow", slow}}, nil, 100*time.Millisecond)

	require.Equal(t, Down, report.Services[0].State)
	require.Equal(t, ReasonUnreachable, report.Services[0].Reason)
	require.Equal(t, Down, report.Services[1].State)
	require.Equal(t, ReasonTimeout, report.Services[1].Reason)
	for _, s := range report.Services {
		require.NotContains(t, s.Name+string(s.Reason), "127.0.0.1", "no address leaks")
	}
}

func TestOneDownServiceDoesNotHideTheOthersAndOrderIsKept(t *testing.T) {
	up := serve(t, respond(http.StatusOK, `{"status":"ready"}`))
	down := serve(t, respond(http.StatusServiceUnavailable, `{"status":"not_ready"}`))

	report := check(t, []Target{{"a", up}, {"b", down}, {"c", up}}, nil, time.Second)

	var names []string
	var states []State
	for _, s := range report.Services {
		names, states = append(names, s.Name), append(states, s.State)
	}
	require.Equal(t, []string{"a", "b", "c"}, names)
	require.Equal(t, []State{Up, Down, Up}, states)
}

func TestChecksRunAtOnce(t *testing.T) {
	slow := func() string {
		return serve(t, func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		})
	}
	started := time.Now()

	report := check(t, []Target{{"a", slow()}, {"b", slow()}, {"c", slow()}}, nil, 2*time.Second)

	require.Less(t, time.Since(started), 800*time.Millisecond, "three 300 ms checks overlap")
	for _, s := range report.Services {
		require.Equal(t, Up, s.State)
		require.GreaterOrEqual(t, s.LatencyMillis, int64(250))
	}
}

func TestFreeFormContentFromAnotherServiceIsNotPassedOn(t *testing.T) {
	body := `{"status":"ready","dependencies":{
		"postgres":"ok",
		"db":"postgres://admin:secret@db:5432/x",
		"has space":"ok",
		"nested":{"a":"b"},
		"number":5,
		"long":"` + strings.Repeat("x", 100) + `",
		"<script>":"ok"}}`
	url := serve(t, respond(http.StatusOK, body))

	s := check(t, []Target{{"x", url}}, nil, time.Second).Services[0]

	require.Equal(t, Up, s.State)
	require.Equal(t, map[string]string{"postgres": "ok"}, s.Dependencies, "only entries in /readyz's vocabulary survive")
}

func TestUnexpectedBodiesFromAHealthyStatus(t *testing.T) {
	for name, body := range map[string]string{
		"not json":            `<html>ok</html>`,
		"empty":               ``,
		"status not a word":   `{"status":"ready; drop table"}`,
		"dependencies a list": `{"status":"ready","dependencies":["a"]}`,
		"too many":            `{"status":"ready","dependencies":{` + manyDependencies(20) + `}}`,
		"dependencies empty":  `{"status":"ready","dependencies":{}}`,
		"dependencies null":   `{"status":"ready","dependencies":null}`,
	} {
		url := serve(t, respond(http.StatusOK, body))

		s := check(t, []Target{{"x", url}}, nil, time.Second).Services[0]

		require.Equal(t, Up, s.State, name)
		require.Equal(t, "ready", s.Status, "%s: a 200 from /readyz means ready", name)
		require.Nil(t, s.Dependencies, name)
	}
}

func manyDependencies(n int) string {
	var parts []string
	for i := range n {
		parts = append(parts, `"d`+strings.Repeat("a", i)+`":"ok"`)
	}
	return strings.Join(parts, ",")
}

func TestAnOversizedBodyIsNotReadInFull(t *testing.T) {
	url := serve(t, respond(http.StatusOK, `{"status":"ready","pad":"`+strings.Repeat("x", 1<<20)+`"}`))

	s := check(t, []Target{{"x", url}}, nil, time.Second).Services[0]

	require.Equal(t, Up, s.State)
	require.Equal(t, "ready", s.Status)
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var elsewhere int
	other := serve(t, func(http.ResponseWriter, *http.Request) { elsewhere++ })
	url := serve(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other, http.StatusFound) })

	s := check(t, []Target{{"x", url}}, nil, time.Second).Services[0]

	require.Equal(t, Down, s.State)
	require.Equal(t, ReasonUnexpected, s.Reason)
	require.Zero(t, elsewhere)
}

func TestNotCheckedServicesAreReportedAsGiven(t *testing.T) {
	report := check(t, nil, []NotChecked{{"user-service", "no /readyz endpoint"}}, time.Second)

	require.Empty(t, report.Services)
	require.Equal(t, []NotChecked{{"user-service", "no /readyz endpoint"}}, report.NotChecked)
}

func TestNewValidates(t *testing.T) {
	_, err := New(nil, nil, 0)
	require.Error(t, err)
	for _, target := range []Target{{"", "http://x"}, {"x", ""}, {"x", "ftp://x"}, {"x", "http://"}, {"x", "not a url"}} {
		_, err = New([]Target{target}, nil, time.Second)
		require.Error(t, err, target)
	}
	_, err = New([]Target{{"x", "http://a"}, {"x", "http://b"}}, nil, time.Second)
	require.Error(t, err, "duplicate names")
}

func TestTrailingSlashInTheURLIsFine(t *testing.T) {
	url := serve(t, respond(http.StatusOK, `{"status":"ready"}`))

	s := check(t, []Target{{"x", url + "/"}}, nil, time.Second).Services[0]

	require.Equal(t, Up, s.State)
}
