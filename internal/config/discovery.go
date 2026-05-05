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
	"time"
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

const cacheTTL = 24 * time.Hour

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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Discovery{}, fmt.Errorf("discovery: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Discovery{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Discovery{}, fmt.Errorf("discovery: status %d", resp.StatusCode)
	}
	return decodeDiscovery(resp.Body)
}

func fetchDiscoveryFile(u *url.URL) (Discovery, error) {
	path := u.Path
	if path == "" {
		// e.g. file://relative/path — uncommon but tolerate.
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

func writeCache(path string, d Discovery) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(cachedDiscovery{FetchedAt: time.Now(), Data: d})
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
