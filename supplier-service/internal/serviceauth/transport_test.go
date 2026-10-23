package serviceauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeSource struct {
	token string
	err   error
}

func (f fakeSource) Token(context.Context) (string, error) { return f.token, f.err }

type closeTracker struct {
	io.Reader
	closed atomic.Bool
}

func (c *closeTracker) Close() error {
	c.closed.Store(true)
	return nil
}

func echoAuthorization(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, r.Header.Get("Authorization"))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestTransportSetsBearerHeader(t *testing.T) {
	server, _ := echoAuthorization(t)
	client := &http.Client{Transport: NewTransport(nil, fakeSource{token: "service-token"})}

	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Basic caller-supplied")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, "Bearer service-token", string(body))
	require.Equal(t, "Basic caller-supplied", req.Header.Get("Authorization"), "caller's request must not be modified")
}

func TestTransportDoesNotSendWhenTokenUnavailable(t *testing.T) {
	server, hits := echoAuthorization(t)
	sourceErr := errors.New("serviceauth: token endpoint returned status 401")
	client := &http.Client{Transport: NewTransport(http.DefaultTransport, fakeSource{err: sourceErr})}

	body := &closeTracker{Reader: strings.NewReader("payload")}
	req, err := http.NewRequest(http.MethodPost, server.URL, body)
	require.NoError(t, err)
	_, err = client.Do(req)

	require.ErrorIs(t, err, sourceErr)
	require.True(t, body.closed.Load())
	require.Zero(t, hits.Load())
}

func TestTransportWithClientCredentialsFetchesOnce(t *testing.T) {
	stub := newTokenStub(t)
	src, _ := newSource(t, stub)
	server, _ := echoAuthorization(t)
	client := &http.Client{Transport: NewTransport(nil, src)}

	for range 3 {
		resp, err := client.Get(server.URL)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, "Bearer token-1", string(body))
	}
	require.EqualValues(t, 1, stub.hits.Load())
}

func TestNewTransportRequiresSource(t *testing.T) {
	require.Panics(t, func() { NewTransport(nil, nil) })
}
