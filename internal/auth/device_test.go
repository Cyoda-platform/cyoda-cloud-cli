package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// withFakeDeviceAuth0 stands up an httptest server emulating Auth0's
// /oauth/device/code and /oauth/token device-flow endpoints.
//
// deviceCodeBody is the JSON returned by /oauth/device/code.
// tokenResponses is a slice of (status, body) pairs returned in order from
// /oauth/token (one response per call).
func withFakeDeviceAuth0(t *testing.T, deviceCodeBody string, tokenResponses []deviceTokenResp) (*httptest.Server, *deviceCallStats, func()) {
	t.Helper()
	stats := &deviceCallStats{}
	var idx int32
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/device/code", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stats.deviceCodeCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(deviceCodeBody))
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&idx, 1)) - 1
		atomic.AddInt32(&stats.tokenCalls, 1)
		if i >= len(tokenResponses) {
			i = len(tokenResponses) - 1
		}
		resp := tokenResponses[i]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		_, _ = w.Write([]byte(resp.body))
	})
	srv := httptest.NewServer(mux)
	authBaseURLMu.Lock()
	prev := authBaseURL
	authBaseURL = func(_ string) string { return srv.URL }
	cleanup := func() {
		authBaseURL = prev
		authBaseURLMu.Unlock()
		srv.Close()
	}
	return srv, stats, cleanup
}

type deviceTokenResp struct {
	status int
	body   string
}

type deviceCallStats struct {
	deviceCodeCalls int32
	tokenCalls      int32
}

// withSleepRecorder replaces sleepFunc to record durations without sleeping.
func withSleepRecorder(t *testing.T) *[]time.Duration {
	t.Helper()
	prev := sleepFunc
	var (
		mu     sync.Mutex
		sleeps []time.Duration
	)
	sleepFunc = func(d time.Duration) {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
	}
	t.Cleanup(func() { sleepFunc = prev })
	return &sleeps
}

func TestLoginDevice_HappyPath(t *testing.T) {
	deviceBody := `{
		"device_code":"DEVICE",
		"user_code":"ABCD-1234",
		"verification_uri":"https://example.auth0.com/activate",
		"verification_uri_complete":"https://example.auth0.com/activate?user_code=ABCD-1234",
		"expires_in":900,
		"interval":2
	}`
	tokens := []deviceTokenResp{
		{http.StatusForbidden, `{"error":"authorization_pending"}`},
		{http.StatusForbidden, `{"error":"authorization_pending"}`},
		{http.StatusOK, `{"access_token":"AT","refresh_token":"RT","id_token":"IT","expires_in":3600,"scope":"openid profile"}`},
	}
	_, stats, cleanup := withFakeDeviceAuth0(t, deviceBody, tokens)
	defer cleanup()
	sleeps := withSleepRecorder(t)

	var stderr bytes.Buffer
	cfg := LoopbackConfig{
		Auth0Domain: "ignored.example",
		ClientID:    "client",
		Audience:    "https://api.cyoda.cloud",
		Scopes:      []string{"openid", "profile"},
		Stderr:      &stderr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	toks, err := LoginDevice(ctx, cfg)
	if err != nil {
		t.Fatalf("LoginDevice: %v", err)
	}
	if toks.AccessToken != "AT" || toks.RefreshToken != "RT" || toks.IDToken != "IT" {
		t.Errorf("tokens = %+v", toks)
	}
	if toks.Scope != "openid profile" {
		t.Errorf("scope = %q", toks.Scope)
	}
	if toks.ExpiresAt.Before(time.Now().Add(50 * time.Minute)) {
		t.Errorf("ExpiresAt too soon: %v", toks.ExpiresAt)
	}
	if got := atomic.LoadInt32(&stats.tokenCalls); got != 3 {
		t.Errorf("token calls = %d, want 3", got)
	}
	// Two pending responses → two sleeps before the success.
	if len(*sleeps) < 2 {
		t.Errorf("sleeps = %v, want >=2", *sleeps)
	} else {
		for _, d := range (*sleeps)[:2] {
			if d != 2*time.Second {
				t.Errorf("expected 2s interval, got %v", d)
			}
		}
	}
	out := stderr.String()
	if !strings.Contains(out, "https://example.auth0.com/activate?user_code=ABCD-1234") {
		t.Errorf("stderr missing verification_uri_complete: %q", out)
	}
	if !strings.Contains(out, "ABCD-1234") {
		t.Errorf("stderr missing user_code: %q", out)
	}
	if !strings.Contains(out, "https://example.auth0.com/activate") {
		t.Errorf("stderr missing verification_uri: %q", out)
	}
}

// Device-flow analogue of TestLoginPKCE_NoRefreshTokenWhenRequestedReturnsTypedError.
// Same Auth0 misconfiguration (Allow Offline Access OFF on the API) silently
// drops offline_access from the granted scope set, so the 200 response carries
// access_token but no refresh_token. We surface a typed error with a
// remediation hint instead of returning empty Tokens.
func TestLoginDevice_NoRefreshTokenWhenRequestedReturnsTypedError(t *testing.T) {
	deviceBody := `{
		"device_code":"DEVICE",
		"user_code":"ABCD-1234",
		"verification_uri":"https://example.auth0.com/activate",
		"verification_uri_complete":"https://example.auth0.com/activate?user_code=ABCD-1234",
		"expires_in":900,
		"interval":1
	}`
	tokens := []deviceTokenResp{
		{http.StatusOK, `{"access_token":"AT","id_token":"IT","expires_in":3600,"scope":"openid profile"}`},
	}
	_, _, cleanup := withFakeDeviceAuth0(t, deviceBody, tokens)
	defer cleanup()
	withSleepRecorder(t)

	cfg := LoopbackConfig{
		Auth0Domain: "ignored.example",
		ClientID:    "client",
		Audience:    "https://api.cyoda.cloud",
		Scopes:      []string{"openid", "profile", "offline_access"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := LoginDevice(ctx, cfg)
	if !errors.Is(err, ErrRefreshTokenNotIssued) {
		t.Fatalf("expected ErrRefreshTokenNotIssued, got %v", err)
	}
}

func TestLoginDevice_NoRefreshTokenAcceptedWhenNotRequested(t *testing.T) {
	deviceBody := `{
		"device_code":"DEVICE",
		"user_code":"ABCD-1234",
		"verification_uri":"https://example.auth0.com/activate",
		"verification_uri_complete":"https://example.auth0.com/activate?user_code=ABCD-1234",
		"expires_in":900,
		"interval":1
	}`
	tokens := []deviceTokenResp{
		{http.StatusOK, `{"access_token":"AT","id_token":"IT","expires_in":3600,"scope":"openid profile"}`},
	}
	_, _, cleanup := withFakeDeviceAuth0(t, deviceBody, tokens)
	defer cleanup()
	withSleepRecorder(t)

	cfg := LoopbackConfig{
		Auth0Domain: "ignored.example",
		ClientID:    "client",
		Audience:    "https://api.cyoda.cloud",
		Scopes:      []string{"openid", "profile"}, // user opted out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	toks, err := LoginDevice(ctx, cfg)
	if err != nil {
		t.Fatalf("LoginDevice: %v", err)
	}
	if toks.RefreshToken != "" {
		t.Errorf("RefreshToken = %q, want empty", toks.RefreshToken)
	}
}

func TestLoginDevice_SlowDownIncreasesIntervalBy5s(t *testing.T) {
	deviceBody := `{
		"device_code":"DEVICE",
		"user_code":"AB-12",
		"verification_uri":"https://example.auth0.com/activate",
		"verification_uri_complete":"https://example.auth0.com/activate?user_code=AB-12",
		"expires_in":900,
		"interval":5
	}`
	tokens := []deviceTokenResp{
		{http.StatusForbidden, `{"error":"slow_down"}`},
		{http.StatusOK, `{"access_token":"AT","refresh_token":"RT","expires_in":3600}`},
	}
	_, _, cleanup := withFakeDeviceAuth0(t, deviceBody, tokens)
	defer cleanup()
	sleeps := withSleepRecorder(t)

	cfg := LoopbackConfig{Auth0Domain: "ignored.example", ClientID: "c", Audience: "a", Scopes: []string{"openid"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := LoginDevice(ctx, cfg); err != nil {
		t.Fatalf("LoginDevice: %v", err)
	}
	if len(*sleeps) < 1 {
		t.Fatalf("sleeps = %v, want >=1", *sleeps)
	}
	// Per RFC 8628 §3.5 slow_down adds 5s. Initial interval was 5s → 10s.
	if (*sleeps)[0] != 10*time.Second {
		t.Errorf("first sleep after slow_down = %v, want 10s", (*sleeps)[0])
	}
}

func TestLoginDevice_AccessDenied(t *testing.T) {
	deviceBody := `{"device_code":"D","user_code":"U","verification_uri":"x","verification_uri_complete":"x","expires_in":900,"interval":1}`
	tokens := []deviceTokenResp{
		{http.StatusForbidden, `{"error":"access_denied"}`},
	}
	_, _, cleanup := withFakeDeviceAuth0(t, deviceBody, tokens)
	defer cleanup()
	withSleepRecorder(t)

	cfg := LoopbackConfig{Auth0Domain: "ignored.example", ClientID: "c", Audience: "a", Scopes: []string{"openid"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := LoginDevice(ctx, cfg)
	if !errors.Is(err, ErrDeviceCodeDenied) {
		t.Errorf("err = %v, want ErrDeviceCodeDenied", err)
	}
}

func TestLoginDevice_ExpiredToken(t *testing.T) {
	deviceBody := `{"device_code":"D","user_code":"U","verification_uri":"x","verification_uri_complete":"x","expires_in":900,"interval":1}`
	tokens := []deviceTokenResp{
		{http.StatusForbidden, `{"error":"expired_token"}`},
	}
	_, _, cleanup := withFakeDeviceAuth0(t, deviceBody, tokens)
	defer cleanup()
	withSleepRecorder(t)

	cfg := LoopbackConfig{Auth0Domain: "ignored.example", ClientID: "c", Audience: "a", Scopes: []string{"openid"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := LoginDevice(ctx, cfg)
	if !errors.Is(err, ErrDeviceCodeExpired) {
		t.Errorf("err = %v, want ErrDeviceCodeExpired", err)
	}
}

func TestLoginDevice_DefaultIntervalWhenOmitted(t *testing.T) {
	// RFC 8628 §3.5: default to 5s when interval is omitted.
	deviceBody := `{"device_code":"D","user_code":"U","verification_uri":"x","verification_uri_complete":"x","expires_in":900}`
	tokens := []deviceTokenResp{
		{http.StatusForbidden, `{"error":"authorization_pending"}`},
		{http.StatusOK, `{"access_token":"AT","refresh_token":"RT","expires_in":3600}`},
	}
	_, _, cleanup := withFakeDeviceAuth0(t, deviceBody, tokens)
	defer cleanup()
	sleeps := withSleepRecorder(t)

	cfg := LoopbackConfig{Auth0Domain: "ignored.example", ClientID: "c", Audience: "a", Scopes: []string{"openid"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := LoginDevice(ctx, cfg); err != nil {
		t.Fatalf("LoginDevice: %v", err)
	}
	if len(*sleeps) < 1 || (*sleeps)[0] != 5*time.Second {
		t.Errorf("sleeps = %v, want first==5s default", *sleeps)
	}
}
