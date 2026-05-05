package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// withFakeAuth0 stands up an httptest server that emulates Auth0's /authorize
// (302 to the redirect_uri with a code+state) and /oauth/token (returning the
// supplied JSON body and status). It overrides the package-private
// authBaseURL to point at the test server and returns a cleanup that restores
// it.
func withFakeAuth0(t *testing.T, tokenStatus int, tokenBody string) (server *httptest.Server, cleanup func()) {
	t.Helper()
	// capturedRedirectURI records the redirect_uri that /authorize received.
	// /oauth/token then asserts that the form body posted by the client
	// carries the same value. Auth0 enforces this equality and a regression
	// would otherwise be silent against the permissive stub.
	var (
		mu                  sync.Mutex
		capturedRedirectURI string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		redirect := q.Get("redirect_uri")
		state := q.Get("state")
		// Mandatory PKCE params present?
		if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "missing pkce params", http.StatusBadRequest)
			return
		}
		u, err := url.Parse(redirect)
		if err != nil {
			http.Error(w, "bad redirect", http.StatusBadRequest)
			return
		}
		mu.Lock()
		capturedRedirectURI = redirect
		mu.Unlock()
		rq := u.Query()
		rq.Set("code", "AUTHCODE")
		rq.Set("state", state)
		u.RawQuery = rq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		expected := capturedRedirectURI
		mu.Unlock()
		if got := r.PostFormValue("redirect_uri"); got != expected {
			t.Helper()
			t.Errorf("/oauth/token redirect_uri = %q, want %q", got, expected)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(tokenStatus)
		_, _ = w.Write([]byte(tokenBody))
	})
	srv := httptest.NewServer(mux)

	authBaseURLMu.Lock()
	prev := authBaseURL
	authBaseURL = func(_ string) string { return srv.URL }
	cleanup = func() {
		authBaseURL = prev
		authBaseURLMu.Unlock()
		srv.Close()
	}
	return srv, cleanup
}

// browserGet returns an OpenBrowser implementation that performs an HTTP GET
// on the auth URL (which 302s to the loopback callback with code+state).
// Failures are surfaced via the returned error channel.
func browserGet(t *testing.T) (open func(string) error, getErr chan error) {
	t.Helper()
	getErr = make(chan error, 1)
	open = func(u string) error {
		// Use a client that follows redirects; httptest's redirect lands on
		// 127.0.0.1:<random>/callback which the loopback server serves.
		go func() {
			// Brief delay so the listener is up; the LoginPKCE goroutine
			// starts srv.Serve before calling open(), but be defensive.
			resp, err := http.Get(u)
			if err != nil {
				getErr <- err
				return
			}
			_ = resp.Body.Close()
			close(getErr)
		}()
		return nil
	}
	return
}

func TestLoginPKCE_HappyPath(t *testing.T) {
	_, cleanup := withFakeAuth0(t, http.StatusOK, `{
		"access_token":"AT",
		"refresh_token":"RT",
		"id_token":"IT",
		"expires_in":3600,
		"scope":"openid profile"
	}`)
	defer cleanup()

	open, getErr := browserGet(t)
	cfg := LoopbackConfig{
		Auth0Domain: "ignored.example", // overridden by authBaseURL
		ClientID:    "client",
		Audience:    "https://api.cyoda.cloud",
		Scopes:      []string{"openid", "profile"},
		OpenBrowser: open,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	toks, err := LoginPKCE(ctx, cfg)
	if err != nil {
		t.Fatalf("LoginPKCE: %v", err)
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
	select {
	case err := <-getErr:
		if err != nil {
			t.Errorf("simulated browser GET: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Errorf("simulated browser GET did not complete")
	}
}

func TestLoginPKCE_StateMismatch(t *testing.T) {
	// Custom Auth0 emulator: tampers with the state on the redirect.
	authBaseURLMu.Lock()
	prev := authBaseURL
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		redirect := r.URL.Query().Get("redirect_uri")
		u, _ := url.Parse(redirect)
		rq := u.Query()
		rq.Set("code", "AUTHCODE")
		rq.Set("state", "WRONG_STATE")
		u.RawQuery = rq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "should not be reached", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	authBaseURL = func(_ string) string { return srv.URL }
	defer func() {
		authBaseURL = prev
		authBaseURLMu.Unlock()
		srv.Close()
	}()

	open, _ := browserGet(t)
	cfg := LoopbackConfig{
		Auth0Domain: "ignored.example",
		ClientID:    "client",
		Audience:    "aud",
		Scopes:      []string{"openid"},
		OpenBrowser: open,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := LoginPKCE(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("expected state mismatch error, got %v", err)
	}
}

func TestLoginPKCE_TokenError(t *testing.T) {
	_, cleanup := withFakeAuth0(t, http.StatusBadRequest, `{"error":"invalid_grant","error_description":"bad code"}`)
	defer cleanup()

	open, _ := browserGet(t)
	cfg := LoopbackConfig{
		Auth0Domain: "ignored.example",
		ClientID:    "client",
		Audience:    "aud",
		Scopes:      []string{"openid"},
		OpenBrowser: open,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := LoginPKCE(ctx, cfg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "token exchange") || !strings.Contains(err.Error(), "400") {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("expected wrapped Auth0 body, got %v", err)
	}
}

func TestLoginPKCE_ContextCancel(t *testing.T) {
	// Auth0 emulator that never redirects, so we wait on the loopback.
	authBaseURLMu.Lock()
	prev := authBaseURL
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, _ *http.Request) {
		// Hang so the LoginPKCE goroutine never receives a callback.
		<-time.After(2 * time.Second)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	authBaseURL = func(_ string) string { return srv.URL }
	defer func() {
		authBaseURL = prev
		authBaseURLMu.Unlock()
		srv.Close()
	}()

	cfg := LoopbackConfig{
		Auth0Domain: "ignored.example",
		ClientID:    "c",
		Audience:    "a",
		Scopes:      []string{"openid"},
		// OpenBrowser intentionally a no-op so nothing hits /authorize.
		OpenBrowser: func(string) error { return nil },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := LoginPKCE(ctx, cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("LoginPKCE blocked too long: %v", d)
	}
}

func TestLoginPKCE_BrowserOpenFailureFallsBack(t *testing.T) {
	// /authorize is reachable but the browser-open hook returns an error.
	// We expect: the URL is printed to cfg.Stderr AND the flow continues
	// (here it will time out via ctx since no browser actually fires).
	authBaseURLMu.Lock()
	prev := authBaseURL
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		// Should never be reached; only the URL is printed.
		http.Error(w, "should not be hit", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	authBaseURL = func(_ string) string { return srv.URL }
	defer func() {
		authBaseURL = prev
		authBaseURLMu.Unlock()
		srv.Close()
	}()

	var stderr bytes.Buffer
	cfg := LoopbackConfig{
		Auth0Domain: "ignored.example",
		ClientID:    "c",
		Audience:    "a",
		Scopes:      []string{"openid"},
		OpenBrowser: func(_ string) error { return fmt.Errorf("no display") },
		Stderr:      &stderr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := LoginPKCE(ctx, cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
	out := stderr.String()
	if !strings.Contains(out, "Could not open browser automatically") {
		t.Errorf("expected fallback message in stderr, got %q", out)
	}
	if !strings.Contains(out, "/authorize") {
		t.Errorf("expected auth URL printed in stderr, got %q", out)
	}
}

