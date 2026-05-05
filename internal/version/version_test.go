package version

import "testing"

func TestUserAgent(t *testing.T) {
	got := UserAgent("0.1.0", "darwin", "arm64")
	want := "cyoda-cloud-cli/0.1.0 (darwin arm64)"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
