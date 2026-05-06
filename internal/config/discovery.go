package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/version"
)

// Discovery is the well-known document describing the Cyoda Cloud deployment
// the CLI should target.
type Discovery struct {
	APIURL        string `json:"api_url"`
	Auth0Domain   string `json:"auth0_domain"`
	Auth0ClientID string `json:"auth0_client_id"`
	Auth0Audience string `json:"auth0_audience"`
}

// DefaultDiscoveryURL is the production discovery endpoint.
const DefaultDiscoveryURL = "https://cyoda.cloud/.well-known/cyoda-cloud-cli.json"

// EnvDiscoveryURL is the env var that overrides the discovery URL. Lifted
// from docs/cli-handover.md §"Auth0 setup" so local development can point
// the CLI at a file:// or staging discovery document.
const EnvDiscoveryURL = "CYODA_CLOUD_DISCOVERY_URL"

// ResolveDiscoveryURL returns the URL to fetch discovery from, applying the
// standard CLI precedence: env var (CYODA_CLOUD_DISCOVERY_URL) > config file
// (discovery_url) > DefaultDiscoveryURL.
//
// A LoadFile failure is non-fatal — the env var path and the hard-coded
// default still work even when the user's config TOML is malformed, and
// discovery resolution shouldn't fail the command for an orthogonal
// config-file problem. We DO emit a one-line warning to stderr so the user
// knows the config was ignored: a silently-skipped malformed file could
// otherwise mask a wrong API target without any feedback. The warning is
// also printed verbatim by `cyoda-cloud config get/set/list`, which call
// LoadFile directly and return the error.
var resolveDiscoveryWarn = func(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
}

func ResolveDiscoveryURL() string {
	if v := os.Getenv(EnvDiscoveryURL); v != "" {
		return v
	}
	f, err := LoadFile()
	if err != nil {
		resolveDiscoveryWarn("warning: %s unreadable: %v\n", ConfigFilePath(), err)
		return DefaultDiscoveryURL
	}
	if f.DiscoveryURL != "" {
		return f.DiscoveryURL
	}
	return DefaultDiscoveryURL
}

const cacheTTL = 24 * time.Hour

// maxDiscoveryBody caps the response body to 64 KiB; the document is tiny and
// we don't want a hostile or misconfigured server to push us into OOM.
const maxDiscoveryBody = 64 * 1024

// discoveryClient is the package-private HTTP client used to fetch the
// discovery document. The Timeout covers the full request, so callers don't
// need to layer on a context.WithTimeout.
var discoveryClient = &http.Client{Timeout: 10 * time.Second}

// FetchDiscovery retrieves and decodes the discovery document at the given URL.
// Supports https:// (and http:// for tests) plus a file:// scheme for local
// development.
func FetchDiscovery(rawURL string) (Discovery, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Discovery{}, fmt.Errorf("discovery: parse url: %w", err)
	}
	if u.Scheme == "file" {
		return fetchDiscoveryFile(u)
	}
	return fetchDiscoveryHTTP(rawURL)
}

func fetchDiscoveryHTTP(rawURL string) (Discovery, error) {
	// Use context.Background so callers/tests can swap a different context in
	// the future if desired; the client Timeout already bounds the request.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		return Discovery{}, fmt.Errorf("discovery: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// User-Agent + Cyoda-Cloud-CLI-Version are the CLI's standard identity
	// headers (see version.SetStandardHeaders). Both this discovery client
	// and the min-version fetcher in commands/version.go use the helper so
	// the manager sees a consistent identity across the two non-API HTTP
	// paths that bypass api.Transport.
	version.SetStandardHeaders(req)

	resp, err := discoveryClient.Do(req)
	if err != nil {
		return Discovery{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Discovery{}, fmt.Errorf("discovery: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "application/json") {
		return Discovery{}, fmt.Errorf("discovery: unexpected content-type %q", ct)
	}
	body := io.LimitReader(resp.Body, maxDiscoveryBody)
	return decodeDiscovery(body)
}

func fetchDiscoveryFile(u *url.URL) (Discovery, error) {
	// Per RFC 8089 the host on a file:// URL is empty or "localhost"; anything
	// else is a malformed/relative URL (e.g. file://relative/path parses with
	// Host="relative", Path="/path") and we reject it explicitly.
	if u.Host != "" && u.Host != "localhost" {
		return Discovery{}, fmt.Errorf("discovery: file:// URL must have empty or \"localhost\" host (got %q)", u.Host)
	}
	path := u.Path
	// Fallback for opaque file: URLs without "//" — e.g. file:relative/path —
	// where Host and Path are both empty and the path lives in Opaque.
	if path == "" && u.Host == "" {
		path = u.Opaque
	}
	f, err := os.Open(path)
	if err != nil {
		return Discovery{}, fmt.Errorf("discovery: open %s: %w", path, err)
	}
	defer f.Close()
	return decodeDiscovery(f)
}

func decodeDiscovery(r io.Reader) (Discovery, error) {
	var d Discovery
	if err := json.NewDecoder(r).Decode(&d); err != nil {
		return Discovery{}, fmt.Errorf("discovery: decode: %w", err)
	}
	if d.APIURL == "" || d.Auth0ClientID == "" || d.Auth0Domain == "" || d.Auth0Audience == "" {
		return Discovery{}, fmt.Errorf("discovery: incomplete response")
	}
	return d, nil
}

// LoadDiscovery returns the cached discovery (if fresh) or fetches and caches.
// file:// URLs bypass the cache entirely.
func LoadDiscovery(rawURL string, force bool) (Discovery, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Discovery{}, fmt.Errorf("discovery: parse url: %w", err)
	}
	if u.Scheme == "file" {
		return FetchDiscovery(rawURL)
	}

	cachePath := filepath.Join(ConfigDir(), "discovery.json")
	if !force {
		if d, ok := readFreshCache(cachePath); ok {
			return d, nil
		}
	}
	d, err := FetchDiscovery(rawURL)
	if err != nil {
		return Discovery{}, err
	}
	_ = writeCache(cachePath, d)
	return d, nil
}

type cachedDiscovery struct {
	FetchedAt time.Time `json:"fetched_at"`
	Data      Discovery `json:"data"`
}

func readFreshCache(path string) (Discovery, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Discovery{}, false
	}
	var c cachedDiscovery
	if err := json.Unmarshal(b, &c); err != nil {
		return Discovery{}, false
	}
	if time.Since(c.FetchedAt) > cacheTTL {
		return Discovery{}, false
	}
	return c.Data, true
}

// writeCache atomically writes the discovery cache: write to a tmp file with
// mode 0600, then rename into place. On any failure we best-effort remove the
// tmp file.
func writeCache(path string, d Discovery) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(cachedDiscovery{FetchedAt: time.Now(), Data: d})
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
