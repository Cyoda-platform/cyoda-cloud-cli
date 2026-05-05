package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Revoke calls Auth0's /oauth/revoke endpoint to invalidate a refresh token.
// Per RFC 7009 §2.2 the endpoint is idempotent — unknown tokens still return
// 200. Callers that fail this call should still proceed to delete local
// state: the user wants out regardless.
func Revoke(ctx context.Context, auth0Domain, clientID, refreshToken string) error {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("token", refreshToken)
	form.Set("token_type_hint", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authBaseURL(auth0Domain)+"/oauth/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("revoke: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("revoke: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("revoke: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// SetAuthBaseURLForTest overrides the package-level Auth0 base URL and
// returns a restore function. Tests use this to point auth code at an
// httptest server. Not safe for concurrent test use — wrap calls in the
// authBaseURLMu test mutex when running parallel tests in the same package.
func SetAuthBaseURLForTest(u string) (restore func()) {
	prev := authBaseURL
	authBaseURL = func(_ string) string { return u }
	return func() { authBaseURL = prev }
}
