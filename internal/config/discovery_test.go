package config

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchDiscovery(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/cyoda-cloud-cli.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"api_url":"https://api.cyoda.cloud",
			"auth0_domain":"tenant.eu.auth0.com",
			"auth0_client_id":"native-client-id",
			"auth0_audience":"https://api.cyoda.cloud"
		}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := FetchDiscovery(srv.URL + "/.well-known/cyoda-cloud-cli.json")
	if err != nil {
		t.Fatal(err)
	}
	if got.APIURL != "https://api.cyoda.cloud" || got.Auth0ClientID != "native-client-id" {
		t.Fatalf("got %+v", got)
	}
}

func TestFetchDiscoveryFileScheme(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cyoda-cloud-cli.json")
	body := []byte(`{
		"api_url":"https://api.cyoda.cloud",
		"auth0_domain":"tenant.eu.auth0.com",
		"auth0_client_id":"native-client-id",
		"auth0_audience":"https://api.cyoda.cloud"
	}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := FetchDiscovery("file://" + path)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIURL != "https://api.cyoda.cloud" || got.Auth0Domain != "tenant.eu.auth0.com" {
		t.Fatalf("got %+v", got)
	}
}

func TestFetchDiscoveryFileSchemeRejectsNonLocalHost(t *testing.T) {
	// "file://relative/path" parses with Host="relative", Path="/path", which
	// is almost certainly a user mistake (they meant file:///abs/path or
	// file:relative/path). Reject it explicitly rather than silently dropping
	// the host segment.
	_, err := FetchDiscovery("file://somehost/etc/passwd")
	if err == nil {
		t.Fatal("expected error for file:// URL with non-local host, got nil")
	}
	if !strings.Contains(err.Error(), "must have empty or \"localhost\" host") {
		t.Errorf("error %q does not mention host requirement", err)
	}
}

func TestFetchDiscoveryFileSchemeAllowsLocalhostHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cyoda-cloud-cli.json")
	body := []byte(`{
		"api_url":"https://api.cyoda.cloud",
		"auth0_domain":"tenant.eu.auth0.com",
		"auth0_client_id":"native-client-id",
		"auth0_audience":"https://api.cyoda.cloud"
	}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := FetchDiscovery("file://localhost" + path)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIURL != "https://api.cyoda.cloud" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveDiscoveryURL_Precedence(t *testing.T) {
	// env > config-file > default. Each subtest builds the precise overlap
	// it cares about so the absent-priority paths are also covered.

	t.Run("default when nothing set", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv(EnvDiscoveryURL, "")
		if got := ResolveDiscoveryURL(); got != DefaultDiscoveryURL {
			t.Errorf("ResolveDiscoveryURL = %q, want %q", got, DefaultDiscoveryURL)
		}
	})

	t.Run("config-file overrides default", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv(EnvDiscoveryURL, "")
		want := "https://config.example/disco.json"
		if err := SaveFile(File{DiscoveryURL: want}); err != nil {
			t.Fatalf("SaveFile: %v", err)
		}
		if got := ResolveDiscoveryURL(); got != want {
			t.Errorf("ResolveDiscoveryURL = %q, want %q", got, want)
		}
	})

	t.Run("env overrides config-file", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		want := "https://env.example/disco.json"
		t.Setenv(EnvDiscoveryURL, want)
		if err := SaveFile(File{DiscoveryURL: "https://config.example/disco.json"}); err != nil {
			t.Fatalf("SaveFile: %v", err)
		}
		if got := ResolveDiscoveryURL(); got != want {
			t.Errorf("ResolveDiscoveryURL = %q, want %q", got, want)
		}
	})
}

func TestLoadDiscoveryFileSchemeBypassesCache(t *testing.T) {
	// Point XDG_CONFIG_HOME at a temp dir so we can verify no cache is written.
	tmpHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpHome)

	dir := t.TempDir()
	path := filepath.Join(dir, "cyoda-cloud-cli.json")
	body := []byte(`{
		"api_url":"https://api.cyoda.cloud",
		"auth0_domain":"tenant.eu.auth0.com",
		"auth0_client_id":"native-client-id",
		"auth0_audience":"https://api.cyoda.cloud"
	}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadDiscovery("file://"+path, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIURL != "https://api.cyoda.cloud" {
		t.Fatalf("got %+v", got)
	}

	// No cache should be written for file:// loads.
	cachePath := filepath.Join(tmpHome, "cyoda-cloud", "discovery.json")
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("expected no cache file at %s, got err=%v", cachePath, err)
	}
}

// TestLoadDiscoveryForceBypassesFreshCache verifies the --refresh-discovery
// flag's underlying mechanism: even when a fresh on-disk cache exists,
// LoadDiscovery(_, force=true) re-fetches over HTTP. The companion path —
// force=false using the cache — is asserted in the same test by counting the
// upstream's hit count.
func TestLoadDiscoveryForceBypassesFreshCache(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpHome)

	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/cyoda-cloud-cli.json", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"api_url":"https://api.cyoda.cloud",
			"auth0_domain":"tenant.eu.auth0.com",
			"auth0_client_id":"native-client-id",
			"auth0_audience":"https://api.cyoda.cloud"
		}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	url := srv.URL + "/.well-known/cyoda-cloud-cli.json"

	// First call: no cache yet -> network hit, write cache.
	if _, err := LoadDiscovery(url, false); err != nil {
		t.Fatalf("first LoadDiscovery: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("after first call, hits=%d want 1", got)
	}

	// Second call without force: fresh cache should serve, no new hit.
	if _, err := LoadDiscovery(url, false); err != nil {
		t.Fatalf("second LoadDiscovery: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("cached call hit upstream: hits=%d want 1", got)
	}

	// Third call WITH force: must bypass the fresh cache and re-fetch.
	if _, err := LoadDiscovery(url, true); err != nil {
		t.Fatalf("forced LoadDiscovery: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("force=true did not re-fetch: hits=%d want 2", got)
	}
}
