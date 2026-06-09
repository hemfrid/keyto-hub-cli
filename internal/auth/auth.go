package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

// Options configures the auth login flow.  All fields with non-nil defaults
// are optional; the zero value uses production defaults.
type Options struct {
	// HubURL is the base URL of the Keyto Hub (e.g. "https://hub.keytolabs.com").
	HubURL string
	// OpenURL is called with the browser auth URL.  It defaults to
	// browser.OpenURL.  Inject a stub in tests to avoid real browser launches.
	OpenURL func(string) error
	// HTTP is the *http.Client used for the token exchange.  Defaults to
	// http.DefaultClient.
	HTTP *http.Client
	// Timeout caps the total time waiting for the browser callback.
	// Defaults to 2 minutes.
	Timeout time.Duration
}

// Run performs the RFC 8252 loopback + PKCE browser login flow.
//
// It binds a loopback listener, opens the Hub's authorization page via
// opts.OpenURL, waits for the callback with the authorization code, verifies
// the OAuth state, exchanges the code for a credential, and returns the
// credential.  It does NOT persist the credential — the caller is responsible
// for calling config.Save.
func Run(ctx context.Context, opts Options) (*hub.TokenResponse, error) {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	httpClient := opts.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	// 1. Generate PKCE values.
	pk, err := NewPKCE()
	if err != nil {
		return nil, fmt.Errorf("auth: generate PKCE: %w", err)
	}

	// 2. Bind a random loopback port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("auth: bind loopback listener: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	// 3. Build the authorization URL.
	params := url.Values{}
	params.Set("redirect_uri", redirectURI)
	params.Set("state", pk.State)
	params.Set("code_challenge", pk.Challenge)
	params.Set("code_challenge_method", "S256")
	authURL := opts.HubURL + "/api/cli/auth?" + params.Encode()

	// codeCh carries the authorization code on success; errCh carries an error
	// on state mismatch or handler failure.
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	// 4. Start the loopback callback server.
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		state := q.Get("state")
		code := q.Get("code")

		if state != pk.State {
			http.Error(w, "state mismatch — possible CSRF attack", http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("auth: state mismatch: got %q, want %q", state, pk.State):
			default:
			}
			return
		}

		// Handle OAuth error or missing code before attempting token exchange.
		if oauthErr := q.Get("error"); oauthErr != "" || code == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintln(w, `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>Keyto — authentication failed</title></head>
<body style="font-family:sans-serif;max-width:480px;margin:4rem auto;text-align:center">
  <h1>Authentication failed</h1>
  <p>Authorization was denied or no code was returned. Return to your terminal.</p>
</body>
</html>`)
			select {
			case errCh <- fmt.Errorf("authorization denied or no code returned: %s", oauthErr):
			default:
			}
			return
		}

		// Respond with a friendly page; no secrets included.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintln(w, `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>Keyto — authenticated</title></head>
<body style="font-family:sans-serif;max-width:480px;margin:4rem auto;text-align:center">
  <h1>You're authenticated</h1>
  <p>Return to your terminal — you're all set.</p>
</body>
</html>`)

		select {
		case codeCh <- code:
		default:
		}
	})

	srv := &http.Server{Handler: mux}

	// serverErrCh captures any fatal serve error (distinct from callback errors).
	serverErrCh := make(chan error, 1)
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			select {
			case serverErrCh <- serveErr:
			default:
			}
		}
	}()

	// 3b. Open the browser (after the server is ready to accept connections).
	if err := opts.OpenURL(authURL); err != nil {
		_ = srv.Close()
		return nil, fmt.Errorf("auth: open browser: %w", err)
	}

	// Apply timeout on top of the caller's context.
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 5. Wait for the authorization code (or error/timeout).
	var code string
	select {
	case code = <-codeCh:
		// Happy path — fall through to exchange.
	case authErr := <-errCh:
		_ = srv.Shutdown(context.Background())
		return nil, authErr
	case srvErr := <-serverErrCh:
		return nil, fmt.Errorf("auth: callback server: %w", srvErr)
	case <-timeoutCtx.Done():
		_ = srv.Shutdown(context.Background())
		return nil, fmt.Errorf("auth: timed out waiting for browser callback (%s)", timeout)
	}

	// Shut down the callback server gracefully.
	_ = srv.Shutdown(context.Background())

	// 6. Exchange the code for a credential.
	c := &hub.Client{
		BaseURL: opts.HubURL,
		HTTP:    httpClient,
	}
	tr, err := c.ExchangeToken(timeoutCtx, code, pk.Verifier, redirectURI)
	if err != nil {
		return nil, fmt.Errorf("auth: token exchange: %w", err)
	}

	return tr, nil
}
