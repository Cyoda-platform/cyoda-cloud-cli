// Package config holds shared configuration helpers (paths, discovery).
package config

import (
	"os"
	"path/filepath"
)

// ConfigDir returns the per-user config directory for the CLI.
// Honors XDG_CONFIG_HOME, falling back to ~/.config/cyoda-cloud.
func ConfigDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "cyoda-cloud")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "cyoda-cloud")
}
