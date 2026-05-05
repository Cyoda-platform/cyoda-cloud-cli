package api

import (
	"net/http"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/auth"
)

// Transport is an http.RoundTripper that injects authentication and
// identification headers into each outgoing request and transparently
// retries once on 401.
//
// It is safe for concurrent use; the underlying TokenCache serialises token
// refreshes via its own mutex.
//
// Behaviour on RoundTrip:
//  1. Clone the caller's request (RoundTripper contract: don't mutate input).
//  2. Set Authorization: Bearer <token> from cache.AccessToken.
//  3. Set Cyoda-Cloud-CLI-Version and User-Agent.
//  4. Issue the request via Underlying (defaults to http.DefaultTransport).
//  5. On 401, invalidate the cached access token and retry exactly once with
//     a freshly-minted token. The cache's RefreshToken is preserved across
//     Invalidate; only AccessToken/ExpiresAt are zeroed, so the next
//     AccessToken call goes through the refresh path.
//  6. If the retry also returns 401, the second response bubbles up
//     unmodified — callers can map it to ErrSessionExpired surfaced via the
//     TokenCache.
//
// The RoundTripper does not read the response body; it leaves the body
// untouched so the caller (the generated API client) can decode it.
type Transport struct {
	// Underlying is the inner RoundTripper. nil means http.DefaultTransport.
	Underlying http.RoundTripper

	// Cache supplies access tokens and serves as the seam for forced refresh
	// on 401 via Invalidate.
	Cache *auth.TokenCache

	// CLIVersion is sent verbatim as the Cyoda-Cloud-CLI-Version header.
	CLIVersion string

	// UserAgent is sent verbatim as the User-Agent header.
	UserAgent string
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	// First 401: drain & close so the connection can be reused, then force a
	// token refresh and retry exactly once.
	resp.Body.Close()
	t.Cache.Invalidate()
	return t.do(req)
}

// do clones the caller's request, sets headers, and dispatches it. It is
// the unit retried by RoundTrip.
func (t *Transport) do(req *http.Request) (*http.Response, error) {
	tok, err := t.Cache.AccessToken(req.Context())
	if err != nil {
		return nil, err
	}
	cloned := req.Clone(req.Context())
	if cloned.Header == nil {
		cloned.Header = make(http.Header)
	}
	cloned.Header.Set("Authorization", "Bearer "+tok)
	if t.CLIVersion != "" {
		cloned.Header.Set("Cyoda-Cloud-CLI-Version", t.CLIVersion)
	}
	if t.UserAgent != "" {
		cloned.Header.Set("User-Agent", t.UserAgent)
	}
	rt := t.Underlying
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(cloned)
}
