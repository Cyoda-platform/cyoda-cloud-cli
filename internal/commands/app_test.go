package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// problemBody is a minimal helper to write an RFC 7807 Problem document.
func problemBody(slug, title string, status int) map[string]any {
	return map[string]any{
		"type":   "https://docs.cyoda.cloud/errors/" + slug,
		"title":  title,
		"status": status,
	}
}

// ---- app build ----

func TestAppBuild_TierNotEntitledMapsExitFive(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/builds" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(problemBody(
			"tier-not-entitled",
			"Subscription tier does not allow this action",
			403,
		))
	}))

	cmd := NewAppCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(),
		"build", "--repo", "https://github.com/x/y", "--branch", "main")
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
	if output.Exit(err) != 5 {
		t.Errorf("Exit = %d, want 5", output.Exit(err))
	}
	if !strings.Contains(cerr.Error(), "Subscription tier") {
		t.Errorf("error message = %q", cerr.Error())
	}
}

func TestAppBuild_PostsBuildAction(t *testing.T) {
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
			"build_id": "b_123",
			"state":    "PROCESSING",
		})
	}))
	cmd := NewAppCmd()
	stdout, _, err := runCmd(t, cmd, context.Background(),
		"build", "--repo", "https://github.com/x/y", "--branch", "feature",
		"--public", "--output-json")
	if err != nil {
		t.Fatalf("app build: %v", err)
	}
	if captured.method != http.MethodPost || captured.path != "/v2/builds" {
		t.Errorf("request = %s %s, want POST /v2/builds", captured.method, captured.path)
	}
	if len(captured.idem) < minIdempotencyKeyLen {
		t.Errorf("Idempotency-Key len = %d, want >= %d", len(captured.idem), minIdempotencyKeyLen)
	}
	if captured.body["action"] != "build" {
		t.Errorf("body.action = %v, want build", captured.body["action"])
	}
	if captured.body["repository_url"] != "https://github.com/x/y" {
		t.Errorf("body.repository_url = %v", captured.body["repository_url"])
	}
	if captured.body["branch_name"] != "feature" {
		t.Errorf("body.branch_name = %v", captured.body["branch_name"])
	}
	if captured.body["is_public"] != true {
		t.Errorf("body.is_public = %v, want true", captured.body["is_public"])
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode stdout: %v\nout=%s", err, stdout)
	}
	if got["build_id"] != "b_123" {
		t.Errorf("build_id = %v", got["build_id"])
	}
}

func TestAppBuild_RequiresRepo(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server must not be called when --repo missing, got %s %s", r.Method, r.URL.Path)
	}))
	cmd := NewAppCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "build")
	if err == nil || !strings.Contains(err.Error(), "--repo is required") {
		t.Fatalf("err = %v, want --repo required", err)
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) || cerr.Code != output.CodeBadUsage {
		t.Errorf("expected CodeBadUsage, got %v", err)
	}
}

func TestAppBuild_RejectsShortIdempotencyKey(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server must not be called for short key")
	}))
	cmd := NewAppCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(),
		"build", "--repo", "x", "--branch", "main", "--idempotency-key", "short")
	if err == nil || !strings.Contains(err.Error(), "at least 16") {
		t.Fatalf("err = %v, want minimum-length error", err)
	}
}

// ---- app deploy ----

func TestAppDeploy_TierNotEntitledMapsExitFive(t *testing.T) {
	var capturedAction string
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["action"].(string); ok {
			capturedAction = v
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(problemBody(
			"tier-not-entitled", "tier blocks deploy", 403,
		))
	}))
	cmd := NewAppCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(),
		"deploy", "--repo", "https://x/y", "--branch", "main")
	if err == nil {
		t.Fatal("expected tier-not-entitled error, got nil")
	}
	if capturedAction != "deploy" {
		t.Errorf("server saw action=%q, want deploy", capturedAction)
	}
	if output.Exit(err) != 5 {
		t.Errorf("Exit = %d, want 5", output.Exit(err))
	}
}

// ---- app list ----

func TestAppList_GETsAndRendersItems(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/builds" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		// Verify query params plumb through.
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("limit = %q, want 10", r.URL.Query().Get("limit"))
		}
		if r.URL.Query().Get("action") != "build" {
			t.Errorf("action = %q, want build", r.URL.Query().Get("action"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"build_id": "b1", "state": "SUCCESS", "action": "build"},
				{"build_id": "b2", "state": "FAILED", "action": "build"},
			},
			"next_cursor": "cur123",
		})
	}))
	cmd := NewAppCmd()
	stdout, _, err := runCmd(t, cmd, context.Background(),
		"list", "--limit", "10", "--action", "build", "--output-json")
	if err != nil {
		t.Fatalf("app list: %v", err)
	}
	var got struct {
		Items      []output.BuildSnapshot `json:"items"`
		NextCursor string                 `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode stdout: %v\nout=%s", err, stdout)
	}
	if len(got.Items) != 2 {
		t.Errorf("items = %d, want 2", len(got.Items))
	}
	if got.NextCursor != "cur123" {
		t.Errorf("next_cursor = %q, want cur123", got.NextCursor)
	}
	if got.Items[0].BuildId != "b1" {
		t.Errorf("items[0].BuildId = %q", got.Items[0].BuildId)
	}
}

func TestAppList_EmptyListIsNotAnError(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{},
		})
	}))
	cmd := NewAppCmd()
	stdout, _, err := runCmd(t, cmd, context.Background(), "list", "--output-json")
	if err != nil {
		t.Fatalf("app list (empty): %v", err)
	}
	var got struct {
		Items      []output.BuildSnapshot `json:"items"`
		NextCursor string                 `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 0 {
		t.Errorf("items = %d, want 0", len(got.Items))
	}
}

func TestAppList_CursorExpiredMapsConflict(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(problemBody(
			"cursor-expired", "Cursor expired; restart pagination", 409,
		))
	}))
	cmd := NewAppCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "list", "--cursor", "stale")
	if err == nil {
		t.Fatal("expected error")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) || cerr.Code != output.CodeConflict {
		t.Errorf("expected CodeConflict (8), got %v (Exit=%d)", err, output.Exit(err))
	}
}

// ---- app status ----

func TestAppStatus_PathParam(t *testing.T) {
	var capturedPath string
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"build_id": "b_xyz",
			"state":    "PROCESSING",
		})
	}))
	cmd := NewAppCmd()
	stdout, _, err := runCmd(t, cmd, context.Background(),
		"status", "b_xyz", "--output-json")
	if err != nil {
		t.Fatalf("app status: %v", err)
	}
	if capturedPath != "/v2/builds/b_xyz" {
		t.Errorf("path = %q, want /v2/builds/b_xyz", capturedPath)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["build_id"] != "b_xyz" {
		t.Errorf("build_id = %v", got["build_id"])
	}
}

func TestAppStatus_NotFoundMapsCodeSeven(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(problemBody("not-found", "no such build", 404))
	}))
	cmd := NewAppCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "status", "b_missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if output.Exit(err) != 7 {
		t.Errorf("Exit = %d, want 7 (NotFound)", output.Exit(err))
	}
}

// ---- app cancel ----

func TestAppCancel_TierNotEntitledMapsExitFive(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/builds/b1:cancel" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(problemBody(
			"tier-not-entitled", "tier blocks cancel", 403,
		))
	}))
	cmd := NewAppCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "cancel", "b1")
	if err == nil {
		t.Fatal("expected error")
	}
	if output.Exit(err) != 5 {
		t.Errorf("Exit = %d, want 5", output.Exit(err))
	}
}

func TestAppCancel_AcceptedQueuesMessage(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	cmd := NewAppCmd()
	_, stderr, err := runCmd(t, cmd, context.Background(), "cancel", "b1")
	if err != nil {
		t.Fatalf("app cancel: %v", err)
	}
	if !strings.Contains(stderr, "queued") {
		t.Errorf("stderr missing queued message:\n%s", stderr)
	}
}

// ---- app delete ----

func TestAppDelete_TierNotEntitledMapsExitFive(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v2/builds/b1" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(problemBody(
			"tier-not-entitled", "tier blocks delete", 403,
		))
	}))
	cmd := NewAppCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_, _, err := runCmd(t, cmd, context.Background(), "delete", "b1")
	if err == nil {
		t.Fatal("expected error")
	}
	if output.Exit(err) != 5 {
		t.Errorf("Exit = %d, want 5", output.Exit(err))
	}
}

func TestAppDelete_AcceptedQueuesMessage(t *testing.T) {
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	cmd := NewAppCmd()
	_, stderr, err := runCmd(t, cmd, context.Background(), "delete", "b1")
	if err != nil {
		t.Fatalf("app delete: %v", err)
	}
	if !strings.Contains(stderr, "queued") {
		t.Errorf("stderr missing queued message:\n%s", stderr)
	}
}

// ---- app build --wait ----

func TestAppBuild_WaitPollsUntilTerminal(t *testing.T) {
	var pollCount int32
	envTestSetup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/builds":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"build_id": "b_w",
				"state":    "PROCESSING",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/builds/b_w":
			n := atomic.AddInt32(&pollCount, 1)
			state := "PROCESSING"
			if n >= 3 {
				state = "SUCCESS"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"build_id": "b_w",
				"state":    state,
			})
		default:
			http.NotFound(w, r)
		}
	}))

	cmd := NewAppCmd()
	stdout, stderr, err := runWithFastWait(t, cmd,
		"build", "--repo", "https://x/y", "--branch", "main", "--wait", "--output-json")
	if err != nil {
		t.Fatalf("app build --wait: %v\nstderr=%s", err, stderr)
	}
	if got := atomic.LoadInt32(&pollCount); got < 3 {
		t.Errorf("polls = %d, want >= 3", got)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode: %v\nout=%s", err, stdout)
	}
	if got["state"] != "SUCCESS" {
		t.Errorf("final state = %v, want SUCCESS", got["state"])
	}
}
