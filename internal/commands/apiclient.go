package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/cyoda-platform/cyoda-cloud-cli/internal/api"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/auth"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/config"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/keychain"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/output"
	"github.com/cyoda-platform/cyoda-cloud-cli/internal/version"
)

// shouldRefreshDiscovery reports whether the global --refresh-discovery flag
// is set on the root cobra command. It walks up to the root so the flag
// (declared on root.PersistentFlags) is found regardless of how deep the
// invoked subcommand sits. Returns false if the flag isn't registered (e.g.
// in a unit test that builds a subcommand in isolation) — that path is
// indistinguishable from "not set" from the caller's perspective.
func shouldRefreshDiscovery(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	root := cmd.Root()
	v, err := root.PersistentFlags().GetBool("refresh-discovery")
	if err != nil {
		return false
	}
	return v
}

// APIBuild bundles the dependencies a command needs to call the manager API.
//
// Most callers only need Client; commands that perform refresh-aware
// operations (e.g. `token print`, logout flows) also use Cache, Discovery
// and Profile — bundling them in a struct avoids the multi-return blank-
// identifier smell at every callsite.
//
// Cache is non-nil only when an authenticated session was successfully
// loaded; today every successful BuildAPIClient produces one, but the type
// stays nilable so future "discovery only" callers don't have to fabricate
// one.
type APIBuild struct {
	Client    *api.ClientWithResponses
	Cache     *auth.TokenCache
	Discovery config.Discovery
	Profile   keychain.Profile
}

// BuildAPIClient resolves discovery + keychain profile and constructs an
// authenticated api.ClientWithResponses ready to call /v2/* endpoints.
//
// The returned APIBuild.Client has a TokenCache wired to refresh + persist
// rotated refresh tokens back to the keychain via makePersistFn. The
// TokenCache, Discovery and Profile are returned alongside so callers that
// need refresh-aware operations (e.g. logout, `token print`) can reuse the
// same snapshot without re-loading.
//
// Errors:
//   - discovery failures wrap "discovery: ...".
//   - keychain.ErrNotFound is translated to a user-facing
//     "not logged in. Run \"cyoda-cloud login\"." error so all command-side
//     callers surface the same actionable text. Other keychain errors wrap
//     "keychain load: ...".
//
// The cmd parameter is consulted for the global --refresh-discovery flag (so
// callers can force-bypass the 24h discovery cache) and is the source of the
// context plumbed into future context-aware discovery fetches. Passing the
// cobra command directly (rather than just a context.Context) avoids a
// cascading signature change every time a new global flag is added.
func BuildAPIClient(cmd *cobra.Command, org string) (*APIBuild, error) {
	d, err := config.LoadDiscovery(config.ResolveDiscoveryURL(), shouldRefreshDiscovery(cmd))
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}

	profile, err := keychain.Load(org)
	if err != nil {
		if errors.Is(err, keychain.ErrNotFound) {
			// Spec §6.6: "not logged in" maps to exit code 3 via the
			// CLIError wrapper. main.go's run() translates this into the
			// process exit code; cobra prints the wrapped message verbatim.
			return nil, &output.CLIError{
				Code: output.CodeUnauthenticated,
				Err:  errors.New("not logged in. Run \"cyoda-cloud login\"."),
			}
		}
		return nil, fmt.Errorf("keychain load: %w", err)
	}

	cli, cache, err := newAuthenticatedClient(profile, d)
	if err != nil {
		return nil, err
	}
	return &APIBuild{
		Client:    cli,
		Cache:     cache,
		Discovery: d,
		Profile:   profile,
	}, nil
}

// newAuthenticatedClient wires together the TokenCache, auth-injecting
// Transport, and oapi-codegen client for the given profile/discovery pair.
//
// The cache's PersistFunc (built via makePersistFn) writes the rotated
// profile back to the keychain so a refresh-token rotation survives across
// invocations.
func newAuthenticatedClient(profile keychain.Profile, d config.Discovery) (*api.ClientWithResponses, *auth.TokenCache, error) {
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
	// Optional outermost wrapper: when CYODA_CLOUD_DEBUG is truthy, log the
	// request and response (with Authorization redacted) to stderr. Sits
	// outside the auth Transport so the trace shows what's actually on the
	// wire after token refresh / 401 retry. Zero overhead when disabled.
	debugEnabled := api.IsDebugEnabled(os.Getenv(api.EnvDebug))
	rt := api.WrapDebug(tr, os.Stderr, debugEnabled)
	// Per-request safety net: a server that completes TLS but never
	// responds would otherwise hang indefinitely (a context-only
	// approach trusts every caller to set a deadline). The --wait
	// poll loops set their own per-iteration ctx deadline well under
	// 60s, so this ceiling does not constrain them.
	httpClient := &http.Client{Transport: rt, Timeout: 60 * time.Second}
	cli, err := api.NewClientWithResponses(d.APIURL, api.WithHTTPClient(httpClient))
	if err != nil {
		return nil, nil, err
	}
	return cli, cache, nil
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
