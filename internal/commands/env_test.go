package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/keychain"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// envTestSetup wires the file-fallback keychain, a fake Auth0 token endpoint,
// and a custom API handler. Returns the API server URL so callers can assert
// against it. Closes everything via t.Cleanup.
func envTestSetup(t *testing.T, handler http.HandlerFunc) (apiURL string) {
	t.Helper()
	setupFileFallback(t)

	apiSrv := httptest.NewServer(handler)
	t.Cleanup(apiSrv.Close)

	stubDiscoveryFile(t, apiSrv.URL)

	_, cleanup := stubAuth0Token(t, "AT-fresh")
	t.Cleanup(cleanup)

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
	return apiSrv.URL
}

// runCmd executes cmd with args and returns stdout/stderr/err. ctx defaults to
// background. Tests that need a deterministic wait clock build their own
// context.
func runCmd(t *testing.T, cmd interface {
	SetOut(io.Writer)
	SetErr(io.Writer)
	SetArgs([]string)
	ExecuteContext(context.Context) error
}, ctx context.Context, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var so, se bytes.Buffer
	cmd.SetOut(&so)
	cmd.SetErr(&se)
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(ctx)
	return so.String(), se.String(), err
}

// ---- env up ----

func TestEnvUp_PostsBackendAndIdempotencyKey(t *testing.T) {
	var captured struct {
		method string
		path   string
		idem   string
		body   map[string]any
	}
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.idem = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&captured.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"env_id":    "env_abc",
			"namespace": "ns_org_acme",
			"state":     "PROCESSING",
		})
	}))

	cmd := NewEnvCmd()
	stdout, _, err := runCmd(t, cmd, context.Background(),
		"up", "--backend", "cassandra-basic", "--output-json")
	if err != nil {
		t.Fatalf("env up: %v", err)
	}
	if captured.method != http.MethodPost || captured.path != "/v2/env" {
		t.Errorf("request = %s %s, want POST /v2/env", captured.method, captured.path)
	}
	if len(captured.idem) < minIdempotencyKeyLen {
		t.Errorf("Idempotency-Key = %q (len=%d), want >= %d chars",
			captured.idem, len(captured.idem), minIdempotencyKeyLen)
	}
	if captured.body["backend"] != "cassandra-basic" {
		t.Errorf("body.backend = %v", captured.body["backend"])
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode stdout: %v\nout=%s", err, stdout)
	}
	if got["env_id"] != "env_abc" {
		t.Errorf("env_id = %v", got["env_id"])
	}
	if got["state"] != "PROCESSING" {
		t.Errorf("state = %v", got["state"])
	}
}

func TestEnvUp_RequiresBackend(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server must not be called when --backend is missing, got %s %s", r.Method, r.URL.Path)
	}))
	cmd := NewEnvCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "up")
	if err == nil || !strings.Contains(err.Error(), "--backend is required") {
		t.Fatalf("err = %v, want --backend required", err)
	}
	// Spec §6.6: bad usage maps to exit code 2 via CLIError — parity with
	// app's "--repo is required" surface.
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError, got %T: %v", err, err)
	}
	if cerr.Code != output.CodeBadUsage {
		t.Errorf("CLIError.Code = %d, want %d (BadUsage)", cerr.Code, output.CodeBadUsage)
	}
	if got := output.Exit(err); got != 2 {
		t.Errorf("Exit = %d, want 2", got)
	}
}

// TestEnvUp_TierNotEntitledMapsExitFive covers the 403 path on POST /v2/env.
// Spec §6.6: a Problem with type slug `tier-not-entitled` must exit 5 — the
// pre-Task-7 env code surfaced this as plain fmt.Errorf and exited 1, which
// broke parity with the app subtree.
func TestEnvUp_TierNotEntitledMapsExitFive(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(problemBody(
			"tier-not-entitled",
			"Subscription tier does not allow env up",
			403,
		))
	}))
	cmd := NewEnvCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "up", "--backend", "x")
	if err == nil {
		t.Fatal("expected tier-not-entitled error, got nil")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %T %v, want *output.CLIError", err, err)
	}
	if cerr.Code != output.CodeTierNotEntitled {
		t.Errorf("Code = %d, want %d (TierNotEntitled)", cerr.Code, output.CodeTierNotEntitled)
	}
	if got := output.Exit(err); got != 5 {
		t.Errorf("Exit = %d, want 5", got)
	}
	if !strings.Contains(cerr.Error(), "Subscription tier") {
		t.Errorf("error message = %q", cerr.Error())
	}
}

func TestEnvUp_RejectsShortIdempotencyKey(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server must not be called for short key, got %s %s", r.Method, r.URL.Path)
	}))
	cmd := NewEnvCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(),
		"up", "--backend", "x", "--idempotency-key", "short")
	if err == nil || !strings.Contains(err.Error(), "at least 16") {
		t.Fatalf("err = %v, want minimum-length error", err)
	}
	// Spec §6.6: bad usage maps to exit code 2 via CLIError.
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError, got %T: %v", err, err)
	}
	if cerr.Code != output.CodeBadUsage {
		t.Errorf("CLIError.Code = %d, want %d (BadUsage)", cerr.Code, output.CodeBadUsage)
	}
}

func TestEnvUp_HonoursUserSuppliedIdempotencyKey(t *testing.T) {
	const userKey = "user-supplied-idem-key-very-long"
	var seen string
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"env_id": "env_x", "namespace": "ns", "state": "PROCESSING",
		})
	}))
	cmd := NewEnvCmd()
	if _, _, err := runCmd(t, cmd, context.Background(),
		"up", "--backend", "x", "--output-json", "--idempotency-key", userKey); err != nil {
		t.Fatalf("env up: %v", err)
	}
	if seen != userKey {
		t.Errorf("Idempotency-Key = %q, want %q", seen, userKey)
	}
}

func TestEnvUp_Wait_PollsUntilTerminal(t *testing.T) {
	var calls int32
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/env":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"env_id": "env_w", "namespace": "ns_w", "state": "PROCESSING",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/env":
			n := atomic.AddInt32(&calls, 1)
			state := "PROCESSING"
			if n >= 3 {
				state = "SUCCESS"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"env_id": "env_w", "namespace": "ns_w", "state": state,
				"job_status": "RUNNING",
			})
		default:
			http.NotFound(w, r)
		}
	}))

	// Speed up the wait loop to milliseconds for the test.
	cmd := NewEnvCmd()
	stdout, stderr, err := runWithFastWait(t, cmd, "up", "--backend", "x", "--wait", "--output-json")
	if err != nil {
		t.Fatalf("env up --wait: %v\nstderr=%s", err, stderr)
	}
	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Errorf("polls = %d, want >= 3", got)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode stdout: %v\nout=%s", err, stdout)
	}
	if got["state"] != "SUCCESS" {
		t.Errorf("final state = %v, want SUCCESS\nstderr=%s", got["state"], stderr)
	}
	// Status messages should appear on stderr.
	if !strings.Contains(stderr, "still PROCESSING") {
		t.Errorf("stderr missing wait status:\n%s", stderr)
	}
}

// TestEnvUp_SessionExpiredMapsToCLIError covers the 401 path on POST /v2/env.
// Spec §6.6 mandates exit code 3 (unauthenticated); the legacy plain
// errors.New(...) bubbled to exit code 1. The fix routes 401 through
// errSessionExpired() so errors.As recovers a *output.CLIError carrying
// CodeUnauthenticated.
func TestEnvUp_SessionExpiredMapsToCLIError(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":   "about:blank",
			"title":  "session expired",
			"status": 401,
		})
	}))
	cmd := NewEnvCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "up", "--backend", "x")
	if err == nil {
		t.Fatal("env up (401): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "session expired") {
		t.Errorf("err = %v, want session-expired message", err)
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError, got %T: %v", err, err)
	}
	if cerr.Code != output.CodeUnauthenticated {
		t.Errorf("CLIError.Code = %d, want %d (Unauthenticated)", cerr.Code, output.CodeUnauthenticated)
	}
}

// ---- env status ----

func TestEnvStatus_HappyPath(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/env" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		envID, ns, state := "env_x", "ns_x", "READY"
		_ = json.NewEncoder(w).Encode(map[string]any{
			"env_id":    envID,
			"namespace": ns,
			"state":     state,
		})
	}))

	cmd := NewEnvCmd()
	stdout, _, err := runCmd(t, cmd, context.Background(),
		"status", "--output-json")
	if err != nil {
		t.Fatalf("env status: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	if got["state"] != "READY" {
		t.Errorf("state = %v", got["state"])
	}
}

// TestEnvStatus_NotFoundMapsExitSeven covers the 404 path on GET /v2/env.
// Spec §6.6 maps not-found to exit code 7. The CLI keeps the informational
// stderr line for shell-friendliness but returns a *output.CLIError carrying
// CodeNotFound so main.go's wrapper sets the documented exit code.
func TestEnvStatus_NotFoundMapsExitSeven(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":   "about:blank",
			"title":  "no env",
			"status": 404,
		})
	}))
	cmd := NewEnvCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	stdout, stderr, err := runCmd(t, cmd, context.Background(), "status")
	if err == nil {
		t.Fatal("env status (404): expected CLIError, got nil")
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on 404, got %q", stdout)
	}
	if !strings.Contains(stderr, "No environment provisioned") {
		t.Errorf("stderr missing informational message:\n%s", stderr)
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %T %v, want *output.CLIError", err, err)
	}
	if cerr.Code != output.CodeNotFound {
		t.Errorf("Code = %d, want %d (NotFound)", cerr.Code, output.CodeNotFound)
	}
	if got := output.Exit(err); got != 7 {
		t.Errorf("Exit = %d, want 7", got)
	}
}

// ---- env cancel ----

func TestEnvCancel_PostsCancelEndpoint(t *testing.T) {
	var captured struct {
		method string
		path   string
	}
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.method = r.Method
		captured.path = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	cmd := NewEnvCmd()
	_, stderr, err := runCmd(t, cmd, context.Background(), "cancel")
	if err != nil {
		t.Fatalf("env cancel: %v", err)
	}
	if captured.method != http.MethodPost || captured.path != "/v2/env:cancel" {
		t.Errorf("request = %s %s, want POST /v2/env:cancel", captured.method, captured.path)
	}
	if !strings.Contains(stderr, "queued") {
		t.Errorf("stderr missing queued message:\n%s", stderr)
	}
}

func TestEnvCancel_ConflictMapsToCLIError(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":   "about:blank",
			"title":  "not cancellable",
			"status": 409,
		})
	}))
	cmd := NewEnvCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "cancel")
	if err == nil || !strings.Contains(err.Error(), "not cancellable") {
		t.Fatalf("err = %v, want conflict surfaced", err)
	}
	// Status-only fallback: about:blank type → codeForStatus(409) = CodeConflict.
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %T %v, want *output.CLIError", err, err)
	}
	if cerr.Code != output.CodeConflict {
		t.Errorf("Code = %d, want %d (Conflict)", cerr.Code, output.CodeConflict)
	}
	if got := output.Exit(err); got != 8 {
		t.Errorf("Exit = %d, want 8", got)
	}
}

// ---- env down ----

func TestEnvDown_DeletesEndpoint(t *testing.T) {
	var captured struct {
		method string
		path   string
	}
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.method = r.Method
		captured.path = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	cmd := NewEnvCmd()
	_, stderr, err := runCmd(t, cmd, context.Background(), "down")
	if err != nil {
		t.Fatalf("env down: %v", err)
	}
	if captured.method != http.MethodDelete || captured.path != "/v2/env" {
		t.Errorf("request = %s %s, want DELETE /v2/env", captured.method, captured.path)
	}
	if !strings.Contains(stderr, "queued") {
		t.Errorf("stderr missing queued message:\n%s", stderr)
	}
}

func TestEnvDown_ConflictMapsToCLIError(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":   "about:blank",
			"title":  "app still deployed",
			"status": 409,
		})
	}))
	cmd := NewEnvCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "down")
	if err == nil || !strings.Contains(err.Error(), "app still deployed") {
		t.Fatalf("err = %v, want app-still-deployed surfaced", err)
	}
	// Spec §6.6: 409 → CodeConflict (exit 8). Pre-Task-7 env down returned a
	// plain fmt.Errorf and the process exited 1 — parity gap with app delete.
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %T %v, want *output.CLIError", err, err)
	}
	if cerr.Code != output.CodeConflict {
		t.Errorf("Code = %d, want %d (Conflict)", cerr.Code, output.CodeConflict)
	}
	if got := output.Exit(err); got != 8 {
		t.Errorf("Exit = %d, want 8", got)
	}
}

// TestEnvUp_WaitShortCircuitsOnTerminalInitial covers the idempotent-replay
// path: POST /v2/env returns 200 with state=SUCCESS already, so --wait must
// skip the poll loop entirely (no GET, no "still …" status lines).
func TestEnvUp_WaitShortCircuitsOnTerminalInitial(t *testing.T) {
	var getCalls int32
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/env":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK) // 200 = idempotent replay
			_ = json.NewEncoder(w).Encode(map[string]string{
				"env_id": "env_done", "namespace": "ns_done", "state": "SUCCESS",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/env":
			atomic.AddInt32(&getCalls, 1)
			http.Error(w, "should not be called", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))

	cmd := NewEnvCmd()
	stdout, stderr, err := runWithFastWait(t, cmd, "up", "--backend", "x", "--wait", "--output-json")
	if err != nil {
		t.Fatalf("env up --wait (terminal initial): %v\nstderr=%s", err, stderr)
	}
	if n := atomic.LoadInt32(&getCalls); n != 0 {
		t.Errorf("GET /v2/env calls = %d, want 0 (short-circuit)", n)
	}
	if strings.Contains(stderr, "still ") {
		t.Errorf("stderr should not contain wait status lines:\n%s", stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode stdout: %v\nout=%s", err, stdout)
	}
	if got["state"] != "SUCCESS" {
		t.Errorf("state = %v, want SUCCESS", got["state"])
	}
}

func TestEnvDown_Wait_PollsUntilGone(t *testing.T) {
	var calls int32
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/env":
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/env":
			n := atomic.AddInt32(&calls, 1)
			if n >= 2 {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"type": "about:blank", "title": "gone", "status": 404,
				})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"env_id": "env_w", "namespace": "ns_w", "state": "DELETING",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	cmd := NewEnvCmd()
	_, stderr, err := runWithFastWait(t, cmd, "down", "--wait")
	if err != nil {
		t.Fatalf("env down --wait: %v\nstderr=%s", err, stderr)
	}
	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Errorf("polls = %d, want >= 2", got)
	}
	if !strings.Contains(stderr, "torn down") {
		t.Errorf("stderr missing torn-down notice:\n%s", stderr)
	}
}

// TestEnvDown_Wait_TerminalStateBefore404 covers the terminal-state exit
// path of waitForEnvTeardown: the server reports a terminal state (e.g.
// CANCELLED — one of the spec §4.3 vocabulary) on the first poll. The loop
// must exit immediately, the user-facing message must still be "env torn
// down.", and --output-json must emit {"status":"torn_down"} on stdout.
func TestEnvDown_Wait_TerminalStateBefore404(t *testing.T) {
	var getCalls int32
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/env":
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/env":
			n := atomic.AddInt32(&getCalls, 1)
			if n == 1 {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"env_id": "env_w", "namespace": "ns_w", "state": "CANCELLED",
				})
				return
			}
			// Any subsequent poll fails the test — the terminal state on the
			// first poll must short-circuit the loop.
			t.Errorf("unexpected extra poll #%d after terminal state", n)
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "about:blank", "title": "gone", "status": 404,
			})
		default:
			http.NotFound(w, r)
		}
	}))

	cmd := NewEnvCmd()
	stdout, stderr, err := runWithFastWait(t, cmd, "down", "--wait", "--output-json")
	if err != nil {
		t.Fatalf("env down --wait: %v\nstderr=%s", err, stderr)
	}
	if n := atomic.LoadInt32(&getCalls); n != 1 {
		t.Errorf("GET polls = %d, want exactly 1 (terminal on first)", n)
	}
	if !strings.Contains(stderr, "env torn down.") {
		t.Errorf("stderr missing torn-down notice:\n%s", stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode stdout: %v\nout=%s", err, stdout)
	}
	if got["status"] != "torn_down" {
		t.Errorf("stdout status = %v, want torn_down", got["status"])
	}
}

// ---- helpers ----

// runWithFastWait sets up nowFunc/sleepFunc seams via WaitOpts so the wait
// loop runs in milliseconds. We cannot inject WaitOpts directly through the
// cobra flag plumbing, so we override stdoutIsTerminal to false (forces JSON
// output regardless) and shrink the wait clock by patching the seam.
func runWithFastWait(t *testing.T, cmd interface {
	SetOut(io.Writer)
	SetErr(io.Writer)
	SetArgs([]string)
	ExecuteContext(context.Context) error
}, args ...string) (string, string, error) {
	t.Helper()
	// Force non-TTY so commands that don't set --output-json still produce
	// JSON when the table would otherwise depend on stdout.IsTerminal.
	prevIsTerm := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdoutIsTerminal = prevIsTerm })

	// Shrink the wait constants for tests via the package-level seam.
	prevDefault := defaultWaitOpts
	defaultWaitOpts = func() output.WaitOpts {
		return output.WaitOpts{
			Initial: 1 * time.Millisecond,
			Max:     2 * time.Millisecond,
			Total:   500 * time.Millisecond,
		}
	}
	t.Cleanup(func() { defaultWaitOpts = prevDefault })

	var so, se bytes.Buffer
	cmd.SetOut(&so)
	cmd.SetErr(&se)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return so.String(), se.String(), err
}
