package commands

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

// withXDG isolates the per-test config directory.
func withXDG(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func runConfig(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewConfigCmd()
	var so, se bytes.Buffer
	cmd.SetOut(&so)
	cmd.SetErr(&se)
	cmd.SetArgs(args)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err = cmd.Execute()
	return so.String(), se.String(), err
}

func TestConfig_SetGetRoundTrip(t *testing.T) {
	withXDG(t)

	if _, _, err := runConfig(t, "set", "default_org", "acme"); err != nil {
		t.Fatalf("set: %v", err)
	}
	stdout, _, err := runConfig(t, "get", "default_org")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := strings.TrimRight(stdout, "\n"); got != "acme" {
		t.Errorf("get default_org = %q, want %q", got, "acme")
	}
	// Round-trip through LoadFile to make sure the file shape is sane.
	f, err := config.LoadFile()
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if f.DefaultOrg != "acme" {
		t.Errorf("file.DefaultOrg = %q, want acme", f.DefaultOrg)
	}
}

func TestConfig_GetUnsetReturnsEmptyLine(t *testing.T) {
	withXDG(t)
	stdout, _, err := runConfig(t, "get", "default_org")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Spec: print empty line + exit 0 so $(cyoda-cloud config get ...) works.
	if stdout != "\n" {
		t.Errorf("get on unset key stdout = %q, want one newline", stdout)
	}
}

func TestConfig_SetUnknownKey(t *testing.T) {
	withXDG(t)
	_, _, err := runConfig(t, "set", "bogus", "value")
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError, got %T: %v", err, err)
	}
	if cerr.Code != output.CodeBadUsage {
		t.Errorf("Code = %d, want %d (BadUsage)", cerr.Code, output.CodeBadUsage)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should mention key, got: %v", err)
	}
}

func TestConfig_GetUnknownKey(t *testing.T) {
	withXDG(t)
	_, _, err := runConfig(t, "get", "bogus")
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError, got %T: %v", err, err)
	}
	if cerr.Code != output.CodeBadUsage {
		t.Errorf("Code = %d, want %d", cerr.Code, output.CodeBadUsage)
	}
}

func TestConfig_SetInvalidOutputFormat(t *testing.T) {
	withXDG(t)
	_, _, err := runConfig(t, "set", "output_format", "yaml")
	if err == nil {
		t.Fatal("expected error for invalid output_format, got nil")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) || cerr.Code != output.CodeBadUsage {
		t.Errorf("err = %v, want CLIError{BadUsage}", err)
	}
}

func TestConfig_SetValidOutputFormats(t *testing.T) {
	for _, v := range []string{"table", "json"} {
		t.Run(v, func(t *testing.T) {
			withXDG(t)
			if _, _, err := runConfig(t, "set", "output_format", v); err != nil {
				t.Fatalf("set output_format=%s: %v", v, err)
			}
		})
	}
}

func TestConfig_SetInvalidDiscoveryURL(t *testing.T) {
	withXDG(t)
	cases := []string{
		"not a url",
		"ftp://example.com",
		"://broken",
	}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			_, _, err := runConfig(t, "set", "discovery_url", v)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", v)
			}
			var cerr *output.CLIError
			if !errors.As(err, &cerr) || cerr.Code != output.CodeBadUsage {
				t.Errorf("err = %v, want CLIError{BadUsage}", err)
			}
		})
	}
}

func TestConfig_SetValidDiscoveryURLs(t *testing.T) {
	cases := []string{
		"https://example.com/disco.json",
		"http://localhost:8080/disco.json",
		"file:///tmp/disco.json",
	}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			withXDG(t)
			if _, _, err := runConfig(t, "set", "discovery_url", v); err != nil {
				t.Fatalf("set discovery_url=%s: %v", v, err)
			}
		})
	}
}

func TestConfig_List(t *testing.T) {
	withXDG(t)
	if _, _, err := runConfig(t, "set", "default_org", "acme"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, _, err := runConfig(t, "set", "output_format", "json"); err != nil {
		t.Fatalf("set: %v", err)
	}
	stdout, _, err := runConfig(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout, "default_org=acme") {
		t.Errorf("list missing default_org, got: %s", stdout)
	}
	if !strings.Contains(stdout, "output_format=json") {
		t.Errorf("list missing output_format, got: %s", stdout)
	}
	if !strings.Contains(stdout, "discovery_url=") {
		t.Errorf("list missing discovery_url, got: %s", stdout)
	}
}
