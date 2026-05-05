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
