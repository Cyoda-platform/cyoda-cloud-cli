package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/auth"
)

// newCacheWithRefresh constructs a TokenCache whose refresh function returns
// the next access token from atSeq each time it is called. Each call
// increments calls. No persist callback is attached — these tests exercise
// transport-level retry semantics, not persistence.
func newCacheWithRefresh(t *testing.T, atSeq []string, calls *int32) *auth.TokenCache {
	t.Helper()
	if len(atSeq) == 0 {
		t.Fatal("newCacheWithRefresh: empty atSeq")
	}
	return auth.NewTokenCache(auth.Tokens{
		AccessToken:  atSeq[0],
		RefreshToken: "RT0",
		// Far in the future so AccessToken returns the cached token without
		// refreshing — until the test calls Invalidate (via 401 handling).
		ExpiresAt: time.Now().Add(time.Hour),
	}, func(ctx context.Context, rt string) (auth.Tokens, error) {
		idx := atomic.AddInt32(calls, 1)
		// idx is 1-based; map idx==1 → atSeq[1], idx==2 → atSeq[2], ...
		if int(idx) >= len(atSeq) {
			return auth.Tokens{}, errors.New("refresh: out of fixtures")
		}
		return auth.Tokens{
			AccessToken:  atSeq[idx],
			RefreshToken: rt,
			ExpiresAt:    time.Now().Add(time.Hour),
		}, nil
	}, nil)
}

func TestTransport_InjectsHeaders(t *testing.T) {
	var gotAuth, gotVer, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotVer = r.Header.Get("Cyoda-Cloud-CLI-Version")
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var calls int32
	cache := newCacheWithRefresh(t, []string{"AT0"}, &calls)
	tr := &Transport{
		Cache:      cache,
		CLIVersion: "1.2.3",
		UserAgent:  "cyoda-cloud-cli/1.2.3 (test arch)",
	}
	cli := &http.Client{Transport: tr}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if gotAuth != "Bearer AT0" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer AT0")
	}
	if gotVer != "1.2.3" {
		t.Errorf("Cyoda-Cloud-CLI-Version = %q, want 1.2.3", gotVer)
	}
	if gotUA != "cyoda-cloud-cli/1.2.3 (test arch)" {
		t.Errorf("User-Agent = %q", gotUA)
	}
}

// TestTransport_DoesNotMutateCallerRequest asserts the RoundTripper contract:
// the request handed to RoundTrip must not be mutated. We construct a request
// with no headers and verify it still has none after the call.
func TestTransport_DoesNotMutateCallerRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var calls int32
	tr := &Transport{
		Cache:      newCacheWithRefresh(t, []string{"AT0"}, &calls),
		CLIVersion: "v",
		UserAgent:  "ua",
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("caller request was mutated: Authorization=%q", got)
	}
	if got := req.Header.Get("User-Agent"); got != "" {
		t.Errorf("caller request was mutated: User-Agent=%q", got)
	}
}

// TestTransport_RetriesOnce401 verifies that a 401 with a token the cache
// thinks is fresh triggers Invalidate + retry. The fake server returns 401
// for AT0 and 200 for AT1.
func TestTransport_RetriesOnce401(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		seen = append(seen, auth)
		if auth == "Bearer AT0" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var refreshCalls int32
	cache := newCacheWithRefresh(t, []string{"AT0", "AT1"}, &refreshCalls)
	tr := &Transport{Cache: cache, CLIVersion: "v", UserAgent: "ua"}
	cli := &http.Client{Transport: tr}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if len(seen) != 2 {
		t.Fatalf("server saw %d requests (%v), want 2", len(seen), seen)
	}
	if seen[0] != "Bearer AT0" || seen[1] != "Bearer AT1" {
		t.Errorf("auth headers = %v, want [Bearer AT0, Bearer AT1]", seen)
	}
	if atomic.LoadInt32(&refreshCalls) != 1 {
		t.Errorf("refresh calls = %d, want 1", refreshCalls)
	}
}

// TestTransport_401TwiceBubbles asserts that two consecutive 401s — even
// after a forced refresh — return the second response to the caller without
// a third retry.
func TestTransport_401TwiceBubbles(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "unauthorized")
	}))
	defer srv.Close()

	var refreshCalls int32
	cache := newCacheWithRefresh(t, []string{"AT0", "AT1", "AT2"}, &refreshCalls)
	tr := &Transport{Cache: cache, CLIVersion: "v", UserAgent: "ua"}
	cli := &http.Client{Transport: tr}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hits = %d, want 2 (no third retry)", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "unauthorized") {
		t.Errorf("body = %q, want to contain 'unauthorized'", body)
	}
}

// TestTransport_RetriesPOSTBodyReplay verifies that on a 401, a POST whose
// body is built from a replayable source (strings.NewReader, which causes
// http.NewRequest to populate GetBody) is retried with the SAME body bytes.
// Without explicit body-replay handling the second attempt would send an
// empty body because req.Clone does not clone Body.
func TestTransport_RetriesPOSTBodyReplay(t *testing.T) {
	const wantBody = "hello-replay-body"
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if r.Header.Get("Authorization") == "Bearer AT0" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if string(b) != wantBody {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var refreshCalls int32
	cache := newCacheWithRefresh(t, []string{"AT0", "AT1"}, &refreshCalls)
	tr := &Transport{Cache: cache, CLIVersion: "v", UserAgent: "ua"}
	cli := &http.Client{Transport: tr}
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(wantBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if len(bodies) != 2 {
		t.Fatalf("server saw %d requests (%v), want 2", len(bodies), bodies)
	}
	if bodies[0] != wantBody {
		t.Errorf("first request body = %q, want %q", bodies[0], wantBody)
	}
	if bodies[1] != wantBody {
		t.Errorf("retry request body = %q, want %q (body replay broken)", bodies[1], wantBody)
	}
}

// nopGetBodyStripper wraps a Request after construction to clear GetBody,
// simulating a non-replayable streaming body (e.g. an arbitrary io.Reader
// passed to http.NewRequest).
type readerWithoutGetBody struct{ io.Reader }

func (readerWithoutGetBody) Close() error { return nil }

// TestTransport_NoRetryWhenBodyNotReplayable verifies that a POST whose body
// has no GetBody (i.e. cannot be replayed) does NOT retry on 401. Instead
// the original 401 response is surfaced unchanged so callers can map it to
// ErrSessionExpired and prompt the user to re-login. Retrying with an empty
// body would fail the server's request validation and produce a confusing
// downstream error.
func TestTransport_NoRetryWhenBodyNotReplayable(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "unauthorized")
	}))
	defer srv.Close()

	var refreshCalls int32
	cache := newCacheWithRefresh(t, []string{"AT0", "AT1"}, &refreshCalls)
	tr := &Transport{Cache: cache, CLIVersion: "v", UserAgent: "ua"}

	// Build a POST request with a non-replayable body. http.NewRequest
	// populates GetBody for *strings.Reader; wrapping it in a type that only
	// satisfies io.Reader (via embedding) defeats the type-switch and leaves
	// GetBody nil.
	req, err := http.NewRequest(http.MethodPost, srv.URL, readerWithoutGetBody{strings.NewReader("nope")})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if req.GetBody != nil {
		t.Fatalf("test setup invalid: GetBody should be nil for non-replayable body")
	}

	cli := &http.Client{Transport: tr}
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want 1 (no retry for non-replayable body)", got)
	}
	// Confirm the 401 body is still readable by the caller.
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "unauthorized") {
		t.Errorf("body = %q, want to contain 'unauthorized'", body)
	}
}

// TestTransport_ContextCancel ensures a cancelled context aborts the request.
func TestTransport_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block forever so cancellation is what ends the call.
		<-r.Context().Done()
	}))
	defer srv.Close()

	var calls int32
	tr := &Transport{
		Cache:      newCacheWithRefresh(t, []string{"AT0"}, &calls),
		CLIVersion: "v",
		UserAgent:  "ua",
	}
	cli := &http.Client{Transport: tr}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if _, err := cli.Do(req); err == nil {
		t.Fatal("Do: expected error on cancellation, got nil")
	}
}
