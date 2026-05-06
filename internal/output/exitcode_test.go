package output

import (
	"errors"
	"fmt"
	"testing"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
)

func TestExit_NilZero(t *testing.T) {
	if got := Exit(nil); got != 0 {
		t.Errorf("Exit(nil) = %d, want 0", got)
	}
}

func TestExit_GenericForPlainError(t *testing.T) {
	if got := Exit(errors.New("boom")); got != int(CodeGeneric) {
		t.Errorf("Exit(plain err) = %d, want %d", got, CodeGeneric)
	}
}

func TestExit_CLIErrorReturnsCode(t *testing.T) {
	err := &CLIError{Code: CodeTierNotEntitled, Err: errors.New("nope")}
	if got := Exit(err); got != int(CodeTierNotEntitled) {
		t.Errorf("Exit(CLIError tier) = %d, want %d", got, CodeTierNotEntitled)
	}
}

func TestExit_CLIErrorWrapped(t *testing.T) {
	inner := &CLIError{Code: CodeNotFound, Err: errors.New("missing")}
	wrapped := fmt.Errorf("context: %w", inner)
	if got := Exit(wrapped); got != int(CodeNotFound) {
		t.Errorf("Exit(wrapped CLIError) = %d, want %d", got, CodeNotFound)
	}
}

func TestFromProblem_SlugTable(t *testing.T) {
	cases := []struct {
		slug string
		want Code
	}{
		{"unauthenticated", CodeUnauthenticated},
		{"revoked", CodeUnauthenticated},
		{"forbidden", CodeForbidden},
		{"tier-not-entitled", CodeTierNotEntitled},
		{"quota-exceeded", CodeQuotaExceeded},
		{"not-found", CodeNotFound},
		{"idempotency-conflict", CodeConflict},
		{"cursor-expired", CodeConflict},
		{"validation-error", CodeBadUsage},
		{"invalid-org-id", CodeBadUsage},
		{"upstream-failure", CodeUpstreamFailure},
	}
	for _, tc := range cases {
		t.Run(tc.slug, func(t *testing.T) {
			p := &api.Problem{
				Type:   "https://docs.cyoda.cloud/errors/" + tc.slug,
				Title:  tc.slug,
				Status: 400, // status is irrelevant when slug matches
			}
			if got := FromProblem(p); got != tc.want {
				t.Errorf("FromProblem(%s) = %d, want %d", tc.slug, got, tc.want)
			}
		})
	}
}

func TestFromProblem_StatusFallback(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   Code
	}{
		{"401", 401, CodeUnauthenticated},
		{"403", 403, CodeForbidden},
		{"404", 404, CodeNotFound},
		{"409", 409, CodeConflict},
		{"412", 412, CodeServerMinVersionRequired},
		{"426", 426, CodeServerMinVersionRequired},
		{"429", 429, CodeQuotaExceeded},
		{"500", 500, CodeUpstreamFailure},
		{"503", 503, CodeUpstreamFailure},
		{"418", 418, CodeGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &api.Problem{Type: "about:blank", Title: "fallback", Status: tc.status}
			if got := FromProblem(p); got != tc.want {
				t.Errorf("FromProblem(status=%d) = %d, want %d", tc.status, got, tc.want)
			}
		})
	}
}

func TestFromProblem_UnknownSlugFallsBackToStatus(t *testing.T) {
	p := &api.Problem{
		Type:   "https://docs.cyoda.cloud/errors/something-new",
		Title:  "weird",
		Status: 403,
	}
	if got := FromProblem(p); got != CodeForbidden {
		t.Errorf("FromProblem(unknown slug, 403) = %d, want %d", got, CodeForbidden)
	}
}

func TestFromProblem_NilReturnsGeneric(t *testing.T) {
	if got := FromProblem(nil); got != CodeGeneric {
		t.Errorf("FromProblem(nil) = %d, want %d", got, CodeGeneric)
	}
}

func TestWrapHTTP_2xxReturnsNil(t *testing.T) {
	if got := WrapHTTP(200, nil); got != nil {
		t.Errorf("WrapHTTP(200) = %v, want nil", got)
	}
	if got := WrapHTTP(204, nil); got != nil {
		t.Errorf("WrapHTTP(204) = %v, want nil", got)
	}
}

func TestWrapHTTP_NoBodyUsesStatus(t *testing.T) {
	got := WrapHTTP(404, nil)
	if got == nil {
		t.Fatal("WrapHTTP(404,nil) = nil, want CLIError")
	}
	if got.Code != CodeNotFound {
		t.Errorf("Code = %d, want %d", got.Code, CodeNotFound)
	}
	if got.Error() == "" {
		t.Error("Error() should not be empty")
	}
}

func TestWrapHTTP_BodyDrivesCodeAndMessage(t *testing.T) {
	detail := "App deployment is not yet available on your tier."
	p := &api.Problem{
		Type:   "https://docs.cyoda.cloud/errors/tier-not-entitled",
		Title:  "Subscription tier does not allow this action",
		Status: 403,
		Detail: &detail,
	}
	got := WrapHTTP(403, p)
	if got == nil {
		t.Fatal("WrapHTTP(403, tier-not-entitled) = nil")
	}
	if got.Code != CodeTierNotEntitled {
		t.Errorf("Code = %d, want %d", got.Code, CodeTierNotEntitled)
	}
	msg := got.Error()
	if msg == "" {
		t.Error("Error() empty")
	}
	// Must include both title and detail for actionable text.
	if !contains(msg, "Subscription tier") || !contains(msg, "deployment") {
		t.Errorf("Error() = %q, missing title or detail", msg)
	}
}

func TestProblemTypeSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"about:blank", ""},
		{"https://docs.cyoda.cloud/errors/tier-not-entitled", "tier-not-entitled"},
		{"forbidden", "forbidden"},
		// trailing slash: i+1 == len(t) so we return the URI as-is; the
		// caller treats unknown slugs as a status fallback so the exact
		// value here is non-load-bearing.
		{"https://x/", "https://x/"},
	}
	for _, tc := range cases {
		if got := problemTypeSlug(tc.in); got != tc.want {
			t.Errorf("problemTypeSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
