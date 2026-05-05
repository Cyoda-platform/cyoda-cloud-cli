package auth

import (
	"context"
	"sync"
	"time"
)

// refreshSkew is how close to expiry we consider a token stale and refresh
// proactively. 60s buys enough headroom that an in-flight API call doesn't
// expire mid-RTT.
const refreshSkew = 60 * time.Second

// RefreshFunc mints a new Tokens from a refresh token. Production wires this
// to Refresh(); tests inject a recorder.
type RefreshFunc func(ctx context.Context, refreshToken string) (Tokens, error)

// PersistFunc, when non-nil, is invoked after a successful refresh so the
// caller can save the (possibly rotated) refresh token. Errors are returned
// to the AccessToken caller — failing to persist a rotated RT would silently
// strand the next process startup.
type PersistFunc func(Tokens) error

// TokenCache is a per-process, per-profile cache of tokens with built-in
// auto-refresh. Each API client owns one cache; sharing across profiles would
// cross-contaminate refresh tokens.
//
// Concurrency: a single mutex coalesces parallel API calls so only one
// refresh fires at a time. singleflight would be overkill — the cache
// instance itself is already scoped to one profile.
type TokenCache struct {
	mu      sync.Mutex
	tokens  Tokens
	refresh RefreshFunc
	persist PersistFunc
}

// NewTokenCache wraps an initial Tokens snapshot with a refresh function and
// optional persistence callback.
func NewTokenCache(initial Tokens, refresh RefreshFunc, persist PersistFunc) *TokenCache {
	return &TokenCache{tokens: initial, refresh: refresh, persist: persist}
}

// AccessToken returns a non-expired access token, refreshing if within the
// 60s skew window. On invalid_grant the underlying refresh returns
// ErrSessionExpired, which we propagate verbatim so callers can map it to
// the "Session expired; run \"cyoda-cloud login\"." prompt.
func (c *TokenCache) AccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.tokens.AccessToken != "" && time.Until(c.tokens.ExpiresAt) > refreshSkew {
		return c.tokens.AccessToken, nil
	}
	newToks, err := c.refresh(ctx, c.tokens.RefreshToken)
	if err != nil {
		return "", err
	}
	c.tokens = newToks
	if c.persist != nil {
		if err := c.persist(newToks); err != nil {
			return "", err
		}
	}
	return c.tokens.AccessToken, nil
}

// Tokens returns a copy of the currently cached tokens. Useful for callers
// that need the refresh token alongside the access token (e.g. logout).
func (c *TokenCache) Tokens() Tokens {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens
}
