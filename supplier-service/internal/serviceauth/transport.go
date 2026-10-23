package serviceauth

import "net/http"

type transport struct {
	base http.RoundTripper
	src  TokenSource
}

// NewTransport returns a RoundTripper that sets "Authorization: Bearer <token>"
// from src on every request, replacing any Authorization header already set,
// and then sends it with base. A nil base uses http.DefaultTransport.
func NewTransport(base http.RoundTripper, src TokenSource) http.RoundTripper {
	if src == nil {
		panic("serviceauth: token source is required")
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{base: base, src: src}
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.src.Token(req.Context())
	if err != nil {
		// A RoundTripper must close the body even when it fails.
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	// A RoundTripper must not modify the caller's request.
	out := req.Clone(req.Context())
	out.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(out)
}
