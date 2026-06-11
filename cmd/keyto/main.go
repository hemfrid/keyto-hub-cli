package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hemfrid/keyto-hub-cli/internal/auth"
	"github.com/hemfrid/keyto-hub-cli/internal/browser"
	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/credential"
	"github.com/hemfrid/keyto-hub-cli/internal/envsync"
	"github.com/hemfrid/keyto-hub-cli/internal/gitwire"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
	"github.com/hemfrid/keyto-hub-cli/internal/selfupdate"
	"github.com/hemfrid/keyto-hub-cli/internal/shellinit"
	"github.com/hemfrid/keyto-hub-cli/internal/start"
	"github.com/hemfrid/keyto-hub-cli/internal/ui"
)

// defaultHubURL is the production Hub.  Override with KEYTO_HUB_URL.
const defaultHubURL = "https://hub.keytolabs.com"

// hubURL returns the Hub base URL, preferring the KEYTO_HUB_URL env var.
// Trailing slashes are stripped so a value like "https://hub/" does not produce
// double-slash paths (e.g. "//git/...").
func hubURL() string {
	u := os.Getenv("KEYTO_HUB_URL")
	if u == "" {
		u = defaultHubURL
	}
	return strings.TrimRight(u, "/")
}

var version = "dev"

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "keyto:", err)
		os.Exit(1)
	}
}

func dispatch(args []string) error {
	if len(args) == 0 {
		ui.Banner(os.Stdout, ui.IsStdoutTTY(), ui.TermWidth(), false, version)
		printUsage()
		selfupdate.MaybeNotify(version, ui.IsStderrTTY(), os.Stderr)
		return nil
	}

	switch args[0] {
	case "help":
		ui.Banner(os.Stdout, ui.IsStdoutTTY(), ui.TermWidth(), false, version)
		printUsage()
		selfupdate.MaybeNotify(version, ui.IsStderrTTY(), os.Stderr)
		return nil
	case "auth":
		selfupdate.MaybeNotify(version, ui.IsStderrTTY(), os.Stderr)
		return runAuth(args[1:])
	case "start":
		// Banner + all interactive output go to stderr so stdout stays clean
		// for shell integration (which captures the resolved project dir).
		ui.Banner(os.Stderr, ui.IsStderrTTY(), ui.TermWidth(), false, version)
		selfupdate.MaybeNotify(version, ui.IsStderrTTY(), os.Stderr)
		return runStart(context.Background(), args[1:])
	case "update":
		return runUpdate()
	case "shell-init":
		return runShellInit(args[1:])
	case "credential":
		return runCredential(args)
	case "env":
		return runEnvDispatch(context.Background(), args[1:])
	case "dev":
		return runDev(context.Background(), args[1:])
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

// runStart implements `keyto start [project]`.
// It loads creds (nil if not authed — start.Run returns a helpful error in that
// case), builds the real Deps wrappers, and delegates to start.Run.
func runStart(ctx context.Context, args []string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git is required but was not found on PATH — install git and retry")
	}

	creds, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotAuthed) {
			creds = nil // start.Run will return the helpful "run keyto auth" error
		} else {
			return fmt.Errorf("start: load config: %w", err)
		}
	}

	// Friendly re-login (S4.3): a stored-but-expired credential would otherwise
	// fail mid-flow with a cryptic error. Catch it up front with a clear hint.
	if creds != nil && creds.Expired() {
		return fmt.Errorf("your sign-in has expired — run `keyto auth` to sign in again")
	}

	var hubClient *hub.Client
	if creds != nil {
		hubClient = &hub.Client{
			BaseURL:    creds.HubURL,
			Credential: creds.Credential,
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("start: get working directory: %w", err)
	}

	projectArg := ""
	if len(args) > 0 {
		projectArg = args[0]
	}

	d := start.Deps{
		Creds: creds,
		List: func(ctx context.Context) ([]hub.Project, error) {
			if hubClient == nil {
				return nil, fmt.Errorf("not authenticated")
			}
			return hubClient.ListProjects(ctx)
		},
		Clone: func(repoURL, dir string) error {
			hubForClone := ""
			if creds != nil {
				hubForClone = creds.HubURL
			}
			cmd := exec.CommandContext(ctx, "git", cloneArgs(hubForClone, repoURL, dir)...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		},
		Wire: func(dir string, m *project.Marker, email, name string) error {
			return gitwire.Wire(gitwire.RealRunner, dir, m, email, name)
		},
		ReadMarker:  project.Read,
		WriteMarker: project.Write,
		Cwd:         cwd,
		In:          os.Stdin,
		Out:         os.Stderr,
	}

	projectDir, err := start.Run(ctx, projectArg, d)
	if err != nil {
		return err
	}
	emitProjectDir(projectDir)
	return nil
}

// runEnvDispatch routes `keyto env <subcommand>` to the appropriate handler.
// Currently only "sync" is supported.
func runEnvDispatch(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "sync" {
		return runEnvSync(ctx, args[1:])
	}
	return fmt.Errorf("unknown env subcommand: %s — try `keyto env sync`", args[0])
}

// runEnvSync implements `keyto env sync [flags]`.
// It loads creds, builds the real Deps, and delegates to envsync.Run.
func runEnvSync(ctx context.Context, args []string) error {
	creds, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotAuthed) {
			creds = nil // envsync.Run returns the helpful "run keyto auth" error
		} else {
			return fmt.Errorf("env sync: load config: %w", err)
		}
	}

	if creds != nil && creds.Expired() {
		return fmt.Errorf("your sign-in has expired — run `keyto auth` to sign in again")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("env sync: get working directory: %w", err)
	}

	var fetcher envsync.HubFetcher
	if creds != nil {
		hubClient := &hub.Client{
			BaseURL:    creds.HubURL,
			Credential: creds.Credential,
		}
		fetcher = hubClient.FetchEnvValues
	}

	d := envsync.Deps{
		Creds: creds,
		Cwd:   cwd,
		Fetch: fetcher,
		Out:   os.Stdout,
	}

	return envsync.Run(ctx, args, d)
}

// runDevImpl implements `keyto dev`: env sync then docker compose up.
// The sync runs on the host first (it needs ~/.keyto/credentials and browser
// auth, so it cannot run inside a container). The documented two-step
// baseline is: keyto env sync && docker compose up
func runDevImpl(ctx context.Context, args []string) error {
	fmt.Fprintln(os.Stderr, "keyto dev: syncing env…")
	if err := runEnvSync(ctx, []string{}); err != nil {
		return fmt.Errorf("keyto dev: env sync: %w", err)
	}

	// `docker compose up` brings up the project's backing services (the profiles
	// already in COMPOSE_PROFILES). The app itself runs on the host via `npm run
	// dev` — the primary local-dev workflow. To containerise the app instead, run
	// `docker compose --profile app up` directly.
	fmt.Fprintln(os.Stderr, "keyto dev: starting docker compose…")
	composeArgs := append([]string{"compose", "up"}, args...)
	cmd := exec.CommandContext(ctx, "docker", composeArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keyto dev: docker compose up: %w", err)
	}
	return nil
}

// emitProjectDir reports the resolved project directory after `keyto start`.
// Under shell integration (KEYTO_SHELL_INTEGRATION=1, exported by the wrapper
// function) it prints ONLY the directory to stdout so the wrapper can cd into
// it — every other message went to stderr. Without integration it prints a
// copy-pasteable cd + next-step hint to stderr and leaves stdout clean.
func emitProjectDir(dir string) {
	if dir == "" {
		return
	}
	if os.Getenv("KEYTO_SHELL_INTEGRATION") == "1" {
		fmt.Println(dir) // stdout: the cd target for the shell wrapper
		fmt.Fprintln(os.Stderr, "\nStart your AI session:  claude")
		return
	}
	fmt.Fprintf(os.Stderr, "\nNext:\n  cd %s\n  claude        # start your AI dev session\n", dir)
}

// runShellInit prints the shell-integration wrapper function. With no argument
// it picks the wrapper for $SHELL (falling back to a POSIX function). Source it
// from your shell rc:  eval "$(keyto shell-init)"
func runShellInit(args []string) error {
	shell := ""
	if len(args) > 0 {
		shell = args[0]
	} else if s := os.Getenv("SHELL"); s != "" {
		shell = filepath.Base(s)
	}
	script, err := shellinit.Script(shell)
	if err != nil {
		return err
	}
	fmt.Print(script)
	return nil
}

// cloneArgs builds the `git clone` argument list, injecting the keyto credential
// helper inline so the clone itself can authenticate to the Hub git proxy.
//
// gitwire.Wire configures the helper only repo-locally — which is too late for
// the clone, since the repository does not exist yet. Without an inline helper,
// git falls back to its interactive "Username:" prompt. The helper config is
// scoped to the Hub host (matching Wire), and the helper additionally self-guards
// by host, so it never answers for unrelated remotes. The "-c" flag and its value
// must precede the "clone" subcommand.
func cloneArgs(hubURL, repoURL, dir string) []string {
	args := []string{}
	if hubURL != "" {
		args = append(args, "-c", fmt.Sprintf("credential.%s.helper=!keyto credential", hubURL))
	}
	return append(args, "clone", repoURL, dir)
}

// runCredential implements the git credential helper sub-command.
// git calls: keyto credential <op>  with attributes on stdin.
// We never print the banner — this path must be stdout-clean for git.
func runCredential(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("credential: missing operation (get/store/erase)")
	}
	op := args[1]

	// Load stored credentials.  ErrNotAuthed is non-fatal: we pass nil so the
	// helper emits nothing and defers to the next helper in the git chain.
	creds, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotAuthed) {
			creds = nil
		} else {
			return fmt.Errorf("credential: load config: %w", err)
		}
	}

	// Derive the Hub hostname from the stored HubURL.  If we have no creds the
	// host is "" which never matches any git request host.
	hubHost := ""
	if creds != nil && creds.HubURL != "" {
		if u, err := url.Parse(creds.HubURL); err == nil {
			hubHost = u.Host
		}
	}

	return credential.Helper(op, os.Stdin, os.Stdout, os.Stderr, creds, hubHost)
}

// runUpdate implements `keyto update`: fetch the latest release, verify it, and
// replace the running binary. It is a package var so the dispatch routing can
// be tested without performing a real network update.
var runUpdate = func() error {
	return selfupdate.Run(context.Background(), version, os.Stdout)
}

// runDev is a package var so dispatch routing can be tested without performing
// a real env sync or docker compose up.
var runDev = func(ctx context.Context, args []string) error {
	return runDevImpl(ctx, args)
}

// reuseCredential reports whether a stored credential makes a fresh login
// unnecessary: it is non-nil, not expired, for the same Hub, and --force was
// not given. Reusing it avoids minting a duplicate CLI token on every auth.
func reuseCredential(c *config.Creds, hub string, force bool) bool {
	if force || c == nil {
		return false
	}
	return !c.Expired() && c.HubURL == hub
}

// runAuth performs the full loopback + PKCE login, persists the credential,
// and prints a success message. If a valid credential for the same Hub is
// already stored it short-circuits (no new token is minted) unless --force is
// given — this stops repeated `keyto auth` from piling up CLI tokens.
func runAuth(args []string) error {
	force := false
	for _, a := range args {
		if a == "--force" || a == "-f" {
			force = true
		}
	}

	hub := hubURL()

	if existing, err := config.Load(); err == nil && reuseCredential(existing, hub, force) {
		fmt.Printf("Already authenticated as %s (%s).\n", existing.UserName, existing.UserEmail)
		if !existing.ExpiresAt.IsZero() {
			fmt.Printf("Credential valid until %s.\n", existing.ExpiresAt.Local().Format("2006-01-02 15:04 MST"))
		}
		fmt.Println("Re-authenticate (mint a new token) with: keyto auth --force")
		return nil
	}

	tr, err := auth.Run(context.Background(), auth.Options{
		HubURL:  hub,
		OpenURL: browser.OpenURL,
	})
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if err := config.Save(&config.Creds{
		Credential: tr.Credential,
		HubURL:     hub,
		UserEmail:  tr.UserEmail,
		UserName:   tr.UserName,
		ExpiresAt:  tr.ExpiresAt,
	}); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	fmt.Printf("Authenticated as %s (%s)\n", tr.UserName, tr.UserEmail)
	return nil
}

func printUsage() {
	fmt.Printf("keyto %s\n\n", version)
	fmt.Println("Usage: keyto <command> [args]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  auth        Authenticate with the Keyto Hub (reuses a valid credential; --force to re-issue)")
	fmt.Println("  start       Clone and wire a Keyto project")
	fmt.Println("  update      Update keyto to the latest release")
	fmt.Println("  shell-init  Print the shell integration snippet (eval \"$(keyto shell-init)\")")
	fmt.Println("  credential  Git credential helper")
	fmt.Println("  env sync    Sync UAT secrets into .env for local docker-compose dev")
	fmt.Println("              Flags: --env uat|prod  --out <file>  --print  --allow-prod")
	fmt.Println("  dev         env sync + docker compose up (requires docker)")
	fmt.Println("  help        Show this help message")
}
