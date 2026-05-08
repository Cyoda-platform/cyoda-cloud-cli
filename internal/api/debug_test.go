package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// roundTripFunc adapts a function into an http.RoundTripper for tests.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWrapDebug_NoOpWhenDisabled(t *testing.T) {
	inner := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	got := WrapDebug(inner, io.Discard, false)
	// When disabled, WrapDebug must NOT wrap — verify by asserting the
	// returned type is not *debugTransport. We can't compare functions
	// with == (Go panic), so structural assertion is the right shape.
	if _, wrapped := got.(*debugTransport); wrapped {
		t.Errorf("WrapDebug(_, _, false) wrapped despite disabled flag")
	}
}

func TestDebugTransport_LogsRequestAndResponseAndRedactsAuth(t *testing.T) {
	var buf bytes.Buffer
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		// Echo back: 200 with JSON body and a Set-Cookie that should be redacted.
		resp := &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    r,
		}
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Set("Set-Cookie", "session=secret-session-cookie")
		return resp, nil
	})
	rt := WrapDebug(inner, &buf, true)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.example/v2/things?x=1", strings.NewReader(`{"y":2}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-jwt-token-here")
	req.Header.Set("X-Custom", "ok-to-log")

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if string(got) != `{"ok":true}` {
		t.Errorf("body tampered: got %q", got)
	}

	out := buf.String()
	cases := []struct {
		want string
		desc string
	}{
		{"→ POST https://api.example/v2/things?x=1", "request line"},
		{"← 200 200 OK", "response status line"},
		{"X-Custom: ok-to-log", "non-secret request header logged"},
		{"Authorization: <redacted>", "Authorization redacted in request"},
		{"Set-Cookie: <redacted>", "Set-Cookie redacted in response"},
		{`body: {"y":2}`, "request body logged"},
		{`body: {"ok":true}`, "response body logged"},
	}
	for _, tc := range cases {
		if !strings.Contains(out, tc.want) {
			t.Errorf("missing %s — wanted substring %q in:\n%s", tc.desc, tc.want, out)
		}
	}
	if strings.Contains(out, "secret-jwt-token-here") {
		t.Errorf("Authorization value leaked into trace:\n%s", out)
	}
	if strings.Contains(out, "secret-session-cookie") {
		t.Errorf("Set-Cookie value leaked into trace:\n%s", out)
	}
}

func TestDebugTransport_LogsRoundTripError(t *testing.T) {
	var buf bytes.Buffer
	inner := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("boom")
	})
	rt := WrapDebug(inner, &buf, true)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.example/x", nil)
	if _, err := rt.RoundTrip(req); err == nil || err.Error() != "boom" {
		t.Errorf("expected boom error, got %v", err)
	}
	if !strings.Contains(buf.String(), "← error: boom") {
		t.Errorf("expected error line in trace, got:\n%s", buf.String())
	}
}

func TestDebugTransport_TruncatesLargeBody(t *testing.T) {
	var buf bytes.Buffer
	big := strings.Repeat("A", 16<<10) // 16 KiB
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(big)),
		}, nil
	})
	rt := WrapDebug(inner, &buf, true)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.example/big", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(buf.String(), "[truncated,") {
		t.Errorf("expected truncation marker, got:\n%s", buf.String()[:300])
	}
}

func TestIsDebugEnabled(t *testing.T) {
	// Strict allowlist: only canonical truthy tokens (case-insensitive)
	// enable debug. Anything else — empty, unknown, typo — is off.
	// This is safer than the inverse "everything except a small off-list"
	// rule, which silently treats "fasle"/"of"/etc. as truthy.
	cases := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"FALSE": false,
		"no":    false,
		"off":   false,
		"foo":   false, // unknown — off
		"2":     false, // not in allowlist

		"1":    true,
		"true": true,
		"yes":  true,
		"on":   true,
		// Case-insensitive over the allowlist:
		"TRUE": true,
		"Yes":  true,
		"ON":   true,
		"True": true,
	}
	for in, want := range cases {
		if got := IsDebugEnabled(in); got != want {
			t.Errorf("IsDebugEnabled(%q) = %v, want %v", in, got, want)
		}
	}
}
