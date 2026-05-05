package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrSessionExpired is returned when the refresh token is no longer accepted
// by Auth0 (rotation reuse, revocation, absolute lifetime exceeded). Per spec
// §6.3 the user-facing message is `Session expired; run "cyoda-cloud login".`,
// but the typed sentinel itself is terser; callers map it to that prompt and
// to exit code 3 (unauthenticated).
var ErrSessionExpired = errors.New("session expired")

// RefreshConfig holds the inputs needed to mint a new access token from a
// refresh token. It is a sibling of LoopbackConfig — kept separate because
// refresh has no notion of browsers, scopes, or stderr.
type RefreshConfig struct {
	Auth0Domain  string
	ClientID     string
	RefreshToken string
}

// Refresh exchanges a refresh token for a fresh access token (and, when the
// tenant rotates, a new refresh token).
//
// On invalid_grant the returned error wraps ErrSessionExpired so callers can
// distinguish "user must log in again" from transient failures.
//
// Persistence is the caller's responsibility. When the response includes a
// new refresh_token the returned Tokens.RefreshToken carries it; when omitted
// the original RefreshToken from cfg is preserved so the caller never
// accidentally writes an empty string back to the keychain.
func Refresh(ctx context.Context, cfg RefreshConfig) (Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", cfg.ClientID)
	form.Set("refresh_token", cfg.RefreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authBaseURL(cfg.Auth0Domain)+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, fmt.Errorf("refresh: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("refresh: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		// Try to decode an Auth0 error object so invalid_grant maps cleanly.
		var errBody struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &errBody)
		if errBody.Error == "invalid_grant" {
			return Tokens{}, fmt.Errorf("refresh: %w: %s", ErrSessionExpired, errBody.ErrorDescription)
		}
		return Tokens{}, fmt.Errorf("refresh: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "application/json") {
		return Tokens{}, fmt.Errorf("refresh: unexpected content-type %q", ct)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Tokens{}, fmt.Errorf("refresh: decode: %w", err)
	}
	rt := out.RefreshToken
	if rt == "" {
		// Auth0 may omit refresh_token when rotation is off or for some
		// tenants — keep the caller's RT so we don't blank it out.
		rt = cfg.RefreshToken
	}
	return Tokens{
		AccessToken:  out.AccessToken,
		RefreshToken: rt,
		IDToken:      out.IDToken,
		ExpiresAt:    nowFunc().Add(time.Duration(out.ExpiresIn) * time.Second),
		Scope:        out.Scope,
	}, nil
}
