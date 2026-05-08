package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/auth"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/keychain"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// stubDiscoveryFile writes a static discovery JSON to <tempdir>/disco.json
// and sets CYODA_CLOUD_DISCOVERY_URL to its file:// URL.
func stubDiscoveryFile(t *testing.T, apiURL string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "disco.json")
	doc := map[string]string{
		"api_url":         apiURL,
		"auth0_domain":    "ignored.example",
		"auth0_client_id": "client-id",
		"auth0_audience":  "https://api.cyoda.cloud",
	}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatalf("write disco: %v", err)
	}
	t.Setenv("CYODA_CLOUD_DISCOVERY_URL", "file://"+p)
}

// stubAuth0Token installs a fake Auth0 server with a /oauth/token handler
// that returns the supplied access token. Intended for whoami tests where
// we want a one-shot refresh -> AT -> /v2/me round-trip.
func stubAuth0Token(t *testing.T, accessToken string) (calls *int32, cleanup func()) {
	t.Helper()
	var c int32
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&c, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  accessToken,
			"refresh_token": "RT-rotated",
			"expires_in":    3600,
			"scope":         "openid",
		})
	})
	srv := httptest.NewServer(mux)
	restore := auth.SetAuthBaseURLForTest(srv.URL)
	cleanup = func() {
		restore()
		srv.Close()
	}
	return &c, cleanup
}

func TestWhoami_NoProfileReturnsClearError(t *testing.T) {
	setupFileFallback(t)
	stubDiscoveryFile(t, "https://example.invalid")

	cmd := NewWhoamiCmd()
	var stderr, stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cyoda-cloud login") {
		t.Errorf("error should suggest login, got: %v", err)
	}
	// Spec §6.6: "not logged in" must surface as a CLIError with
	// CodeUnauthenticated so main.go's wrapper sets exit code 3.
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError, got %T: %v", err, err)
	}
	if cerr.Code != output.CodeUnauthenticated {
		t.Errorf("CLIError.Code = %d, want %d (Unauthenticated)",
			cerr.Code, output.CodeUnauthenticated)
	}
}

// TestWhoami_HappyPathJSON spins up a fake API server returning a Me payload
// plus a fake Auth0 /oauth/token endpoint, stores a profile, and asserts
// --output-json produces the expected JSON.
func TestWhoami_HappyPathJSON(t *testing.T) {
	setupFileFallback(t)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/me" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer AT-fresh" {
			t.Errorf("Authorization = %q, want Bearer AT-fresh", got)
		}
		if got := r.Header.Get("Cyoda-Cloud-CLI-Version"); got == "" {
			t.Errorf("missing Cyoda-Cloud-CLI-Version header")
		}
		if got := r.Header.Get("User-Agent"); !strings.Contains(got, "cyoda-cloud-cli/") {
			t.Errorf("User-Agent = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		me := api.Me{
			UserId:          "auth0|abc",
			OrgId:           "org_acme",
			Tier:            "free",
			Roles:           []string{"member"},
			Scopes:          []string{"read:builds"},
			IsCyodaEmployee: false,
			Features:        map[string]bool{"deploy_app": true},
		}
		me.Quota.EnvDeploys = api.QuotaCounter{Window: "month", Used: 1, Limit: 10}
		me.Quota.AppDeploys = api.QuotaCounter{Window: "month", Used: 0, Limit: 5}
		_ = json.NewEncoder(w).Encode(me)
	}))
	defer apiSrv.Close()

	stubDiscoveryFile(t, apiSrv.URL)
	tokCalls, cleanup := stubAuth0Token(t, "AT-fresh")
	defer cleanup()

	if err := keychain.Store(keychain.Profile{
		Org:           "",
		RefreshToken:  "RT0",
		APIURL:        apiSrv.URL,
		Auth0Domain:   "ignored.example",
		Auth0ClientID: "client-id",
		Auth0Audience: "https://api.cyoda.cloud",
	}); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}

	cmd := NewWhoamiCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--output-json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("whoami: %v\nstderr=%s", err, stderr.String())
	}

	if got := atomic.LoadInt32(tokCalls); got != 1 {
		t.Errorf("oauth/token calls = %d, want 1", got)
	}

	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode stdout: %v\nout=%s", err, stdout.String())
	}
	if got["user_id"] != "auth0|abc" {
		t.Errorf("user_id = %v", got["user_id"])
	}
	if got["org_id"] != "org_acme" {
		t.Errorf("org_id = %v", got["org_id"])
	}

	// Verify rotated RT was persisted.
	stored, err := keychain.Load("")
	if err != nil {
		t.Fatalf("keychain.Load: %v", err)
	}
	if stored.RefreshToken != "RT-rotated" {
		t.Errorf("refresh token after whoami = %q, want RT-rotated", stored.RefreshToken)
	}
}

// TestWhoami_RefreshTokenExpiredMapsExitThree is the regression test for the
// final-review Critical #1 finding: when the stored RT is rejected by Auth0
// with invalid_grant, the refresh path returns auth.ErrSessionExpired. That
// error bubbles up from api.Transport.do as a transport-level error — the
// API request never completes, so callers never see an HTTP 401. Before the
// mapTransportError fix, the error was wrapped by fmt.Errorf("whoami: %w",
// err) and output.Exit fell through to CodeGeneric (1). Spec §6.6 mandates
// exit 3 (CodeUnauthenticated) so shell wrappers can branch on it to trigger
// `cyoda-cloud login`. This test asserts the *output.CLIError surfaces with
// the right code AND that output.Exit returns 3.
func TestWhoami_RefreshTokenExpiredMapsExitThree(t *testing.T) {
	setupFileFallback(t)

	// API server: any /v2/me hit would mean the refresh succeeded — the
	// scenario we're testing has the request fail BEFORE reaching the API.
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("API server must not be hit when refresh fails: %s %s", r.Method, r.URL.Path)
		http.Error(w, "should not be reached", http.StatusInternalServerError)
	}))
	defer apiSrv.Close()
	stubDiscoveryFile(t, apiSrv.URL)

	// Auth0 /oauth/token returns 400 invalid_grant — the canonical RT-expired
	// shape per Auth0 docs. The Refresh code recognises this and wraps with
	// auth.ErrSessionExpired.
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "Unknown or invalid refresh token",
		})
	})
	auth0 := httptest.NewServer(mux)
	defer auth0.Close()
	restoreAuth0 := auth.SetAuthBaseURLForTest(auth0.URL)
	defer restoreAuth0()

	if err := keychain.Store(keychain.Profile{
		Org:           "",
		RefreshToken:  "RT-stale",
		APIURL:        apiSrv.URL,
		Auth0Domain:   "ignored.example",
		Auth0ClientID: "client-id",
		Auth0Audience: "https://api.cyoda.cloud",
	}); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}

	cmd := NewWhoamiCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected refresh failure to produce error, got nil")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError (got %T): %v", err, err)
	}
	if cerr.Code != output.CodeUnauthenticated {
		t.Errorf("CLIError.Code = %d, want %d (Unauthenticated)",
			cerr.Code, output.CodeUnauthenticated)
	}
	if got := output.Exit(err); got != 3 {
		t.Errorf("output.Exit = %d, want 3 (CodeUnauthenticated)", got)
	}
	if !strings.Contains(err.Error(), "session expired") {
		t.Errorf("err message missing session-expired prompt: %v", err)
	}
}

// TestWhoami_Non2xxMapsToCLIError is the regression for CR Important #1:
// a 5xx from /v2/me must map through problemToError to a *output.CLIError
// with CodeUpstreamFailure (exit 9), not a bare fmt.Errorf that falls
// through to the generic exit 1.
func TestWhoami_Non2xxMapsToCLIError(t *testing.T) {
	setupFileFallback(t)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/me" {
			http.NotFound(w, r)
			return
		}
		// 503 Service Unavailable — manager is down. /v2/me does not
		// declare a Problem body in OpenAPI, so the response has no
		// problem+json; whoami must still map to CodeUpstreamFailure
		// purely from the status.
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer apiSrv.Close()

	stubDiscoveryFile(t, apiSrv.URL)
	_, cleanup := stubAuth0Token(t, "AT-fresh")
	defer cleanup()

	if err := keychain.Store(keychain.Profile{
		Org:           "",
		RefreshToken:  "RT0",
		APIURL:        apiSrv.URL,
		Auth0Domain:   "ignored.example",
		Auth0ClientID: "client-id",
		Auth0Audience: "https://api.cyoda.cloud",
	}); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}

	cmd := NewWhoamiCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected 503 to produce error, got nil")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError (got %T): %v", err, err)
	}
	if cerr.Code != output.CodeUpstreamFailure {
		t.Errorf("CLIError.Code = %d, want %d (UpstreamFailure)",
			cerr.Code, output.CodeUpstreamFailure)
	}
	if got := output.Exit(err); got != 9 {
		t.Errorf("output.Exit = %d, want 9 (CodeUpstreamFailure)", got)
	}
}

// TestWhoami_426UpgradeRequiredSurfacesDetail is the end-to-end regression
// for the body-decode fallback. The manager returns 426 Upgrade Required
// with a problem+json body naming the required minimum CLI version. Before
// the fix, /v2/me's response had no typed *api.Problem field for 426 (the
// OpenAPI spec doesn't declare a `default:` error response), so the user
// only saw "unexpected status 426" — actionable detail was dropped on the
// floor. After the fix, the body-decode fallback in problemToError pulls
// the Problem out of the raw body when the typed field is nil, surfaces
// the detail, and still maps to exit 10 via codeForStatus.
func TestWhoami_426UpgradeRequiredSurfacesDetail(t *testing.T) {
	setupFileFallback(t)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/me" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"type":"https://docs.cyoda.cloud/errors/server-min-version-required","title":"upgrade-required","status":426,"detail":"CLI version 0.0.0 is below required minimum 0.4.0; please upgrade"}`))
	}))
	defer apiSrv.Close()

	stubDiscoveryFile(t, apiSrv.URL)
	_, cleanup := stubAuth0Token(t, "AT-fresh")
	defer cleanup()

	if err := keychain.Store(keychain.Profile{
		Org:           "",
		RefreshToken:  "RT0",
		APIURL:        apiSrv.URL,
		Auth0Domain:   "ignored.example",
		Auth0ClientID: "client-id",
		Auth0Audience: "https://api.cyoda.cloud",
	}); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}

	cmd := NewWhoamiCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected 426 to produce error, got nil")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError (got %T): %v", err, err)
	}
	if cerr.Code != output.CodeServerMinVersionRequired {
		t.Errorf("CLIError.Code = %d, want %d (ServerMinVersionRequired)",
			cerr.Code, output.CodeServerMinVersionRequired)
	}
	if got := output.Exit(err); got != 10 {
		t.Errorf("output.Exit = %d, want 10 (CodeServerMinVersionRequired)", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "0.4.0") {
		t.Errorf("err message missing required-min version: %v", err)
	}
	if !strings.Contains(msg, "please upgrade") {
		t.Errorf("err message missing detail: %v", err)
	}
}

// TestWhoami_DefaultOrgFromConfig verifies that with default_org="abc" in
// config.toml and no --org flag, the resolved org reaches BuildAPIClient
// (and therefore keychain.Load). We seed the keychain only under "abc" — if
// the resolution fell back to "" the command would fail with not-logged-in.
func TestWhoami_DefaultOrgFromConfig(t *testing.T) {
	setupFileFallback(t)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/me" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		me := api.Me{UserId: "auth0|abc", OrgId: "org_abc", Tier: "free"}
		_ = json.NewEncoder(w).Encode(me)
	}))
	defer apiSrv.Close()

	stubDiscoveryFile(t, apiSrv.URL)
	_, cleanup := stubAuth0Token(t, "AT-fresh")
	defer cleanup()

	// Seed only the "abc" profile — the "" profile is intentionally absent so
	// a default-org=="" fallback would fail with ErrNotFound.
	if err := keychain.Store(keychain.Profile{
		Org:           "abc",
		RefreshToken:  "RT0",
		APIURL:        apiSrv.URL,
		Auth0Domain:   "ignored.example",
		Auth0ClientID: "client-id",
		Auth0Audience: "https://api.cyoda.cloud",
	}); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}

	// Persist default_org=abc in config.toml. setupFileFallback already
	// scopes XDG_CONFIG_HOME to a tempdir.
	if err := config.SaveFile(config.File{DefaultOrg: "abc"}); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}

	cmd := NewWhoamiCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--output-json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("whoami: %v\nstderr=%s", err, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode stdout: %v\nout=%s", err, stdout.String())
	}
	if got["org_id"] != "org_abc" {
		t.Errorf("org_id = %v, want org_abc", got["org_id"])
	}
}

// TestWhoami_OutputFormatJSONFromConfig verifies that with output_format=json
// in config.toml and no --output-json flag, whoami emits JSON to stdout.
// stdoutIsTerminal is forced true so table would be the natural path.
func TestWhoami_OutputFormatJSONFromConfig(t *testing.T) {
	setupFileFallback(t)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/me" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		me := api.Me{UserId: "auth0|abc", OrgId: "org_acme", Tier: "free"}
		_ = json.NewEncoder(w).Encode(me)
	}))
	defer apiSrv.Close()

	stubDiscoveryFile(t, apiSrv.URL)
	_, cleanup := stubAuth0Token(t, "AT-fresh")
	defer cleanup()

	if err := keychain.Store(keychain.Profile{
		Org:           "",
		RefreshToken:  "RT0",
		APIURL:        apiSrv.URL,
		Auth0Domain:   "ignored.example",
		Auth0ClientID: "client-id",
		Auth0Audience: "https://api.cyoda.cloud",
	}); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}
	if err := config.SaveFile(config.File{OutputFormat: "json"}); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}

	// Pretend stdout is a TTY so the JSON path is only taken because of the
	// config override, not because of the non-TTY auto-JSON fallback.
	prev := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdoutIsTerminal = prev })

	cmd := NewWhoamiCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{}) // no --output-json
	if err := cmd.Execute(); err != nil {
		t.Fatalf("whoami: %v\nstderr=%s", err, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("expected JSON on stdout, decode failed: %v\nout=%s",
			err, stdout.String())
	}
	if got["user_id"] != "auth0|abc" {
		t.Errorf("user_id = %v", got["user_id"])
	}
}
