package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestPKCE_VerifierFormat(t *testing.T) {
	t.Parallel()
	v, err := NewPKCEVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) < 43 || len(v) > 128 {
		t.Errorf("verifier length %d outside RFC 7636 range", len(v))
	}
}

func TestPKCE_ChallengeIsBase64URLOfSHA256(t *testing.T) {
	t.Parallel()
	v := PKCEVerifier("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	c := v.Challenge()
	sum := sha256.Sum256([]byte(v))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if string(c) != want {
		t.Errorf("challenge mismatch")
	}
}

// TestPKCE_ChallengeMethodIsS256 exercises buildAuthURL and asserts that
// code_challenge_method=S256 is present. RFC 7636 §4.3 forbids "plain" for our
// threat model, so this is a guardrail against future regressions.
func TestPKCE_ChallengeMethodIsS256(t *testing.T) {
	t.Parallel()
	v, err := NewPKCEVerifier()
	if err != nil {
		t.Fatal(err)
	}
	cfg := LoopbackConfig{
		Auth0Domain: "tenant.eu.auth0.com",
		ClientID:    "abc",
		Audience:    "https://api.cyoda.cloud",
		Scopes:      []string{"openid", "profile"},
	}
	raw := buildAuthURL(cfg, "http://127.0.0.1:1234/callback", v.Challenge(), "STATE")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}
	if got := u.Query().Get("code_challenge"); got == "" {
		t.Errorf("code_challenge is empty")
	}
	if !strings.Contains(raw, "/authorize") {
		t.Errorf("auth URL missing /authorize path: %s", raw)
	}
}
