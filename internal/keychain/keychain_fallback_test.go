package keychain

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestFileFallback exercises the file-fallback path used by environments
// without an OS keychain (headless Linux runners, CI). Runs without a build
// tag — i.e. always.
func TestFileFallback(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpHome)
	t.Setenv("CYODA_KEYCHAIN_FILE_FALLBACK", "1")

	// Reset the once-warning so this test sees the fallback warning fire
	// (it is logged to stderr; we don't assert on its contents here, just
	// that the path itself works).
	resetFallbackWarning()

	p := Profile{
		Org:           "acme",
		RefreshToken:  "rt-fallback",
		APIURL:        "https://api.cyoda.cloud",
		Auth0Domain:   "tenant.eu.auth0.com",
		Auth0ClientID: "native-client-id",
		Auth0Audience: "https://api.cyoda.cloud",
	}

	if err := Store(p); err != nil {
		t.Fatalf("Store: %v", err)
	}

	credPath := filepath.Join(tmpHome, "cyoda-cloud", "credentials")
	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("expected credentials file at %s: %v", credPath, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("credentials file mode = %o, want 0600", mode)
	}

	got, err := Load("acme")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RefreshToken != "rt-fallback" {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, "rt-fallback")
	}
	if got.Auth0Domain != "tenant.eu.auth0.com" {
		t.Errorf("Auth0Domain = %q", got.Auth0Domain)
	}

	// Loading a missing org should return ErrNotFound.
	if _, err := Load("does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load(missing) err = %v, want ErrNotFound", err)
	}

	if err := Delete("acme"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := Load("acme"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load after Delete err = %v, want ErrNotFound", err)
	}

	// Delete on missing should also return ErrNotFound (parity with go-keyring).
	if err := Delete("acme"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(missing) err = %v, want ErrNotFound", err)
	}
}
