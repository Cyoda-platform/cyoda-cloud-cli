package commands

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/auth"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/keychain"
)

// stubAuth0Revoke installs a fake Auth0 server with a /oauth/revoke handler
// and points auth.AuthBaseURLForTest at it. The handler increments calls and
// records the form values it saw.
func stubAuth0Revoke(t *testing.T, status int) (calls *int32, lastForm *url.Values, cleanup func()) {
	t.Helper()
	var c int32
	var (
		captured url.Values
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&c, 1)
		_ = r.ParseForm()
		captured = r.PostForm
		w.WriteHeader(status)
	})
	srv := httptest.NewServer(mux)
	restore := auth.SetAuthBaseURLForTest(srv.URL)
	cleanup = func() {
		restore()
		srv.Close()
	}
	return &c, &captured, cleanup
}

func setupFileFallback(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CYODA_KEYCHAIN_FILE_FALLBACK", "1")
}

func TestLogout_HappyPath(t *testing.T) {
	setupFileFallback(t)

	if err := keychain.Store(keychain.Profile{
		Org:           "",
		RefreshToken:  "RT-abc",
		APIURL:        "https://api.cyoda.cloud",
		Auth0Domain:   "ignored.example",
		Auth0ClientID: "client-id",
		Auth0Audience: "https://api.cyoda.cloud",
	}); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}

	calls, form, cleanup := stubAuth0Revoke(t, http.StatusOK)
	defer cleanup()

	cmd := NewLogoutCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("revoke calls = %d, want 1", got)
	}
	if got := form.Get("token"); got != "RT-abc" {
		t.Errorf("token form value = %q, want RT-abc", got)
	}
	if got := form.Get("client_id"); got != "client-id" {
		t.Errorf("client_id form value = %q", got)
	}
	if got := form.Get("token_type_hint"); got != "refresh_token" {
		t.Errorf("token_type_hint = %q", got)
	}
	if _, err := keychain.Load(""); !errors.Is(err, keychain.ErrNotFound) {
		t.Errorf("keychain entry should be deleted, got err=%v", err)
	}
}

func TestLogout_NoProfileIsIdempotent(t *testing.T) {
	setupFileFallback(t)

	calls, _, cleanup := stubAuth0Revoke(t, http.StatusOK)
	defer cleanup()

	cmd := NewLogoutCmd()
	var stderr bytes.Buffer
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("revoke calls = %d, want 0 (no profile)", got)
	}
	if !strings.Contains(stderr.String(), "Not logged in") {
		t.Errorf("stderr = %q, want 'Not logged in'", stderr.String())
	}
}

func TestLogout_RevokeFailureStillDeletesKeychain(t *testing.T) {
	setupFileFallback(t)

	if err := keychain.Store(keychain.Profile{
		Org:           "acme",
		RefreshToken:  "RT-acme",
		Auth0Domain:   "ignored.example",
		Auth0ClientID: "client-id",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Auth0 returns 5xx; the command must warn but proceed.
	_, _, cleanup := stubAuth0Revoke(t, http.StatusInternalServerError)
	defer cleanup()

	cmd := NewLogoutCmd()
	var stderr bytes.Buffer
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--org", "acme"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(stderr.String(), "warning") && !strings.Contains(stderr.String(), "revoke") {
		t.Errorf("stderr should mention revoke warning, got %q", stderr.String())
	}
	if _, err := keychain.Load("acme"); !errors.Is(err, keychain.ErrNotFound) {
		t.Errorf("keychain entry should be deleted, got err=%v", err)
	}
}
