package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Errors returned by LoginDevice for caller-actionable conditions.
var (
	// ErrDeviceCodeExpired indicates the device-code TTL elapsed before the
	// user finished activation. The caller should restart the flow.
	ErrDeviceCodeExpired = errors.New("device code expired")
	// ErrDeviceCodeDenied indicates the user explicitly denied the request
	// at the activation page.
	ErrDeviceCodeDenied = errors.New("device authorization denied")
)

// nowFunc and sleepFunc are seams for tests to control the polling clock.
// Production callers see real time.Now / time.Sleep.
var (
	nowFunc   = time.Now
	sleepFunc = time.Sleep
)

// defaultDeviceInterval is the polling interval when /oauth/device/code omits
// "interval". RFC 8628 §3.5 mandates a 5-second default.
const defaultDeviceInterval = 5 * time.Second

// LoginDevice runs the OAuth 2.0 Device Authorization Grant flow (RFC 8628)
// against the configured Auth0 tenant.
//
// The activation URL and user code are written to cfg.Stderr (defaulting to
// os.Stderr) — per spec §6.5 status output goes to stderr so stdout stays
// usable for data piping.
//
// Polling honours the server-supplied interval, defaults to 5s when absent
// (RFC 8628 §3.5), and on a slow_down response increases the interval by
// 5 seconds per RFC 8628 §3.5. Note: docs/plan.md says "double" the interval —
// we deliberately follow the RFC instead.
//
// Expiry is enforced LOCALLY: a deadline is computed from the
// /oauth/device/code response's expires_in at request time, and each loop
// iteration checks nowFunc().After(deadline) before polling. This means we
// stop polling once our local clock says the code has expired, regardless of
// whether the server later returns an "expired_token" error code. Both paths
// surface as ErrDeviceCodeExpired to the caller; the local check just bounds
// how long we wait if the server is slow to acknowledge the expiry.
func LoginDevice(ctx context.Context, cfg LoopbackConfig) (Tokens, error) {
	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	dc, err := requestDeviceCode(ctx, cfg)
	if err != nil {
		return Tokens{}, err
	}

	fmt.Fprintln(stderr, "Open the following URL to log in:")
	fmt.Fprintln(stderr, "  "+dc.VerificationURIComplete)
	fmt.Fprintln(stderr, "Or visit "+dc.VerificationURI+" and enter code: "+dc.UserCode)

	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = defaultDeviceInterval
	}
	deadline := nowFunc().Add(time.Duration(dc.ExpiresIn) * time.Second)

	for {
		select {
		case <-ctx.Done():
			return Tokens{}, ctx.Err()
		default:
		}
		if nowFunc().After(deadline) {
			return Tokens{}, ErrDeviceCodeExpired
		}

		toks, errCode, err := pollDeviceToken(ctx, cfg, dc.DeviceCode)
		if err != nil {
			return Tokens{}, err
		}
		switch errCode {
		case "":
			return toks, checkRefreshTokenIssued(cfg.Scopes, toks)
		case "authorization_pending":
			// keep polling at the current interval
		case "slow_down":
			// RFC 8628 §3.5: increase the polling interval by 5 seconds.
			interval += 5 * time.Second
		case "expired_token":
			return Tokens{}, ErrDeviceCodeExpired
		case "access_denied":
			return Tokens{}, ErrDeviceCodeDenied
		default:
			return Tokens{}, fmt.Errorf("device flow: unexpected error %q", errCode)
		}
		// Wait the (possibly updated) interval before the next poll. Per
		// RFC 8628 §3.4 the client SHOULD wait at least `interval` between
		// attempts.
		sleepFunc(interval)
	}
}

// deviceCodeResponse mirrors the /oauth/device/code JSON body.
type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func requestDeviceCode(ctx context.Context, cfg LoopbackConfig) (deviceCodeResponse, error) {
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("audience", cfg.Audience)
	form.Set("scope", strings.Join(cfg.Scopes, " "))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authBaseURL(cfg.Auth0Domain)+"/oauth/device/code", strings.NewReader(form.Encode()))
	if err != nil {
		return deviceCodeResponse{}, fmt.Errorf("device code: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return deviceCodeResponse{}, fmt.Errorf("device code: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return deviceCodeResponse{}, fmt.Errorf("device code: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "application/json") {
		return deviceCodeResponse{}, fmt.Errorf("device code: unexpected content-type %q", ct)
	}
	var out deviceCodeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return deviceCodeResponse{}, fmt.Errorf("device code: decode: %w", err)
	}
	if out.DeviceCode == "" || out.UserCode == "" || out.VerificationURI == "" {
		return deviceCodeResponse{}, fmt.Errorf("device code: incomplete response")
	}
	return out, nil
}

// pollDeviceToken issues one /oauth/token poll. Returns either tokens (errCode
// empty), or a non-empty errCode such as "authorization_pending" / "slow_down"
// / "expired_token" / "access_denied" along with no tokens and no Go-level
// error. Transport-level failures bubble up via err.
func pollDeviceToken(ctx context.Context, cfg LoopbackConfig, deviceCode string) (Tokens, string, error) {
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("device_code", deviceCode)
	form.Set("client_id", cfg.ClientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authBaseURL(cfg.Auth0Domain)+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, "", fmt.Errorf("device poll: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Tokens{}, "", fmt.Errorf("device poll: %w", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "application/json") {
		return Tokens{}, "", fmt.Errorf("device poll: unexpected content-type %q", ct)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusOK {
		var out struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			IDToken      string `json:"id_token"`
			ExpiresIn    int    `json:"expires_in"`
			Scope        string `json:"scope"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return Tokens{}, "", fmt.Errorf("device poll: decode: %w", err)
		}
		return Tokens{
			AccessToken:  out.AccessToken,
			RefreshToken: out.RefreshToken,
			IDToken:      out.IDToken,
			ExpiresAt:    nowFunc().Add(time.Duration(out.ExpiresIn) * time.Second),
			Scope:        out.Scope,
		}, "", nil
	}
	// Auth0 returns 4xx with {"error":"...","error_description":"..."} for
	// the in-band conditions described by RFC 8628.
	var errBody struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &errBody); err != nil || errBody.Error == "" {
		return Tokens{}, "", fmt.Errorf("device poll: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return Tokens{}, errBody.Error, nil
}
