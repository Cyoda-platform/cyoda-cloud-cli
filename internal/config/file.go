package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// File is the user-facing TOML config persisted at ConfigFilePath().
//
// The CLI itself is the only writer; users may edit it by hand but the
// canonical surface is `cyoda-cloud config set <key> <value>` so that the
// allowed-keys validation runs.
//
// All fields are optional; absent values fall through to env vars or defaults.
type File struct {
	DefaultOrg   string `toml:"default_org"`
	OutputFormat string `toml:"output_format"` // "table" | "json"
	DiscoveryURL string `toml:"discovery_url"` // override; empty = use DefaultDiscoveryURL or env
}

// configFileName is the on-disk filename. Hoisted so tests can refer to it
// indirectly via ConfigFilePath().
const configFileName = "config.toml"

// ConfigFilePath returns the absolute path to the per-user config TOML.
// Honors XDG_CONFIG_HOME via ConfigDir().
func ConfigFilePath() string {
	return filepath.Join(ConfigDir(), configFileName)
}

// LoadFile reads + parses the config TOML at ConfigFilePath().
//
// Returns a zero-value File (no error) when the file does not exist — a
// missing config is the default state for a fresh install. Parse errors
// propagate to the caller; the file IS readable but malformed, and silently
// returning zeros would mask user mistakes.
func LoadFile() (File, error) {
	p := ConfigFilePath()
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return File{}, nil
		}
		return File{}, fmt.Errorf("config: read %s: %w", p, err)
	}
	var f File
	if err := toml.Unmarshal(b, &f); err != nil {
		return File{}, fmt.Errorf("config: parse %s: %w", p, err)
	}
	return f, nil
}

// SaveFile writes f to ConfigFilePath() atomically (tmp + rename, mode 0600),
// creating the parent directory as needed.
func SaveFile(f File) error {
	p := ConfigFilePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}
	b, err := toml.Marshal(f)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	tmp := p + ".tmp"
	tf, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("config: open tmp: %w", err)
	}
	if _, err := tf.Write(b); err != nil {
		_ = tf.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("config: write tmp: %w", err)
	}
	if err := tf.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config: close tmp: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}
