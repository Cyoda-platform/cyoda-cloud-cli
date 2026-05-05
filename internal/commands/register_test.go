package commands

import "testing"

// TestRegisterCmd_DoesNotExposeSignupFlag verifies that the `register`
// subcommand does NOT carry a `--signup` flag. The previous implementation
// shared the login flag set and pre-set --signup=true before parsing, which
// meant that `register --signup=false` silently degraded to a login. The
// minimum guard against that regression is keeping the flag off `register`
// entirely.
func TestRegisterCmd_DoesNotExposeSignupFlag(t *testing.T) {
	t.Parallel()
	cmd := NewRegisterCmd()
	if f := cmd.Flags().Lookup("signup"); f != nil {
		t.Errorf("register should not expose --signup flag, got %+v", f)
	}
	// Sanity: the flags it should keep.
	for _, name := range []string{"device", "org", "scope"} {
		if f := cmd.Flags().Lookup(name); f == nil {
			t.Errorf("register should expose --%s flag", name)
		}
	}
}

// TestLoginCmd_ExposesSignupFlag is the converse — `login` must keep
// `--signup` so users can opt into the signup screen on the login command.
func TestLoginCmd_ExposesSignupFlag(t *testing.T) {
	t.Parallel()
	cmd := NewLoginCmd()
	if f := cmd.Flags().Lookup("signup"); f == nil {
		t.Errorf("login should expose --signup flag")
	}
}
