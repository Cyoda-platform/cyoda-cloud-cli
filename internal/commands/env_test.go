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

// runCmd executes cmd with args and returns stdout/stderr/err.
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

// envDetailJSON is the wire-shape of an EnvDetail response. Lets tests build
// realistic payloads without depending on the codegen's internal types.
func envDetailJSON(envName, namespace, state string) map[string]any {
	return map[string]any{
		"env_id":        "11111111-2222-3333-4444-555555555555",
		"env_name":      envName,
		"namespace":     namespace,
		"app_namespace": "cl-app-x-" + envName,
		"cyoda_env_url": "https://" + namespace + ".kube3.cyoda.org",
		"m2m_client_id": "m2m-id-" + envName,
		"state":         state,
		"creation_date": "2026-05-04T10:00:00Z",
	}
}

// ---- env up ----

func TestEnvUp_PostsBackendNameAndIdempotencyKey(t *testing.T) {
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
		_ = json.NewEncoder(w).Encode(envDetailJSON("dev", "cl-x-dev", "Queued"))
	}))

	cmd := NewEnvCmd()
	stdout, _, err := runCmd(t, cmd, context.Background(),
		"up", "dev", "--backend", "cassandra-basic", "--output-json")
	if err != nil {
		t.Fatalf("env up: %v", err)
	}
	if captured.method != http.MethodPost || captured.path != "/v2/envs" {
		t.Errorf("request = %s %s, want POST /v2/envs", captured.method, captured.path)
	}
	if len(captured.idem) < minIdempotencyKeyLen {
		t.Errorf("Idempotency-Key = %q (len=%d), want >= %d chars",
			captured.idem, len(captured.idem), minIdempotencyKeyLen)
	}
	if captured.body["env_name"] != "dev" {
		t.Errorf("body.env_name = %v", captured.body["env_name"])
	}
	if captured.body["backend"] != "cassandra-basic" {
		t.Errorf("body.backend = %v", captured.body["backend"])
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode stdout: %v\nout=%s", err, stdout)
	}
	if got["env_name"] != "dev" {
		t.Errorf("env_name = %v", got["env_name"])
	}
	if got["state"] != "Queued" {
		t.Errorf("state = %v", got["state"])
	}
}

func TestEnvUp_SendsM2MWithAdminRoleFlag(t *testing.T) {
	var bodyMap map[string]any
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&bodyMap)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(envDetailJSON("dev", "cl-x-dev", "Queued"))
	}))
	cmd := NewEnvCmd()
	if _, _, err := runCmd(t, cmd, context.Background(),
		"up", "dev", "--backend", "cassandra-basic",
		"--m2m-with-admin-role", "--output-json"); err != nil {
		t.Fatalf("env up: %v", err)
	}
	if v, _ := bodyMap["m2m_with_admin_role"].(bool); !v {
		t.Errorf("m2m_with_admin_role = %v, want true (body=%v)", bodyMap["m2m_with_admin_role"], bodyMap)
	}
}

func TestEnvUp_RequiresName(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server must not be called when <name> is missing, got %s %s", r.Method, r.URL.Path)
	}))
	cmd := NewEnvCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "up", "--backend", "x")
	if err == nil {
		t.Fatal("expected error for missing name, got nil")
	}
	// cobra's ExactArgs surfaces "accepts 1 arg(s), received 0" — that's fine.
	if !strings.Contains(err.Error(), "arg") {
		t.Errorf("err = %v, want missing-arg error", err)
	}
}

func TestEnvUp_InvalidNameRejectedClientSide(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server must not be called for client-rejected name, got %s %s", r.Method, r.URL.Path)
	}))
	cmd := NewEnvCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	// "default" is reserved.
	_, _, err := runCmd(t, cmd, context.Background(), "up", "default", "--backend", "x")
	if err == nil {
		t.Fatal("expected client-side rejection of reserved name, got nil")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %T %v, want *output.CLIError", err, err)
	}
	if cerr.Code != output.CodeBadUsage {
		t.Errorf("CLIError.Code = %d, want %d (BadUsage)", cerr.Code, output.CodeBadUsage)
	}
	if got := output.Exit(err); got != 2 {
		t.Errorf("Exit = %d, want 2", got)
	}
}

func TestEnvUp_RequiresBackend(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server must not be called when --backend is missing, got %s %s", r.Method, r.URL.Path)
	}))
	cmd := NewEnvCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "up", "dev")
	if err == nil || !strings.Contains(err.Error(), "--backend is required") {
		t.Fatalf("err = %v, want --backend required", err)
	}
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
	_, _, err := runCmd(t, cmd, context.Background(), "up", "dev", "--backend", "x")
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
}

func TestEnvUp_AlreadyExists409(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":     "https://docs.cyoda.cloud/errors/env-already-exists",
			"title":    "env already exists",
			"status":   409,
			"detail":   `env "dev" already exists for this org`,
			"env_id":   "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			"env_name": "dev",
		})
	}))
	cmd := NewEnvCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "up", "dev", "--backend", "x")
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
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
	if !strings.Contains(cerr.Error(), "already exists") {
		t.Errorf("error message = %q, want title surfaced", cerr.Error())
	}
	// Server returns the leader's env_id in the EnvAlreadyExistsProblem
	// extension. Surface it so the user can `env status <name>` against
	// the existing env or attach the id to a support ticket.
	if !strings.Contains(cerr.Error(), "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa") {
		t.Errorf("error message = %q, want env_id surfaced", cerr.Error())
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
		"up", "dev", "--backend", "x", "--idempotency-key", "short")
	if err == nil || !strings.Contains(err.Error(), "at least 16") {
		t.Fatalf("err = %v, want minimum-length error", err)
	}
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
		_ = json.NewEncoder(w).Encode(envDetailJSON("dev", "cl-x-dev", "Queued"))
	}))
	cmd := NewEnvCmd()
	if _, _, err := runCmd(t, cmd, context.Background(),
		"up", "dev", "--backend", "x", "--output-json", "--idempotency-key", userKey); err != nil {
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
		case r.Method == http.MethodPost && r.URL.Path == "/v2/envs":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(envDetailJSON("dev", "cl-x-dev", "Queued"))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/envs/dev":
			n := atomic.AddInt32(&calls, 1)
			state := "Job_Scheduled"
			if n >= 3 {
				state = "Ready"
			}
			_ = json.NewEncoder(w).Encode(envDetailJSON("dev", "cl-x-dev", state))
		default:
			http.NotFound(w, r)
		}
	}))

	cmd := NewEnvCmd()
	stdout, stderr, err := runWithFastWait(t, cmd, "up", "dev", "--backend", "x", "--wait", "--output-json")
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
	if got["state"] != "Ready" {
		t.Errorf("final state = %v, want Ready\nstderr=%s", got["state"], stderr)
	}
	if !strings.Contains(stderr, "still Job_Scheduled") {
		t.Errorf("stderr missing wait status:\n%s", stderr)
	}
}

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
	_, _, err := runCmd(t, cmd, context.Background(), "up", "dev", "--backend", "x")
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

// ---- env list ----

func TestEnvList_HappyPath(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/envs" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"env_id":        "11111111-1111-1111-1111-111111111111",
				"env_name":      "dev",
				"namespace":     "cl-x-dev",
				"state":         "Ready",
				"creation_date": "2026-05-04T10:00:00Z",
			},
			{
				"env_id":        "22222222-2222-2222-2222-222222222222",
				"env_name":      "stage",
				"namespace":     "cl-x-stage",
				"state":         "Job_Scheduled",
				"creation_date": "2026-05-04T11:00:00Z",
			},
		})
	}))
	cmd := NewEnvCmd()
	stdout, _, err := runCmd(t, cmd, context.Background(), "list", "--output-json")
	if err != nil {
		t.Fatalf("env list: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode stdout: %v\nout=%s", err, stdout)
	}
	if len(got) != 2 {
		t.Fatalf("got %d envs, want 2", len(got))
	}
	names := []string{got[0]["env_name"].(string), got[1]["env_name"].(string)}
	if !((names[0] == "dev" && names[1] == "stage") || (names[0] == "stage" && names[1] == "dev")) {
		t.Errorf("env names = %v, want dev/stage", names)
	}
}

func TestEnvList_IncludeTerminal(t *testing.T) {
	var capturedQuery string
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	cmd := NewEnvCmd()
	if _, _, err := runCmd(t, cmd, context.Background(),
		"list", "--include-terminal", "--output-json"); err != nil {
		t.Fatalf("env list: %v", err)
	}
	if !strings.Contains(capturedQuery, "include_terminal=true") {
		t.Errorf("query = %q, want include_terminal=true", capturedQuery)
	}
}

func TestEnvList_Empty(t *testing.T) {
	// Force non-TTY so the command prints stderr "no envs" on the empty path
	// (TTY-mode path is the one we want to exercise; --output-json takes a
	// JSON branch instead). runWithFastWait toggles stdoutIsTerminal=false,
	// which still uses the JSON branch — so we need the TTY-true behaviour.
	prev := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdoutIsTerminal = prev })

	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	cmd := NewEnvCmd()
	stdout, stderr, err := runCmd(t, cmd, context.Background(), "list")
	if err != nil {
		t.Fatalf("env list: %v", err)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty when list is empty (TTY mode), got %q", stdout)
	}
	if !strings.Contains(stderr, "no envs") {
		t.Errorf("stderr missing 'no envs' notice:\n%s", stderr)
	}
}

// ---- env status ----

func TestEnvStatus_HappyPath(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/envs/dev" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(envDetailJSON("dev", "cl-x-dev", "Ready"))
	}))

	cmd := NewEnvCmd()
	stdout, _, err := runCmd(t, cmd, context.Background(),
		"status", "dev", "--output-json")
	if err != nil {
		t.Fatalf("env status: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	if got["state"] != "Ready" {
		t.Errorf("state = %v", got["state"])
	}
	if got["env_name"] != "dev" {
		t.Errorf("env_name = %v", got["env_name"])
	}
}

func TestEnvStatus_RequiresName(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server must not be called when name is missing, got %s %s", r.Method, r.URL.Path)
	}))
	cmd := NewEnvCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "status")
	if err == nil {
		t.Fatal("expected missing-name error, got nil")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %T %v, want *output.CLIError", err, err)
	}
	if cerr.Code != output.CodeBadUsage {
		t.Errorf("CLIError.Code = %d, want %d (BadUsage)", cerr.Code, output.CodeBadUsage)
	}
	if !strings.Contains(err.Error(), "cyoda-cloud env list") {
		t.Errorf("err = %v, want hint mentioning 'cyoda-cloud env list'", err)
	}
}

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
	_, _, err := runCmd(t, cmd, context.Background(), "status", "dev")
	if err == nil {
		t.Fatal("env status (404): expected CLIError, got nil")
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
	_, stderr, err := runCmd(t, cmd, context.Background(), "cancel", "dev")
	if err != nil {
		t.Fatalf("env cancel: %v", err)
	}
	if captured.method != http.MethodPost || captured.path != "/v2/envs/dev:cancel" {
		t.Errorf("request = %s %s, want POST /v2/envs/dev:cancel", captured.method, captured.path)
	}
	if !strings.Contains(stderr, "queued for dev") {
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
	_, _, err := runCmd(t, cmd, context.Background(), "cancel", "dev")
	if err == nil || !strings.Contains(err.Error(), "not cancellable") {
		t.Fatalf("err = %v, want conflict surfaced", err)
	}
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
	_, stderr, err := runCmd(t, cmd, context.Background(), "down", "dev")
	if err != nil {
		t.Fatalf("env down: %v", err)
	}
	if captured.method != http.MethodDelete || captured.path != "/v2/envs/dev" {
		t.Errorf("request = %s %s, want DELETE /v2/envs/dev", captured.method, captured.path)
	}
	if !strings.Contains(stderr, "queued for dev") {
		t.Errorf("stderr missing queued message:\n%s", stderr)
	}
}

func TestEnvDown_Wait_PollsUntilGone(t *testing.T) {
	var calls int32
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/envs/dev":
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/envs/dev":
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
			_ = json.NewEncoder(w).Encode(envDetailJSON("dev", "cl-x-dev", "Job_Scheduled"))
		default:
			http.NotFound(w, r)
		}
	}))
	cmd := NewEnvCmd()
	_, stderr, err := runWithFastWait(t, cmd, "down", "dev", "--wait")
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

// TestEnvDown_Wait_RecognisesEnvTornDown verifies that the new TitleCase
// terminal vocabulary (Env_Torn_Down) short-circuits the teardown poll
// loop without requiring a 404. The server may report the terminal state
// before the entity is removed (or, in v0, never remove it).
func TestEnvDown_Wait_RecognisesEnvTornDown(t *testing.T) {
	var getCalls int32
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v2/envs/dev":
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/envs/dev":
			n := atomic.AddInt32(&getCalls, 1)
			if n == 1 {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(envDetailJSON("dev", "cl-x-dev", "Env_Torn_Down"))
				return
			}
			t.Errorf("unexpected extra poll #%d after Env_Torn_Down", n)
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
	stdout, stderr, err := runWithFastWait(t, cmd, "down", "dev", "--wait", "--output-json")
	if err != nil {
		t.Fatalf("env down --wait: %v\nstderr=%s", err, stderr)
	}
	if n := atomic.LoadInt32(&getCalls); n != 1 {
		t.Errorf("GET polls = %d, want exactly 1 (terminal on first)", n)
	}
	if !strings.Contains(stderr, "env dev torn down.") {
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

// TestEnvUp_WaitShortCircuitsOnTerminalInitial covers the idempotent-replay
// path: POST /v2/envs returns 200 with state=Ready already, so --wait must
// skip the poll loop entirely (no GET, no "still …" status lines).
func TestEnvUp_WaitShortCircuitsOnTerminalInitial(t *testing.T) {
	var getCalls int32
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/envs":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK) // 200 = idempotent replay
			_ = json.NewEncoder(w).Encode(envDetailJSON("dev", "cl-x-dev", "Ready"))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/envs/dev":
			atomic.AddInt32(&getCalls, 1)
			http.Error(w, "should not be called", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))

	cmd := NewEnvCmd()
	stdout, stderr, err := runWithFastWait(t, cmd, "up", "dev", "--backend", "x", "--wait", "--output-json")
	if err != nil {
		t.Fatalf("env up --wait (terminal initial): %v\nstderr=%s", err, stderr)
	}
	if n := atomic.LoadInt32(&getCalls); n != 0 {
		t.Errorf("GET /v2/envs/dev calls = %d, want 0 (short-circuit)", n)
	}
	if strings.Contains(stderr, "still ") {
		t.Errorf("stderr should not contain wait status lines:\n%s", stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode stdout: %v\nout=%s", err, stdout)
	}
	if got["state"] != "Ready" {
		t.Errorf("state = %v, want Ready", got["state"])
	}
}

// ---- helpers ----

// runWithFastWait sets up the WaitOpts seam so the wait loop runs in
// milliseconds and forces non-TTY output.
func runWithFastWait(t *testing.T, cmd interface {
	SetOut(io.Writer)
	SetErr(io.Writer)
	SetArgs([]string)
	ExecuteContext(context.Context) error
}, args ...string) (string, string, error) {
	t.Helper()
	prevIsTerm := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdoutIsTerminal = prevIsTerm })

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
