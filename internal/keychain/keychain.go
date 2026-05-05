// Package keychain stores per-organization Cyoda Cloud credentials.
//
// By default it uses the host OS keychain via github.com/zalando/go-keyring.
// Environments without a keychain (headless Linux, CI) can opt into a
// file-based fallback by setting CYODA_KEYCHAIN_FILE_FALLBACK=1; credentials
// are then written to <ConfigDir>/credentials with mode 0600.
package keychain

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
)

const service = "cyoda-cloud"

// envFileFallback selects the file-based fallback when set to "1".
const envFileFallback = "CYODA_KEYCHAIN_FILE_FALLBACK"

// ErrNotFound is returned when a profile does not exist.
var ErrNotFound = errors.New("keychain: profile not found")

// Profile holds the per-organization credentials and discovery snapshot.
type Profile struct {
	Org           string `json:"org"`
	RefreshToken  string `json:"refresh_token"`
	APIURL        string `json:"api_url"`
	Auth0Domain   string `json:"auth0_domain"`
	Auth0ClientID string `json:"auth0_client_id"`
	Auth0Audience string `json:"auth0_audience"`
}

// Store persists the profile under p.Org.
func Store(p Profile) error {
	if useFallback() {
		warnFallbackOnce()
		return fileStore(p)
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return keyring.Set(service, p.Org, string(b))
}

// Load retrieves the profile for org. Returns ErrNotFound if absent.
func Load(org string) (Profile, error) {
	if useFallback() {
		warnFallbackOnce()
		return fileLoad(org)
	}
	s, err := keyring.Get(service, org)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Profile{}, ErrNotFound
		}
		return Profile{}, fmt.Errorf("keychain load: %w", err)
	}
	var p Profile
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return Profile{}, fmt.Errorf("keychain decode: %w", err)
	}
	return p, nil
}

// Delete removes the profile for org. Returns ErrNotFound if absent.
func Delete(org string) error {
	if useFallback() {
		warnFallbackOnce()
		return fileDelete(org)
	}
	if err := keyring.Delete(service, org); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("keychain delete: %w", err)
	}
	return nil
}

func useFallback() bool {
	return os.Getenv(envFileFallback) == "1"
}

var (
	fallbackWarnOnce sync.Once
	// warnSink is overridable in tests if needed; defaults to stderr.
	warnSink = os.Stderr
)

func warnFallbackOnce() {
	fallbackWarnOnce.Do(func() {
		fmt.Fprintln(warnSink, "warning: cyoda-cloud is using file-based credential storage (CYODA_KEYCHAIN_FILE_FALLBACK=1); credentials are stored at "+credentialsPath()+" mode 0600.")
	})
}

// resetFallbackWarning allows tests to re-arm the once-only warning.
func resetFallbackWarning() {
	fallbackWarnOnce = sync.Once{}
}

func credentialsPath() string {
	return filepath.Join(config.ConfigDir(), "credentials")
}

type fileStore_ struct {
	Profiles map[string]Profile `json:"profiles"`
}

func readFile() (fileStore_, error) {
	path := credentialsPath()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileStore_{Profiles: map[string]Profile{}}, nil
		}
		return fileStore_{}, fmt.Errorf("keychain file read: %w", err)
	}
	var fs fileStore_
	if err := json.Unmarshal(b, &fs); err != nil {
		return fileStore_{}, fmt.Errorf("keychain file decode: %w", err)
	}
	if fs.Profiles == nil {
		fs.Profiles = map[string]Profile{}
	}
	return fs, nil
}

func writeFile(fs fileStore_) error {
	path := credentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("keychain file mkdir: %w", err)
	}
	b, err := json.MarshalIndent(fs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("keychain file write: %w", err)
	}
	// Re-apply mode in case the file already existed with looser perms.
	return os.Chmod(path, 0o600)
}

func fileStore(p Profile) error {
	fs, err := readFile()
	if err != nil {
		return err
	}
	fs.Profiles[p.Org] = p
	return writeFile(fs)
}

func fileLoad(org string) (Profile, error) {
	fs, err := readFile()
	if err != nil {
		return Profile{}, err
	}
	p, ok := fs.Profiles[org]
	if !ok {
		return Profile{}, ErrNotFound
	}
	return p, nil
}

func fileDelete(org string) error {
	fs, err := readFile()
	if err != nil {
		return err
	}
	if _, ok := fs.Profiles[org]; !ok {
		return ErrNotFound
	}
	delete(fs.Profiles, org)
	return writeFile(fs)
}
