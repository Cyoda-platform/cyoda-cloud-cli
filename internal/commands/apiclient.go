package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/auth"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/keychain"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/version"
)

// BuildAPIClient resolves discovery + keychain profile and constructs an
// authenticated api.ClientWithResponses ready to call /v2/* endpoints.
//
// The returned client has a TokenCache wired to refresh + persist rotated
// refresh tokens back to the keychain via makePersistFn. The Discovery and
// Profile are returned alongside so callers that need refresh-aware
// operations (e.g. logout) can reuse the same snapshot without re-loading.
//
// Errors:
//   - discovery failures wrap "discovery: ...".
//   - keychain.ErrNotFound is translated to a user-facing
//     "not logged in. Run \"cyoda-cloud login\"." error so all command-side
//     callers surface the same actionable text. Other keychain errors wrap
//     "keychain load: ...".
//
// The ctx parameter is plumbed into the discovery loader's HTTP fetch — pass
// cmd.Context() so cobra's signal handler can cancel a hung discovery call.
// (LoadDiscovery does not currently take a context; we accept ctx now for
// forward compatibility and to keep the signature stable as Tasks 6/7 add
// context-aware discovery.)
func BuildAPIClient(ctx context.Context, org string) (*api.ClientWithResponses, config.Discovery, keychain.Profile, error) {
	_ = ctx // reserved for future context-aware discovery; see godoc.

	discoURL := config.DefaultDiscoveryURL
	if v := os.Getenv(envDiscoveryURL); v != "" {
		discoURL = v
	}
	d, err := config.LoadDiscovery(discoURL, false)
	if err != nil {
		return nil, config.Discovery{}, keychain.Profile{}, fmt.Errorf("discovery: %w", err)
	}

	profile, err := keychain.Load(org)
	if err != nil {
		if errors.Is(err, keychain.ErrNotFound) {
			// Task 7 will translate this to exit code 3 via an exitcode
			// middleware. For now return a clear error so the user gets
			// actionable text.
			return nil, d, keychain.Profile{}, errors.New("not logged in. Run \"cyoda-cloud login\".")
		}
		return nil, d, keychain.Profile{}, fmt.Errorf("keychain load: %w", err)
	}

	cli, err := newAuthenticatedClient(profile, d)
	if err != nil {
		return nil, d, profile, err
	}
	return cli, d, profile, nil
}

// newAuthenticatedClient wires together the TokenCache, auth-injecting
// Transport, and oapi-codegen client for the given profile/discovery pair.
//
// The cache's PersistFunc (built via makePersistFn) writes the rotated
// profile back to the keychain so a refresh-token rotation survives across
// invocations.
func newAuthenticatedClient(profile keychain.Profile, d config.Discovery) (*api.ClientWithResponses, error) {
	cache := auth.NewTokenCache(
		auth.Tokens{RefreshToken: profile.RefreshToken},
		func(ctx context.Context, rt string) (auth.Tokens, error) {
			return auth.Refresh(ctx, auth.RefreshConfig{
				Auth0Domain:  d.Auth0Domain,
				ClientID:     d.Auth0ClientID,
				RefreshToken: rt,
			})
		},
		makePersistFn(profile, d),
	)
	tr := &api.Transport{
		Cache:      cache,
		CLIVersion: version.Version,
		UserAgent:  version.UserAgent(version.Version, runtime.GOOS, runtime.GOARCH),
	}
	httpClient := &http.Client{Transport: tr}
	return api.NewClientWithResponses(d.APIURL, api.WithHTTPClient(httpClient))
}

// makePersistFn returns an auth.PersistFunc that, on each successful refresh,
// rebuilds the full keychain.Profile and writes it via keychain.Store.
//
// The closure captures:
//   - profile: the originally-loaded Profile, used as the base for fields
//     that do not change on refresh (Org).
//   - d: a Discovery snapshot, used to overwrite APIURL/Auth0Domain/
//     Auth0ClientID/Auth0Audience on every persist so the on-disk profile
//     stays in sync with the latest discovery document.
//
// Only the RefreshToken field is taken from the freshly-rotated Tokens; the
// access token is never persisted (it lives only in the in-memory cache).
//
// Any error from keychain.Store is returned to the caller (TokenCache.
// AccessToken propagates it). See PersistFunc godoc for the consequences of
// a persist failure: the in-memory cache is already updated, so the running
// process keeps working, but the on-disk RT is stale until the next
// successful persist.
func makePersistFn(profile keychain.Profile, d config.Discovery) auth.PersistFunc {
	return func(t auth.Tokens) error {
		updated := profile
		updated.RefreshToken = t.RefreshToken
		updated.APIURL = d.APIURL
		updated.Auth0Domain = d.Auth0Domain
		updated.Auth0ClientID = d.Auth0ClientID
		updated.Auth0Audience = d.Auth0Audience
		return keychain.Store(updated)
	}
}
