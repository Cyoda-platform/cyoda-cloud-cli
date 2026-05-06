package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setXDG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestLoadFile_MissingReturnsZero(t *testing.T) {
	setXDG(t)
	f, err := LoadFile()
	if err != nil {
		t.Fatalf("LoadFile on missing file: %v", err)
	}
	if (f != File{}) {
		t.Errorf("LoadFile = %+v, want zero", f)
	}
}

func TestSaveFile_RoundTrip(t *testing.T) {
	setXDG(t)
	in := File{
		DefaultOrg:   "acme",
		OutputFormat: "json",
		DiscoveryURL: "https://example.com/disco.json",
	}
	if err := SaveFile(in); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	out, err := LoadFile()
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if out != in {
		t.Errorf("round-trip mismatch: got %+v, want %+v", out, in)
	}
}

func TestSaveFile_IsAtomicMode0600(t *testing.T) {
	dir := setXDG(t)
	if err := SaveFile(File{DefaultOrg: "x"}); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	p := filepath.Join(dir, "cyoda-cloud", "config.toml")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Errorf("config.toml mode = %o, expected no group/other perms", st.Mode().Perm())
	}
	// Tmp shouldn't linger in the dir on success.
	entries, err := os.ReadDir(filepath.Join(dir, "cyoda-cloud"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestLoadFile_ParseErrorPropagates(t *testing.T) {
	dir := setXDG(t)
	p := filepath.Join(dir, "cyoda-cloud", "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte("not = valid = toml"), 0o600); err != nil {
		t.Fatalf("write bad toml: %v", err)
	}
	_, err := LoadFile()
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestConfigFilePath(t *testing.T) {
	dir := setXDG(t)
	want := filepath.Join(dir, "cyoda-cloud", "config.toml")
	if got := ConfigFilePath(); got != want {
		t.Errorf("ConfigFilePath = %q, want %q", got, want)
	}
}
