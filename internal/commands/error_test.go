package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// TestProblemFromBody_DecodesProblemJSON: when Content-Type indicates
// application/problem+json and the body decodes cleanly, the helper returns
// a populated *api.Problem. This is the fallback path for unmapped statuses
// (e.g. 426) where codegen exposes no typed *Problem field.
func TestProblemFromBody_DecodesProblemJSON(t *testing.T) {
	body := []byte(`{"title":"upgrade-required","detail":"min CLI 0.4.0","status":426,"type":"https://docs.cyoda.cloud/errors/server-min-version-required"}`)
	p := problemFromBody(body, "application/problem+json")
	if p == nil {
		t.Fatal("problemFromBody returned nil for valid problem+json")
	}
	if p.Title != "upgrade-required" {
		t.Errorf("Title = %q, want upgrade-required", p.Title)
	}
	if p.Detail == nil || *p.Detail != "min CLI 0.4.0" {
		t.Errorf("Detail = %v, want min CLI 0.4.0", p.Detail)
	}
	if p.Status != 426 {
		t.Errorf("Status = %d, want 426", p.Status)
	}
	if !strings.Contains(p.Type, "server-min-version-required") {
		t.Errorf("Type = %q missing slug", p.Type)
	}
}

// TestProblemFromBody_NilOnNonProblemContentType: a plain application/json
// body must not be eagerly decoded — the contract is that the body shape
// is Problem only when Content-Type explicitly says so.
func TestProblemFromBody_NilOnNonProblemContentType(t *testing.T) {
	body := []byte(`{"title":"upgrade-required","status":426}`)
	if p := problemFromBody(body, "application/json"); p != nil {
		t.Errorf("problemFromBody returned non-nil for application/json: %+v", p)
	}
}

// TestProblemFromBody_NilOnMalformedJSON: a malformed body must not panic
// and must return nil so the caller falls back to status-only mapping.
func TestProblemFromBody_NilOnMalformedJSON(t *testing.T) {
	if p := problemFromBody([]byte("not json"), "application/problem+json"); p != nil {
		t.Errorf("problemFromBody returned non-nil for malformed body: %+v", p)
	}
}

// TestProblemFromBody_AcceptsCharsetSuffix: real servers commonly emit
// "application/problem+json; charset=utf-8". The helper must use a substring
// match, not strict equality.
func TestProblemFromBody_AcceptsCharsetSuffix(t *testing.T) {
	body := []byte(`{"title":"x","status":426,"type":"about:blank"}`)
	if p := problemFromBody(body, "application/problem+json; charset=utf-8"); p == nil {
		t.Fatal("problemFromBody returned nil for charset-suffixed content type")
	}
}

// TestProblemToError_FallsBackToBodyWhenTypedNil: this is the regression for
// the unmapped-status case. With typed=nil but a problem+json body, the
// CLIError must carry the body's detail in its message AND the spec §6.8
// exit code (10).
func TestProblemToError_FallsBackToBodyWhenTypedNil(t *testing.T) {
	body := []byte(`{"type":"https://docs.cyoda.cloud/errors/server-min-version-required","title":"upgrade-required","status":426,"detail":"min CLI 0.4.0 required"}`)
	err := problemToError(426, "application/problem+json", body, nil)
	if err == nil {
		t.Fatal("problemToError returned nil for 426")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError (got %T): %v", err, err)
	}
	if cerr.Code != output.CodeServerMinVersionRequired {
		t.Errorf("Code = %d, want %d (CodeServerMinVersionRequired)",
			cerr.Code, output.CodeServerMinVersionRequired)
	}
	if !strings.Contains(err.Error(), "min CLI 0.4.0 required") {
		t.Errorf("err message missing detail: %v", err)
	}
}

// TestProblemToError_PrefersTypedOverBody: when the typed parameter is
// non-nil, the body fallback path is skipped — typed is the contract from
// codegen and must win even if the body says something different.
func TestProblemToError_PrefersTypedOverBody(t *testing.T) {
	typedDetail := "from typed"
	typed := &api.Problem{
		Type:   "https://docs.cyoda.cloud/errors/forbidden",
		Title:  "from typed",
		Status: 403,
		Detail: &typedDetail,
	}
	bodyDetail := []byte(`{"type":"https://docs.cyoda.cloud/errors/upstream-failure","title":"from body","status":503,"detail":"from body"}`)
	err := problemToError(403, "application/problem+json", bodyDetail, typed)
	if err == nil {
		t.Fatal("problemToError returned nil for 403")
	}
	if !strings.Contains(err.Error(), "from typed") {
		t.Errorf("err should reflect typed Problem, got: %v", err)
	}
	if strings.Contains(err.Error(), "from body") {
		t.Errorf("err leaked body content despite typed being non-nil: %v", err)
	}
}
