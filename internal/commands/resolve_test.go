package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
)

// writeMalformedConfig drops a TOML file that fails to parse — used by the
// resolve-helper warn tests below.
func writeMalformedConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(config.ConfigFilePath()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(config.ConfigFilePath(), []byte("default_org = \"acme\nunterminated"), 0o600); err != nil {
		t.Fatalf("write malformed config: %v", err)
	}
	// Re-arm the warn-once sentinel so each test sees a fresh "first encounter".
	config.ResetLoadFileWarnOnceForTest()
	t.Cleanup(config.ResetLoadFileWarnOnceForTest)
}

// newResolveTestCmd builds a minimal cobra command with the same flag shape
// the real commands register, so resolveOrg / resolveOutputJSON exercise the
// `Changed()` path correctly.
func newResolveTestCmd(buf *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("org", "", "")
	cmd.Flags().Bool("output-json", false, "")
	cmd.SetErr(buf)
	return cmd
}

// TestResolveOrg_MalformedConfigWarns ensures that a broken config.toml
// surfaces a warning to the command's stderr rather than being silently
// swallowed (F-003).
func TestResolveOrg_MalformedConfigWarns(t *testing.T) {
	writeMalformedConfig(t)

	var stderr bytes.Buffer
	cmd := newResolveTestCmd(&stderr)

	got := resolveOrg(cmd, "")
	if got != "" {
		t.Errorf("resolveOrg = %q, want \"\" (fallback when config unreadable)", got)
	}
	if w := stderr.String(); !strings.Contains(w, "warning:") || !strings.Contains(w, "unreadable") {
		t.Errorf("expected warn-on-stderr, got: %q", w)
	}
}

// TestResolveOutputJSON_MalformedConfigWarns mirrors the resolveOrg test for
// the --output-json path.
func TestResolveOutputJSON_MalformedConfigWarns(t *testing.T) {
	writeMalformedConfig(t)

	var stderr bytes.Buffer
	cmd := newResolveTestCmd(&stderr)

	got := resolveOutputJSON(cmd, false)
	if got {
		t.Errorf("resolveOutputJSON = true, want false (fallback when config unreadable)")
	}
	if w := stderr.String(); !strings.Contains(w, "warning:") || !strings.Contains(w, "unreadable") {
		t.Errorf("expected warn-on-stderr, got: %q", w)
	}
}

// TestResolveHelpers_WarnOncePerProcess: chained commands sharing a process
// should not flood stderr with a per-call warning. The sync.Once guarantee is
// enforced at the config-package level; this test checks the wiring via the
// resolve helpers.
func TestResolveHelpers_WarnOncePerProcess(t *testing.T) {
	writeMalformedConfig(t)

	var stderr bytes.Buffer
	cmd := newResolveTestCmd(&stderr)

	_ = resolveOrg(cmd, "")
	_ = resolveOutputJSON(cmd, false)
	_ = resolveOrg(cmd, "")

	count := strings.Count(stderr.String(), "warning:")
	if count != 1 {
		t.Errorf("expected exactly 1 warning, got %d in: %q", count, stderr.String())
	}
}
