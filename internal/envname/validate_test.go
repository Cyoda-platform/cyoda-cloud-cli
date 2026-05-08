package envname

import "testing"

func TestValidate_ValidNames(t *testing.T) {
	cases := []string{
		"dev",
		"prod",
		"d",                      // single letter
		"a1",                     // letter + digit
		"my-env",                 // internal hyphen
		"abc123",                 // letters + digits
		"a-b-c-d",                // multiple separated hyphens
		"abcdefghijklmnopqrstuv", // 22 chars (max length)
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Validate(name); err != nil {
				t.Errorf("Validate(%q) = %v, want nil", name, err)
			}
		})
	}
}

func TestValidate_InvalidShape(t *testing.T) {
	cases := []struct {
		name   string
		reason string
	}{
		{"", "empty"},
		{"DEV", "uppercase"},
		{"Dev", "mixed case"},
		{"1dev", "leading digit"},
		{"-dev", "leading hyphen"},
		{"dev-", "trailing hyphen"},
		{"my--env", "consecutive hyphens"},
		{"my_env", "underscore"},
		{"my.env", "dot"},
		{"my env", "space"},
		{"abcdefghijklmnopqrstuvw", "23 chars (over max)"},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			if err := Validate(c.name); err == nil {
				t.Errorf("Validate(%q) = nil, want error (%s)", c.name, c.reason)
			}
		})
	}
}

func TestValidate_ReservedNames(t *testing.T) {
	for _, name := range []string{"default", "kube-system", "kube-public", "kube-node-lease"} {
		t.Run(name, func(t *testing.T) {
			if err := Validate(name); err == nil {
				t.Errorf("Validate(%q) = nil, want reserved error", name)
			}
		})
	}
}

func TestValidate_ReservedPrefixes(t *testing.T) {
	cases := []string{
		"app",
		"app-",
		"app-foo",
		"cl",
		"cl-",
		"cl-foo",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			err := Validate(name)
			// "app-" and "cl-" trip the trailing-hyphen rule first; both are
			// rejected, just with different messages — we only assert that
			// they fail.
			if err == nil {
				t.Errorf("Validate(%q) = nil, want reserved-prefix error", name)
			}
		})
	}
}

// TestValidate_LookalikeAllowed proves the prefix rule is anchored — names
// that *contain* "app-" or "cl-" but don't start with them are fine.
func TestValidate_LookalikeAllowed(t *testing.T) {
	for _, name := range []string{"my-app", "my-cl", "happ", "clean"} {
		t.Run(name, func(t *testing.T) {
			if err := Validate(name); err != nil {
				t.Errorf("Validate(%q) = %v, want nil (not actually reserved)", name, err)
			}
		})
	}
}
