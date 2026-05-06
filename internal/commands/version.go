package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/version"
)

// minVersionPath is the well-known endpoint per spec §6.7. Operator updates
// the served value via the manager's CLI_MIN_VERSION env var; no static-asset
// deployment.
const minVersionPath = "/v2/.well-known/cli-min-version"

// minVersionCacheTTL mirrors the spec's "cached for 24 h" cadence (§6.7).
const minVersionCacheTTL = 24 * time.Hour

// minVersionMaxBody caps the response body. The endpoint returns a tiny JSON
// {"min": "x.y.z"} document; this guards against a hostile server pushing us
// into OOM.
const minVersionMaxBody = 4 * 1024

// minVersionClient is the package-private HTTP client used to fetch the
// served minimum version. The Timeout covers the full request.
//
// NOTE: spec §6.8 also requires consulting this on every command (cached for
// 24 h) — that's a separate middleware concern that runs before each
// command's RunE. Out of scope for Task 8; only the explicit `version
// --check` path is wired here.
var minVersionClient = &http.Client{Timeout: 10 * time.Second}

// NewVersionCmd returns the `cyoda-cloud version` cobra command.
//
// Without flags it prints the User-Agent string to stdout (existing
// behaviour). With --check it consults the manager's
// /v2/.well-known/cli-min-version endpoint and reports/refuses based on the
// served minimum.
func NewVersionCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version info; --check refreshes server-side minimum",
		RunE: func(cmd *cobra.Command, args []string) error {
			if check {
				return runVersionCheck(cmd)
			}
			fmt.Fprintln(cmd.OutOrStdout(),
				version.UserAgent(version.Version, runtime.GOOS, runtime.GOARCH))
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false,
		"check the server-served minimum CLI version (cached 24h)")
	return cmd
}

// runVersionCheck implements `cyoda-cloud version --check`. The dev short-
// circuit avoids both discovery and HTTP — handy in CI where neither is
// guaranteed to be reachable.
func runVersionCheck(cmd *cobra.Command) error {
	stderr := cmd.ErrOrStderr()
	if version.Version == "dev" {
		fmt.Fprintln(stderr, "running development build; min-version check skipped.")
		return nil
	}
	min, err := loadOrFetchMinVersion(cmd.Context())
	if err != nil {
		// Discovery / HTTP / parse failure. Map to UpstreamFailure (9) — the
		// CLI itself isn't refusing service; the manager couldn't answer. Not
		// CodeServerMinVersionRequired, which has the very specific meaning
		// "server demands a newer CLI".
		return &output.CLIError{
			Code: output.CodeUpstreamFailure,
			Err:  fmt.Errorf("version check: %w", err),
		}
	}
	if !versionGTE(version.Version, min) {
		msg := fmt.Sprintf(
			"cyoda-cloud-cli/%s is below required minimum %s; please upgrade.",
			version.Version, min)
		fmt.Fprintln(stderr, msg)
		return &output.CLIError{
			Code: output.CodeServerMinVersionRequired,
			Err:  errors.New(msg),
		}
	}
	fmt.Fprintf(stderr, "cyoda-cloud-cli/%s is current (min: %s)\n", version.Version, min)
	return nil
}

// versionGTE reports whether actual >= min using semver-2 ordering. Both
// inputs are normalised by prefixing "v" since golang.org/x/mod/semver
// requires the leading v. Invalid versions sort below valid ones (caller
// treats this as "outdated") — guarded by the dev short-circuit upstream.
func versionGTE(actual, min string) bool {
	a := normaliseSemver(actual)
	m := normaliseSemver(min)
	if !semver.IsValid(a) || !semver.IsValid(m) {
		// If either side is unparseable, refuse to claim "ok" — the safer
		// default is to fall through to the outdated path.
		return false
	}
	return semver.Compare(a, m) >= 0
}

// normaliseSemver prepends "v" if missing — golang.org/x/mod/semver expects
// the leading v. Empty input becomes "v0.0.0" which sorts below any real
// release.
func normaliseSemver(v string) string {
	if v == "" {
		return "v0.0.0"
	}
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// minVersionCache is the persisted shape on disk: {fetched_at, min}.
type minVersionCache struct {
	FetchedAt time.Time `json:"fetched_at"`
	Min       string    `json:"min"`
}

// loadOrFetchMinVersion returns the served min, hitting the cache when fresh
// (≤24h) and otherwise re-fetching. Cache writes are best-effort; a failed
// write does not propagate.
func loadOrFetchMinVersion(ctx context.Context) (string, error) {
	cachePath := filepath.Join(config.ConfigDir(), "min-cli-version.json")
	if m, ok := readMinVersionCache(cachePath); ok {
		return m, nil
	}

	// Resolve the API base via discovery; the served endpoint is
	// <api>/v2/.well-known/cli-min-version.
	discoURL := config.DefaultDiscoveryURL
	if v := os.Getenv(envDiscoveryURL); v != "" {
		discoURL = v
	}
	d, err := config.LoadDiscovery(discoURL, false)
	if err != nil {
		return "", fmt.Errorf("discovery: %w", err)
	}
	min, err := fetchMinVersion(ctx, d.APIURL)
	if err != nil {
		return "", err
	}
	_ = writeMinVersionCache(cachePath, min)
	return min, nil
}

// readMinVersionCache returns (min, true) if a fresh cache entry exists.
// "Fresh" means FetchedAt is within minVersionCacheTTL of now. Any I/O,
// parse, or freshness failure returns (_, false) so the caller falls through
// to the network path.
func readMinVersionCache(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var c minVersionCache
	if err := json.Unmarshal(b, &c); err != nil {
		return "", false
	}
	if time.Since(c.FetchedAt) > minVersionCacheTTL {
		return "", false
	}
	if c.Min == "" {
		return "", false
	}
	return c.Min, true
}

// writeMinVersionCache atomically writes a fresh cache entry. mode 0600 even
// though the value is non-secret — keeps file ownership tight in case the
// $XDG_CONFIG_HOME tree is shared.
func writeMinVersionCache(path, min string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(minVersionCache{FetchedAt: time.Now(), Min: min})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// fetchMinVersion does the HTTP GET against <api>/v2/.well-known/cli-min-version.
// Mirrors the discovery client's hardening: explicit Accept, User-Agent,
// content-type sniff, and bounded body read.
func fetchMinVersion(ctx context.Context, apiBase string) (string, error) {
	rawURL := strings.TrimRight(apiBase, "/") + minVersionPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", version.UserAgent(version.Version, runtime.GOOS, runtime.GOARCH))
	req.Header.Set("Cyoda-Cloud-CLI-Version", version.Version)

	resp, err := minVersionClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "application/json") {
		return "", fmt.Errorf("unexpected content-type %q", ct)
	}
	body := io.LimitReader(resp.Body, minVersionMaxBody)
	var doc struct {
		Min string `json:"min"`
	}
	if err := json.NewDecoder(body).Decode(&doc); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if doc.Min == "" {
		return "", errors.New("server returned empty min")
	}
	return doc.Min, nil
}
