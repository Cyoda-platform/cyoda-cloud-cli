package main

import (
	"os"
	"testing"
)

// TestRun_ExitsZeroOnHelp is a smoke test for the run() wrapper. It feeds
// `--help` into os.Args and asserts the wrapper returns 0 — i.e. the
// output.Exit(nil) path. We deliberately avoid testing os.Exit directly;
// the per-Code mapping is covered by output.exitcode_test.go.
func TestRun_ExitsZeroOnHelp(t *testing.T) {
	old := os.Args
	t.Cleanup(func() { os.Args = old })
	os.Args = []string{"cyoda-cloud", "--help"}
	if got := run(); got != 0 {
		t.Errorf("run(--help) = %d, want 0", got)
	}
}

// TestRun_ExitsThreeForUnauthenticated relies on the keychain file fallback
// to surface a "not logged in" CLIError when no profile exists, which the
// CLIError mapping translates to exit code 3.
func TestRun_ExitsThreeForUnauthenticated(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CYODA_KEYCHAIN_FILE_FALLBACK", "1")
	// Point discovery at a file:// URL that doesn't exist — actually we
	// need a valid discovery so BuildAPIClient gets past discovery and
	// fails on the keychain. Use a temp file with a stub.
	disco := t.TempDir() + "/disco.json"
	if err := os.WriteFile(disco, []byte(`{
		"api_url":"https://example.invalid",
		"auth0_domain":"a.example",
		"auth0_client_id":"c",
		"auth0_audience":"https://api.cyoda.cloud"
	}`), 0o600); err != nil {
		t.Fatalf("write disco: %v", err)
	}
	t.Setenv("CYODA_CLOUD_DISCOVERY_URL", "file://"+disco)

	old := os.Args
	t.Cleanup(func() { os.Args = old })
	os.Args = []string{"cyoda-cloud", "whoami"}
	if got := run(); got != 3 {
		t.Errorf("run(whoami, no profile) = %d, want 3 (CodeUnauthenticated)", got)
	}
}
