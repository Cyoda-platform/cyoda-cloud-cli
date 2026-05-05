package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func withFakeRefreshAuth0(t *testing.T, status int, body string) (*httptest.Server, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if got := r.PostFormValue("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := r.PostFormValue("refresh_token"); got == "" {
			t.Errorf("refresh_token form value missing")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
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
	return srv, cleanup
}

func TestRefresh_HappyPath_NewRT(t *testing.T) {
	_, cleanup := withFakeRefreshAuth0(t, http.StatusOK, `{
		"access_token":"AT2",
		"refresh_token":"RT2",
		"id_token":"IT2",
		"expires_in":3600,
		"scope":"openid profile"
	}`)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	toks, err := Refresh(ctx, RefreshConfig{
		Auth0Domain:  "ignored.example",
		ClientID:     "client",
		RefreshToken: "RT1",
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if toks.AccessToken != "AT2" || toks.RefreshToken != "RT2" {
		t.Errorf("tokens = %+v", toks)
	}
	if toks.ExpiresAt.Before(time.Now().Add(50 * time.Minute)) {
		t.Errorf("ExpiresAt too soon: %v", toks.ExpiresAt)
	}
}

func TestRefresh_KeepsOldRTWhenNoNew(t *testing.T) {
	_, cleanup := withFakeRefreshAuth0(t, http.StatusOK, `{
		"access_token":"AT2",
		"expires_in":3600
	}`)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	toks, err := Refresh(ctx, RefreshConfig{
		Auth0Domain:  "ignored.example",
		ClientID:     "client",
		RefreshToken: "RT1",
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if toks.AccessToken != "AT2" {
		t.Errorf("AT = %q", toks.AccessToken)
	}
	if toks.RefreshToken != "RT1" {
		t.Errorf("RT = %q, want unchanged RT1", toks.RefreshToken)
	}
}

func TestRefresh_InvalidGrantSurfacesSessionExpired(t *testing.T) {
	_, cleanup := withFakeRefreshAuth0(t, http.StatusForbidden, `{"error":"invalid_grant","error_description":"Unknown or invalid refresh token"}`)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Refresh(ctx, RefreshConfig{
		Auth0Domain:  "ignored.example",
		ClientID:     "client",
		RefreshToken: "RT1",
	})
	if !errors.Is(err, ErrSessionExpired) {
		t.Errorf("err = %v, want ErrSessionExpired", err)
	}
}

func TestRefresh_5xxWrapped(t *testing.T) {
	_, cleanup := withFakeRefreshAuth0(t, http.StatusBadGateway, `oops`)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Refresh(ctx, RefreshConfig{
		Auth0Domain:  "ignored.example",
		ClientID:     "client",
		RefreshToken: "RT1",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrSessionExpired) {
		t.Errorf("5xx must NOT map to ErrSessionExpired: %v", err)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("expected wrapped status code, got %v", err)
	}
}
