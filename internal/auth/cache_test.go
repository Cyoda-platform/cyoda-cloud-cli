package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenCache_ReturnsCachedWhenFresh(t *testing.T) {
	var calls int32
	c := NewTokenCache(Tokens{
		AccessToken:  "AT0",
		RefreshToken: "RT0",
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	}, func(ctx context.Context, rt string) (Tokens, error) {
		atomic.AddInt32(&calls, 1)
		return Tokens{AccessToken: "REFRESHED", RefreshToken: rt, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, nil)

	at, err := c.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if at != "AT0" {
		t.Errorf("AT = %q, want AT0", at)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("refresh calls = %d, want 0", got)
	}
}

func TestTokenCache_RefreshesNearExpiry(t *testing.T) {
	var calls int32
	// Within the 60s skew window → must refresh.
	c := NewTokenCache(Tokens{
		AccessToken:  "STALE",
		RefreshToken: "RT0",
		ExpiresAt:    time.Now().Add(30 * time.Second),
	}, func(ctx context.Context, rt string) (Tokens, error) {
		atomic.AddInt32(&calls, 1)
		return Tokens{AccessToken: "FRESH", RefreshToken: "RT1", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, nil)

	at, err := c.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if at != "FRESH" {
		t.Errorf("AT = %q, want FRESH", at)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("refresh calls = %d, want 1", got)
	}
}

func TestTokenCache_PersistsRotatedRT(t *testing.T) {
	var persisted Tokens
	var persistCalls int32
	c := NewTokenCache(Tokens{
		AccessToken:  "STALE",
		RefreshToken: "RT0",
		ExpiresAt:    time.Now().Add(10 * time.Second),
	}, func(ctx context.Context, rt string) (Tokens, error) {
		return Tokens{AccessToken: "FRESH", RefreshToken: "RT1", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, func(t Tokens) error {
		atomic.AddInt32(&persistCalls, 1)
		persisted = t
		return nil
	})

	if _, err := c.AccessToken(context.Background()); err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if atomic.LoadInt32(&persistCalls) != 1 {
		t.Errorf("persist calls = %d, want 1", persistCalls)
	}
	if persisted.RefreshToken != "RT1" {
		t.Errorf("persisted RT = %q, want RT1", persisted.RefreshToken)
	}
}

func TestTokenCache_ConcurrentRefreshesCoalesce(t *testing.T) {
	var calls int32
	gate := make(chan struct{})
	c := NewTokenCache(Tokens{
		AccessToken:  "STALE",
		RefreshToken: "RT0",
		ExpiresAt:    time.Now().Add(10 * time.Second),
	}, func(ctx context.Context, rt string) (Tokens, error) {
		atomic.AddInt32(&calls, 1)
		<-gate // hold first refresh until released
		return Tokens{AccessToken: "FRESH", RefreshToken: "RT1", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, nil)

	var wg sync.WaitGroup
	const N = 10
	wg.Add(N)
	results := make([]string, N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			at, err := c.AccessToken(context.Background())
			if err != nil {
				t.Errorf("AccessToken: %v", err)
				return
			}
			results[i] = at
		}(i)
	}
	// Give all goroutines a moment to queue at the mutex.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("refresh calls = %d, want 1 (mutex must coalesce)", got)
	}
	for i, r := range results {
		if r != "FRESH" {
			t.Errorf("result[%d] = %q, want FRESH", i, r)
		}
	}
}

// TestTokenCache_PersistDoesNotBlockConcurrentReaders verifies the C2 fix:
// while the persist callback is in flight (here gated by a chan), a
// concurrent AccessToken caller observing the freshly-refreshed in-memory
// token must NOT block on the cache mutex. If persist were invoked while
// holding c.mu, the second caller would queue behind it and time out.
func TestTokenCache_PersistDoesNotBlockConcurrentReaders(t *testing.T) {
	persistGate := make(chan struct{})
	persistEntered := make(chan struct{})
	c := NewTokenCache(Tokens{
		AccessToken:  "STALE",
		RefreshToken: "RT0",
		// Within the skew window → the first caller refreshes.
		ExpiresAt: time.Now().Add(10 * time.Second),
	}, func(ctx context.Context, rt string) (Tokens, error) {
		return Tokens{
			AccessToken:  "FRESH",
			RefreshToken: "RT1",
			// Far in the future so the second caller hits the fast path.
			ExpiresAt: time.Now().Add(time.Hour),
		}, nil
	}, func(Tokens) error {
		close(persistEntered)
		<-persistGate // hold persist until the second caller has returned
		return nil
	})

	// First caller refreshes and then blocks inside persist.
	first := make(chan error, 1)
	go func() {
		_, err := c.AccessToken(context.Background())
		first <- err
	}()

	// Wait until the first caller is inside persist (so the in-memory cache
	// has been updated and the mutex has been released).
	select {
	case <-persistEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first AccessToken did not reach persist within 2s")
	}

	// Second caller MUST return immediately with the fresh in-memory token.
	type result struct {
		tok string
		err error
	}
	second := make(chan result, 1)
	go func() {
		tok, err := c.AccessToken(context.Background())
		second <- result{tok, err}
	}()

	select {
	case r := <-second:
		if r.err != nil {
			t.Fatalf("second AccessToken: %v", r.err)
		}
		if r.tok != "FRESH" {
			t.Errorf("second AccessToken = %q, want FRESH", r.tok)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second AccessToken blocked while persist was in flight")
	}

	// Release persist, then make sure the first caller finishes cleanly.
	close(persistGate)
	select {
	case err := <-first:
		if err != nil {
			t.Errorf("first AccessToken: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first AccessToken did not return after persist released")
	}
}

func TestTokenCache_SurfacesSessionExpired(t *testing.T) {
	c := NewTokenCache(Tokens{
		AccessToken:  "STALE",
		RefreshToken: "RT0",
		ExpiresAt:    time.Now().Add(5 * time.Second),
	}, func(ctx context.Context, rt string) (Tokens, error) {
		return Tokens{}, fmt.Errorf("refresh: %w", ErrSessionExpired)
	}, nil)

	_, err := c.AccessToken(context.Background())
	if !errors.Is(err, ErrSessionExpired) {
		t.Errorf("err = %v, want ErrSessionExpired", err)
	}
}
