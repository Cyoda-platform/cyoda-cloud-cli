package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/version"
)

// withVersion temporarily overrides version.Version for the duration of the
// test, restoring on cleanup.
func withVersion(t *testing.T, v string) {
	t.Helper()
	prev := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = prev })
}

// minVersionServer stands up an http.Server returning the supplied min on
// /v2/.well-known/cli-min-version. The returned int32* counts hits.
func minVersionServer(t *testing.T, min string) (url string, hits *int32, close func()) {
	t.Helper()
	var c int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/.well-known/cli-min-version" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&c, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"min": min})
	}))
	return srv.URL, &c, srv.Close
}

func runVersion(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewVersionCmd()
	var so, se bytes.Buffer
	cmd.SetOut(&so)
	cmd.SetErr(&se)
	cmd.SetArgs(args)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err = cmd.Execute()
	return so.String(), se.String(), err
}

func TestVersion_PrintsUserAgent(t *testing.T) {
	withVersion(t, "0.1.0")
	stdout, _, err := runVersion(t)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	want := "cyoda-cloud-cli/0.1.0 (" + runtime.GOOS + " " + runtime.GOARCH + ")"
	if got := strings.TrimRight(stdout, "\n"); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestVersionCheck_DevSkipsHTTP(t *testing.T) {
	withVersion(t, "dev")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// No discovery stub: if version --check tried to load discovery, we'd
	// see a discovery error. The dev short-circuit must not attempt either.
	_, stderr, err := runVersion(t, "--check")
	if err != nil {
		t.Fatalf("version --check (dev): %v", err)
	}
	if !strings.Contains(stderr, "development build") {
		t.Errorf("stderr = %q, want development-build note", stderr)
	}
}

func TestVersionCheck_HappyPath(t *testing.T) {
	withVersion(t, "0.1.0")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	apiURL, hits, closeSrv := minVersionServer(t, "0.1.0")
	defer closeSrv()
	stubDiscoveryFile(t, apiURL)

	_, stderr, err := runVersion(t, "--check")
	if err != nil {
		t.Fatalf("version --check: %v\nstderr=%s", err, stderr)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("server hits = %d, want 1", got)
	}
	if !strings.Contains(stderr, "current") {
		t.Errorf("stderr = %q, want \"current\" message", stderr)
	}
}

func TestVersionCheck_Outdated(t *testing.T) {
	withVersion(t, "0.1.0")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	apiURL, _, closeSrv := minVersionServer(t, "0.5.0")
	defer closeSrv()
	stubDiscoveryFile(t, apiURL)

	_, stderr, err := runVersion(t, "--check")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError, got %T: %v", err, err)
	}
	if cerr.Code != output.CodeServerMinVersionRequired {
		t.Errorf("Code = %d, want %d (ServerMinVersionRequired)",
			cerr.Code, output.CodeServerMinVersionRequired)
	}
	if !strings.Contains(stderr, "below required minimum") &&
		!strings.Contains(err.Error(), "below required minimum") {
		t.Errorf("expected below-minimum message, stderr=%q err=%v", stderr, err)
	}
}

func TestVersionCheck_ServerUnreachable(t *testing.T) {
	withVersion(t, "0.1.0")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Bind a port and immediately close: subsequent dial will be refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	stubDiscoveryFile(t, srv.URL)

	_, _, err := runVersion(t, "--check")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var cerr *output.CLIError
	if errors.As(err, &cerr) {
		if cerr.Code == output.CodeServerMinVersionRequired {
			t.Errorf("server-unreachable should NOT map to ServerMinVersionRequired, got %d", cerr.Code)
		}
	}
}

func TestVersionCheck_CachesFor24h(t *testing.T) {
	withVersion(t, "0.1.0")
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	apiURL, hits, closeSrv := minVersionServer(t, "0.1.0")
	defer closeSrv()
	stubDiscoveryFile(t, apiURL)

	if _, _, err := runVersion(t, "--check"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("after first run, hits=%d", got)
	}

	// Confirm cache file exists.
	cachePath := filepath.Join(xdg, "cyoda-cloud", "min-cli-version.json")
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache file: %v", err)
	}

	if _, _, err := runVersion(t, "--check"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("second run hits=%d, want 1 (cached)", got)
	}
}
