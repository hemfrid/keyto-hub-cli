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
			fmt.Fprint(w, failurePageHTML)
			select {
			case errCh <- fmt.Errorf("authorization denied or no code returned: %s", oauthErr):
			default:
			}
			return
		}

		// Respond with a friendly page; no secrets included.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, successPageHTML)

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

// The two pages below are served on the loopback callback. They mirror the
// Keyto Hub look (Untitled UI): the Keyto keyhole mark, Inter type, a subtle
// dot-grid backdrop, and an auto light/dark theme that follows the browser's
// prefers-color-scheme (with a [data-theme] override hook). Everything is
// inlined so the page renders even if the Inter web font can't be fetched — it
// falls back to the system sans stack. No secrets are ever embedded.

// successPageHTML is shown after a successful authorization.
const successPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Keyto — authenticated</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>
  :root {
    color-scheme: light dark;
    --bg: #ffffff;
    --grad: radial-gradient(120% 120% at 50% 0%, #ffffff 38%, #f6f7f9 100%);
    --dot: rgba(16, 24, 40, 0.045);
    --fg: #101828;
    --fg-muted: #667085;
    --rule: #eaecf0;
    --mark: #0c0e12;
    --mark-shadow: drop-shadow(0 6px 14px rgba(16, 24, 40, 0.12));
    --footer-mark: #98a2b3;
    --footer-label: #667085;
    --badge-ring: #ffffff;
    --accent: #17b26a;
    --accent-shadow: rgba(7, 148, 85, 0.30);
    --font: "Inter", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  }
  @media (prefers-color-scheme: dark) {
    :root:not([data-theme="light"]) {
      --bg: #0b0d12;
      --grad: radial-gradient(120% 120% at 50% 0%, #161922 0%, #0b0d12 60%);
      --dot: rgba(255, 255, 255, 0.05);
      --fg: #f5f6f8;
      --fg-muted: #94969c;
      --rule: rgba(255, 255, 255, 0.10);
      --mark: #ffffff;
      --mark-shadow: drop-shadow(0 8px 24px rgba(0, 0, 0, 0.55));
      --footer-mark: #555b69;
      --footer-label: #94969c;
      --badge-ring: #0b0d12;
      --accent-shadow: rgba(23, 178, 106, 0.45);
    }
  }
  [data-theme="dark"] {
    --bg: #0b0d12;
    --grad: radial-gradient(120% 120% at 50% 0%, #161922 0%, #0b0d12 60%);
    --dot: rgba(255, 255, 255, 0.05);
    --fg: #f5f6f8;
    --fg-muted: #94969c;
    --rule: rgba(255, 255, 255, 0.10);
    --mark: #ffffff;
    --mark-shadow: drop-shadow(0 8px 24px rgba(0, 0, 0, 0.55));
    --footer-mark: #555b69;
    --footer-label: #94969c;
    --badge-ring: #0b0d12;
    --accent-shadow: rgba(23, 178, 106, 0.45);
  }
  * { box-sizing: border-box; }
  html, body { height: 100%; margin: 0; }
  body {
    font-family: var(--font);
    color: var(--fg);
    background-color: var(--bg);
    background-image:
      radial-gradient(circle at 1px 1px, var(--dot) 1px, transparent 0),
      var(--grad);
    background-size: 22px 22px, 100% 100%;
    display: flex; align-items: center; justify-content: center;
    min-height: 100%; padding: 24px;
    -webkit-font-smoothing: antialiased; text-rendering: optimizeLegibility;
  }
  .stage { width: 100%; max-width: 440px; text-align: center; }
  .mark-wrap {
    position: relative; display: inline-flex; margin-bottom: 36px;
    opacity: 0; animation: pop 0.7s cubic-bezier(0.22, 1, 0.36, 1) 0.05s forwards;
  }
  .mark { width: 76px; height: 73px; color: var(--mark); display: block; filter: var(--mark-shadow); }
  .badge {
    position: absolute; right: -8px; bottom: -6px; width: 32px; height: 32px;
    border-radius: 999px; background: var(--accent); border: 3px solid var(--badge-ring);
    display: flex; align-items: center; justify-content: center;
    box-shadow: 0 4px 12px var(--accent-shadow);
    transform: scale(0); animation: badge 0.55s cubic-bezier(0.34, 1.56, 0.64, 1) 0.55s forwards;
  }
  .badge svg { width: 16px; height: 16px; display: block; }
  h1 {
    font-size: 30px; line-height: 1.2; font-weight: 600; letter-spacing: -0.022em;
    margin: 0 0 10px; color: var(--fg);
    opacity: 0; animation: rise 0.6s cubic-bezier(0.16, 1, 0.3, 1) 0.20s forwards;
  }
  .sub {
    font-size: 16px; line-height: 1.55; color: var(--fg-muted);
    margin: 0 auto; max-width: 320px;
    opacity: 0; animation: rise 0.6s cubic-bezier(0.16, 1, 0.3, 1) 0.30s forwards;
  }
  .footer-rule {
    width: 64px; height: 1px; background: var(--rule);
    margin: 40px auto 0; opacity: 0; animation: fade 0.6s ease 0.38s forwards;
  }
  .footer {
    display: inline-flex; align-items: center; gap: 8px; margin-top: 22px;
    opacity: 0; animation: rise 0.6s cubic-bezier(0.16, 1, 0.3, 1) 0.42s forwards;
  }
  .footer .mark { width: 16px; height: 15px; color: var(--footer-mark); filter: none; }
  .footer span {
    font-size: 13px; font-weight: 500; letter-spacing: 0.06em;
    text-transform: uppercase; color: var(--footer-label);
  }
  @keyframes pop { 0% { opacity: 0; transform: translateY(8px) scale(0.94); } 100% { opacity: 1; transform: translateY(0) scale(1); } }
  @keyframes badge { 0% { transform: scale(0); } 100% { transform: scale(1); } }
  @keyframes rise { 0% { opacity: 0; transform: translateY(10px); } 100% { opacity: 1; transform: translateY(0); } }
  @keyframes fade { to { opacity: 1; } }
  @media (prefers-reduced-motion: reduce) { * { animation: none !important; opacity: 1 !important; transform: none !important; } }
</style>
</head>
<body>
  <main class="stage">
    <div class="mark-wrap">
      <svg class="mark" viewBox="0 0 486 466" role="img" aria-label="Keyto">
        <defs>
          <mask id="khA" maskContentUnits="userSpaceOnUse">
            <rect x="0" y="0" width="486" height="466" fill="#fff"/>
            <circle cx="243" cy="243" r="82" fill="#000"/>
            <path d="M213 232 H273 L307 460 H179 Z" fill="#000"/>
          </mask>
        </defs>
        <path mask="url(#khA)" fill="currentColor"
          d="M289.9 52.2 L440.5 162 Q468 182 457.5 214.4 L391.2 419.5 Q382 448 352 448 L134 448 Q104 448 94.8 419.5 L28.5 214.4 Q18 182 45.5 162 L196.1 52.2 Q243 18 289.9 52.2 Z"/>
      </svg>
      <span class="badge" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none"><path d="M20 6 9 17l-5-5" stroke="#fff" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/></svg>
      </span>
    </div>
    <h1>You&rsquo;re authenticated</h1>
    <p class="sub">Return to your terminal &mdash; you&rsquo;re all set.</p>
    <div class="footer-rule"></div>
    <div class="footer">
      <svg class="mark" viewBox="0 0 486 466" role="img" aria-label="Keyto">
        <defs>
          <mask id="khB" maskContentUnits="userSpaceOnUse">
            <rect x="0" y="0" width="486" height="466" fill="#fff"/>
            <circle cx="243" cy="243" r="82" fill="#000"/>
            <path d="M213 232 H273 L307 460 H179 Z" fill="#000"/>
          </mask>
        </defs>
        <path mask="url(#khB)" fill="currentColor"
          d="M289.9 52.2 L440.5 162 Q468 182 457.5 214.4 L391.2 419.5 Q382 448 352 448 L134 448 Q104 448 94.8 419.5 L28.5 214.4 Q18 182 45.5 162 L196.1 52.2 Q243 18 289.9 52.2 Z"/>
      </svg>
      <span>Keyto Hub</span>
    </div>
  </main>
</body>
</html>`

// failurePageHTML is shown when authorization is denied or no code is returned.
const failurePageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Keyto — authentication failed</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>
  :root {
    color-scheme: light dark;
    --bg: #ffffff;
    --grad: radial-gradient(120% 120% at 50% 0%, #ffffff 38%, #f6f7f9 100%);
    --dot: rgba(16, 24, 40, 0.045);
    --fg: #101828;
    --fg-muted: #667085;
    --rule: #eaecf0;
    --mark: #0c0e12;
    --mark-shadow: drop-shadow(0 6px 14px rgba(16, 24, 40, 0.12));
    --footer-mark: #98a2b3;
    --footer-label: #667085;
    --badge-ring: #ffffff;
    --accent: #f04438;
    --accent-shadow: rgba(217, 45, 32, 0.30);
    --font: "Inter", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  }
  @media (prefers-color-scheme: dark) {
    :root:not([data-theme="light"]) {
      --bg: #0b0d12;
      --grad: radial-gradient(120% 120% at 50% 0%, #161922 0%, #0b0d12 60%);
      --dot: rgba(255, 255, 255, 0.05);
      --fg: #f5f6f8;
      --fg-muted: #94969c;
      --rule: rgba(255, 255, 255, 0.10);
      --mark: #ffffff;
      --mark-shadow: drop-shadow(0 8px 24px rgba(0, 0, 0, 0.55));
      --footer-mark: #555b69;
      --footer-label: #94969c;
      --badge-ring: #0b0d12;
      --accent-shadow: rgba(240, 68, 56, 0.45);
    }
  }
  [data-theme="dark"] {
    --bg: #0b0d12;
    --grad: radial-gradient(120% 120% at 50% 0%, #161922 0%, #0b0d12 60%);
    --dot: rgba(255, 255, 255, 0.05);
    --fg: #f5f6f8;
    --fg-muted: #94969c;
    --rule: rgba(255, 255, 255, 0.10);
    --mark: #ffffff;
    --mark-shadow: drop-shadow(0 8px 24px rgba(0, 0, 0, 0.55));
    --footer-mark: #555b69;
    --footer-label: #94969c;
    --badge-ring: #0b0d12;
    --accent-shadow: rgba(240, 68, 56, 0.45);
  }
  * { box-sizing: border-box; }
  html, body { height: 100%; margin: 0; }
  body {
    font-family: var(--font);
    color: var(--fg);
    background-color: var(--bg);
    background-image:
      radial-gradient(circle at 1px 1px, var(--dot) 1px, transparent 0),
      var(--grad);
    background-size: 22px 22px, 100% 100%;
    display: flex; align-items: center; justify-content: center;
    min-height: 100%; padding: 24px;
    -webkit-font-smoothing: antialiased; text-rendering: optimizeLegibility;
  }
  .stage { width: 100%; max-width: 440px; text-align: center; }
  .mark-wrap {
    position: relative; display: inline-flex; margin-bottom: 36px;
    opacity: 0; animation: pop 0.7s cubic-bezier(0.22, 1, 0.36, 1) 0.05s forwards;
  }
  .mark { width: 76px; height: 73px; color: var(--mark); display: block; filter: var(--mark-shadow); }
  .badge {
    position: absolute; right: -8px; bottom: -6px; width: 32px; height: 32px;
    border-radius: 999px; background: var(--accent); border: 3px solid var(--badge-ring);
    display: flex; align-items: center; justify-content: center;
    box-shadow: 0 4px 12px var(--accent-shadow);
    transform: scale(0); animation: badge 0.55s cubic-bezier(0.34, 1.56, 0.64, 1) 0.55s forwards;
  }
  .badge svg { width: 15px; height: 15px; display: block; }
  h1 {
    font-size: 30px; line-height: 1.2; font-weight: 600; letter-spacing: -0.022em;
    margin: 0 0 10px; color: var(--fg);
    opacity: 0; animation: rise 0.6s cubic-bezier(0.16, 1, 0.3, 1) 0.20s forwards;
  }
  .sub {
    font-size: 16px; line-height: 1.55; color: var(--fg-muted);
    margin: 0 auto; max-width: 340px;
    opacity: 0; animation: rise 0.6s cubic-bezier(0.16, 1, 0.3, 1) 0.30s forwards;
  }
  .footer-rule {
    width: 64px; height: 1px; background: var(--rule);
    margin: 40px auto 0; opacity: 0; animation: fade 0.6s ease 0.38s forwards;
  }
  .footer {
    display: inline-flex; align-items: center; gap: 8px; margin-top: 22px;
    opacity: 0; animation: rise 0.6s cubic-bezier(0.16, 1, 0.3, 1) 0.42s forwards;
  }
  .footer .mark { width: 16px; height: 15px; color: var(--footer-mark); filter: none; }
  .footer span {
    font-size: 13px; font-weight: 500; letter-spacing: 0.06em;
    text-transform: uppercase; color: var(--footer-label);
  }
  @keyframes pop { 0% { opacity: 0; transform: translateY(8px) scale(0.94); } 100% { opacity: 1; transform: translateY(0) scale(1); } }
  @keyframes badge { 0% { transform: scale(0); } 100% { transform: scale(1); } }
  @keyframes rise { 0% { opacity: 0; transform: translateY(10px); } 100% { opacity: 1; transform: translateY(0); } }
  @keyframes fade { to { opacity: 1; } }
  @media (prefers-reduced-motion: reduce) { * { animation: none !important; opacity: 1 !important; transform: none !important; } }
</style>
</head>
<body>
  <main class="stage">
    <div class="mark-wrap">
      <svg class="mark" viewBox="0 0 486 466" role="img" aria-label="Keyto">
        <defs>
          <mask id="khA" maskContentUnits="userSpaceOnUse">
            <rect x="0" y="0" width="486" height="466" fill="#fff"/>
            <circle cx="243" cy="243" r="82" fill="#000"/>
            <path d="M213 232 H273 L307 460 H179 Z" fill="#000"/>
          </mask>
        </defs>
        <path mask="url(#khA)" fill="currentColor"
          d="M289.9 52.2 L440.5 162 Q468 182 457.5 214.4 L391.2 419.5 Q382 448 352 448 L134 448 Q104 448 94.8 419.5 L28.5 214.4 Q18 182 45.5 162 L196.1 52.2 Q243 18 289.9 52.2 Z"/>
      </svg>
      <span class="badge" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none"><path d="M18 6 6 18M6 6l12 12" stroke="#fff" stroke-width="3" stroke-linecap="round"/></svg>
      </span>
    </div>
    <h1>Authentication failed</h1>
    <p class="sub">Authorization was denied or no code was returned. Return to your terminal and try again.</p>
    <div class="footer-rule"></div>
    <div class="footer">
      <svg class="mark" viewBox="0 0 486 466" role="img" aria-label="Keyto">
        <defs>
          <mask id="khB" maskContentUnits="userSpaceOnUse">
            <rect x="0" y="0" width="486" height="466" fill="#fff"/>
            <circle cx="243" cy="243" r="82" fill="#000"/>
            <path d="M213 232 H273 L307 460 H179 Z" fill="#000"/>
          </mask>
        </defs>
        <path mask="url(#khB)" fill="currentColor"
          d="M289.9 52.2 L440.5 162 Q468 182 457.5 214.4 L391.2 419.5 Q382 448 352 448 L134 448 Q104 448 94.8 419.5 L28.5 214.4 Q18 182 45.5 162 L196.1 52.2 Q243 18 289.9 52.2 Z"/>
      </svg>
      <span>Keyto Hub</span>
    </div>
  </main>
</body>
</html>`
