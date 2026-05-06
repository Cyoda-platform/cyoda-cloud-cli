package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

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

// loadFileWarnOnce ensures `command1 && command2` chains only see one
// "config unreadable" warning per process even if both commands resolve
// the same malformed file.
var (
	loadFileWarnOnce sync.Once
	loadFileWarnSink io.Writer = nil // overridden by tests; nil -> os.Stderr
	loadFileWarnMu   sync.Mutex
)

// LoadFileWithWarn is LoadFile with a one-shot stderr warning when the
// underlying file is present but malformed. Callers that need to surface
// config-resolution failures non-fatally (resolveOrg, resolveOutputJSON,
// ResolveDiscoveryURL) use this helper so the user gets feedback even when
// the resolver swallows the error to keep the command running.
//
// The warning fires at most once per process via sync.Once so a chain of
// commands sharing the same broken config doesn't bury the user under N
// copies of the same line.
func LoadFileWithWarn(w io.Writer) (File, error) {
	f, err := LoadFile()
	if err != nil {
		loadFileWarnOnce.Do(func() {
			out := w
			if out == nil {
				loadFileWarnMu.Lock()
				out = loadFileWarnSink
				loadFileWarnMu.Unlock()
			}
			if out == nil {
				out = os.Stderr
			}
			fmt.Fprintf(out, "warning: %s unreadable: %v\n", ConfigFilePath(), err)
		})
	}
	return f, err
}

// ResetLoadFileWarnOnceForTest re-arms the once-per-process warning so a
// test process can drive multiple "first malformed-config encounter" paths.
// Exported for cross-package tests in internal/commands; harmless at runtime.
func ResetLoadFileWarnOnceForTest() {
	loadFileWarnMu.Lock()
	loadFileWarnOnce = sync.Once{}
	loadFileWarnMu.Unlock()
}

// SetLoadFileWarnSinkForTest installs a fallback writer used by
// LoadFileWithWarn when its caller passes nil. Tests use this to capture
// warnings emitted from code paths they cannot otherwise instrument.
// Exported for cross-package tests; pass nil to clear.
func SetLoadFileWarnSinkForTest(w io.Writer) {
	loadFileWarnMu.Lock()
	loadFileWarnSink = w
	loadFileWarnMu.Unlock()
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
