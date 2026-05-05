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
//
// Persistence runs OUTSIDE the cache mutex (see TokenCache for the rationale).
// Consequently a persist failure leaves the in-memory cache updated but the
// on-disk snapshot stale. The next process restart will use the stale RT and
// either succeed (if Auth0 has not rotated yet) or get ErrSessionExpired,
// forcing the user to re-login. This is the deliberate contract: we prefer
// not to roll back a successful in-memory refresh just because the keychain
// write failed, since the live process can keep working.
type PersistFunc func(Tokens) error

// TokenCache is a per-process, per-profile cache of tokens with built-in
// auto-refresh. Each API client owns one cache; sharing across profiles would
// cross-contaminate refresh tokens.
//
// Concurrency: a single mutex coalesces parallel API calls so only one
// refresh fires at a time. singleflight would be overkill — the cache
// instance itself is already scoped to one profile.
//
// The mutex is held only while reading the cached token, performing the
// network refresh, and updating the in-memory tokens. The PersistFunc
// callback (which may write to the OS keychain — a potentially slow
// operation) is invoked AFTER the mutex is released, so a slow keychain
// write does not block concurrent readers. Concurrent callers that arrive
// while persist is in flight will see the freshly-refreshed in-memory token
// immediately and return without queuing.
//
// The classic double-check pattern applies: after acquiring the lock, the
// cached expiry is re-checked. If a previous holder of the lock just
// refreshed, the second caller short-circuits on the now-fresh token and
// never invokes the refresh.
//
// Persistence is fire-and-forget from the cache's perspective: a persist
// failure surfaces to the caller as the AccessToken error, but the
// in-memory cache has already been updated with the new tokens. See
// PersistFunc for the implications.
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
//
// The mutex is released before invoking the persist callback so a slow
// keychain write does not block other goroutines from reading the cached
// token. See the TokenCache godoc for the contract this implies.
func (c *TokenCache) AccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.tokens.AccessToken != "" && time.Until(c.tokens.ExpiresAt) > refreshSkew {
		tok := c.tokens.AccessToken
		c.mu.Unlock()
		return tok, nil
	}
	newToks, err := c.refresh(ctx, c.tokens.RefreshToken)
	if err != nil {
		c.mu.Unlock()
		return "", err
	}
	c.tokens = newToks
	persist := c.persist
	c.mu.Unlock()

	if persist != nil {
		if err := persist(newToks); err != nil {
			return "", err
		}
	}
	return newToks.AccessToken, nil
}

// Tokens returns a copy of the currently cached tokens. Useful for callers
// that need the refresh token alongside the access token (e.g. logout).
func (c *TokenCache) Tokens() Tokens {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens
}
