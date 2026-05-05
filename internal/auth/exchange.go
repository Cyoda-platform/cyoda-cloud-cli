package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// exchangeToken trades the PKCE authorization code for tokens at the
// configured /oauth/token endpoint.
func exchangeToken(ctx context.Context, cfg LoopbackConfig, code string, verifier PKCEVerifier, redirectURI string) (Tokens, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", cfg.ClientID)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", string(verifier))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authBaseURL(cfg.Auth0Domain)+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, fmt.Errorf("token exchange: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Tokens{}, fmt.Errorf("token exchange: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Tokens{}, fmt.Errorf("token exchange: decode: %w", err)
	}
	return Tokens{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		IDToken:      out.IDToken,
		ExpiresAt:    time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
		Scope:        out.Scope,
	}, nil
}
