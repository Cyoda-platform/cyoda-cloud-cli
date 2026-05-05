//go:build !ci

package keychain

import (
	"testing"

	"github.com/zalando/go-keyring"
)

func TestStoreAndLoad(t *testing.T) {
	keyring.MockInit()
	if err := Store(Profile{Org: "acme", RefreshToken: "rt-1", APIURL: "https://api.cyoda.cloud"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load("acme")
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != "rt-1" {
		t.Errorf("rt = %q", got.RefreshToken)
	}
}
