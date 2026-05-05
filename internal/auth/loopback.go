package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// LoopbackConfig configures a PKCE loopback login flow.
//
// OpenBrowser, if non-nil, replaces the default OS-specific browser launcher.
// Tests use it to drive the loopback handshake themselves.
type LoopbackConfig struct {
	Auth0Domain  string
	ClientID     string
	Audience     string
	Scopes       []string
	Organization string // optional Auth0 organization slug
	SignupHint   bool   // request screen_hint=signup

	// OpenBrowser, if set, is called with the authorize URL instead of the
	// platform default. Useful in tests and for headless environments.
	OpenBrowser func(url string) error

	// Stderr, if set, is where the URL fallback ("open this URL manually...")
	// is printed when the browser cannot be opened. Defaults to os.Stderr.
	Stderr io.Writer
}

// Tokens is the result of a successful authentication.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	ExpiresAt    time.Time
	Scope        string
}

// authBaseURL returns the scheme+host portion of the Auth0 endpoints. Tests
// can override this via authBaseURLForTest to point at an httptest server.
//
// We deliberately keep this private and override it in tests rather than
// adding a public field to LoopbackConfig — production callers should never
// have a reason to redirect Auth0 traffic.
var authBaseURL = func(domain string) string { return "https://" + domain }

// httpClient is the package-private HTTP client used for token exchange. We
// allow more headroom than the discovery client because Auth0's token
// endpoint can be slow under load.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// loginTimeout bounds the entire interactive flow. Five minutes is enough for
// the user to authenticate without leaving sessions hanging.
const loginTimeout = 5 * time.Minute

// LoginPKCE runs the OAuth 2.0 Authorization Code + PKCE flow against the
// configured Auth0 tenant via a 127.0.0.1 loopback redirect. Returns on
// callback receipt, error, ctx cancellation, or 5-minute timeout.
func LoginPKCE(ctx context.Context, cfg LoopbackConfig) (Tokens, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Tokens{}, fmt.Errorf("loopback listen: %w", err)
	}
	defer listener.Close()

	verifier, err := NewPKCEVerifier()
	if err != nil {
		return Tokens{}, fmt.Errorf("pkce verifier: %w", err)
	}
	state, err := randomState()
	if err != nil {
		return Tokens{}, fmt.Errorf("random state: %w", err)
	}

	// codeCh / errCh are sized 1 so the handler can post and return without
	// blocking even if the main goroutine has already moved on (e.g. ctx
	// cancellation racing with a late callback).
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		got := q.Get("state")
		if subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1 {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("state mismatch"):
			default:
			}
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, e, http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("auth0 error: %s — %s", e, q.Get("error_description")):
			default:
			}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body><p>Login complete. You can close this tab.</p></body></html>`))
		select {
		case codeCh <- q.Get("code"):
		default:
		}
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(stderr, "loopback server error:", err)
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	authURL := buildAuthURL(cfg, redirectURI, verifier.Challenge(), state)
	open := cfg.OpenBrowser
	if open == nil {
		open = openBrowser
	}
	if err := open(authURL); err != nil {
		// Fallback: print the URL so the user can copy-paste. Do not abort.
		fmt.Fprintln(stderr, "Could not open browser automatically:", err)
		fmt.Fprintln(stderr, "Open this URL to continue login:")
		fmt.Fprintln(stderr, authURL)
	}

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	select {
	case code := <-codeCh:
		return exchangeToken(ctx, cfg, code, verifier, redirectURI)
	case err := <-errCh:
		return Tokens{}, err
	case <-ctx.Done():
		return Tokens{}, ctx.Err()
	case <-time.After(loginTimeout):
		return Tokens{}, fmt.Errorf("login timeout")
	}
}

// buildAuthURL composes the /authorize request URL.
func buildAuthURL(cfg LoopbackConfig, redirectURI string, challenge PKCEChallenge, state string) string {
	u, _ := url.Parse(authBaseURL(cfg.Auth0Domain) + "/authorize")
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("audience", cfg.Audience)
	q.Set("scope", strings.Join(cfg.Scopes, " "))
	q.Set("state", state)
	q.Set("code_challenge", string(challenge))
	q.Set("code_challenge_method", "S256")
	if cfg.Organization != "" {
		q.Set("organization", cfg.Organization)
	}
	if cfg.SignupHint {
		q.Set("screen_hint", "signup")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// randomState returns a 128-bit URL-safe random string used as the OAuth
// state parameter. We bubble rand.Read errors instead of swallowing them —
// silently returning a zero-byte state would make every concurrent login
// share the same state and break CSRF validation.
func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// openBrowser launches the user's default browser to u. Platform-specific.
func openBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Run()
	default:
		return exec.Command("xdg-open", u).Run()
	}
}
