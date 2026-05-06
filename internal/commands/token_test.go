package commands

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/keychain"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
)

func TestTokenPrint_RefusesWithoutShow(t *testing.T) {
	setupFileFallback(t)
	stubDiscoveryFile(t, "https://example.invalid")

	cmd := NewTokenCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"print"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError, got %T: %v", err, err)
	}
	if cerr.Code != output.CodeBadUsage {
		t.Errorf("Code = %d, want %d (BadUsage)", cerr.Code, output.CodeBadUsage)
	}
	if !strings.Contains(err.Error(), "--show") {
		t.Errorf("error should mention --show, got: %v", err)
	}
	// stdout must be empty — we never want token bytes on stdout.
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got: %q", stdout.String())
	}
}

func TestTokenPrint_NoProfileReturnsUnauthenticated(t *testing.T) {
	setupFileFallback(t)
	stubDiscoveryFile(t, "https://example.invalid")

	cmd := NewTokenCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"print", "--show"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var cerr *output.CLIError
	if !errors.As(err, &cerr) {
		t.Fatalf("err should be *output.CLIError, got %T: %v", err, err)
	}
	if cerr.Code != output.CodeUnauthenticated {
		t.Errorf("Code = %d, want %d (Unauthenticated)", cerr.Code, output.CodeUnauthenticated)
	}
}

func TestTokenPrint_HappyPath(t *testing.T) {
	setupFileFallback(t)

	// Discovery only needs to point somewhere; Auth0 stub handles the refresh.
	apiSrv := httptest.NewServer(httptest.NewServer(nil).Config.Handler) // placeholder
	apiSrv.Close()
	stubDiscoveryFile(t, "https://example.invalid")

	_, cleanup := stubAuth0Token(t, "AT-printed")
	defer cleanup()

	if err := keychain.Store(keychain.Profile{
		Org:           "",
		RefreshToken:  "RT0",
		APIURL:        "https://example.invalid",
		Auth0Domain:   "ignored.example",
		Auth0ClientID: "client-id",
		Auth0Audience: "https://api.cyoda.cloud",
	}); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}

	cmd := NewTokenCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"print", "--show"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	if err := cmd.Execute(); err != nil {
		t.Fatalf("token print --show: %v\nstderr=%s", err, stderr.String())
	}

	// Token must go to stderr, not stdout.
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "AT-printed") {
		t.Errorf("stderr should contain access token, got: %q", stderr.String())
	}
}
