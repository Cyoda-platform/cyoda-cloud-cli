package commands

import (
	"bytes"
	"context"
	"encoding/json"
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
	if got["EnvId"] != "env_abc" {
		t.Errorf("EnvId = %v", got["EnvId"])
	}
	if got["State"] != "PROCESSING" {
		t.Errorf("State = %v", got["State"])
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
	if got["State"] != "SUCCESS" {
		t.Errorf("final state = %v, want SUCCESS\nstderr=%s", got["State"], stderr)
	}
	// Status messages should appear on stderr.
	if !strings.Contains(stderr, "still PROCESSING") {
		t.Errorf("stderr missing wait status:\n%s", stderr)
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
	if got["State"] != "READY" {
		t.Errorf("State = %v", got["State"])
	}
}

func TestEnvStatus_NotFoundIsInformational(t *testing.T) {
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
	stdout, stderr, err := runCmd(t, cmd, context.Background(), "status")
	if err != nil {
		t.Fatalf("env status (404): %v", err)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on 404, got %q", stdout)
	}
	if !strings.Contains(stderr, "No environment provisioned") {
		t.Errorf("stderr missing informational message:\n%s", stderr)
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

func TestEnvCancel_ConflictMaps(t *testing.T) {
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

func TestEnvDown_409StillDeployed(t *testing.T) {
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
