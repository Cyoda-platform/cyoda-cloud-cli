package auth

import "sync"

// authBaseURLMu serialises tests that override the package-level authBaseURL.
// It is shared by every test in the auth package and by cross-package
// callers via SetAuthBaseURLForTest. Without it, parallel tests would race
// on the global. Lightweight tests in pkce_test.go avoid the global and can
// run with t.Parallel().
//
// This symbol lives in a non-_test.go file because cross-package tests in
// internal/commands need to drive auth code against an httptest server, and
// _test.go symbols are not exported across packages. The mutex itself is
// unexported; only SetAuthBaseURLForTest is reachable.
var authBaseURLMu sync.Mutex

// SetAuthBaseURLForTest overrides the package-level Auth0 base URL and
// returns a restore function. Tests use this to point auth code at an
// httptest server.
//
// The function acquires authBaseURLMu so concurrent test invocations
// (within the auth package or from other packages such as
// internal/commands) serialise. The returned restore() restores the
// previous URL FIRST and THEN releases the mutex, so other waiters never
// observe a transient default.
//
// Production code never calls this — it is only a test seam. Naming it
// "ForTest" and gating the override through this single helper keeps the
// surface area small and documents intent.
func SetAuthBaseURLForTest(u string) (restore func()) {
	authBaseURLMu.Lock()
	prev := authBaseURL
	authBaseURL = func(_ string) string { return u }
	return func() {
		authBaseURL = prev
		authBaseURLMu.Unlock()
	}
}
