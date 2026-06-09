package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/hemfrid/keyto-hub-cli/internal/auth"
	"github.com/hemfrid/keyto-hub-cli/internal/browser"
	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/credential"
	"github.com/hemfrid/keyto-hub-cli/internal/gitwire"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
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
		return nil
	}

	switch args[0] {
	case "help":
		ui.Banner(os.Stdout, ui.IsStdoutTTY(), ui.TermWidth(), false, version)
		printUsage()
		return nil
	case "auth":
		return runAuth()
	case "start":
		ui.Banner(os.Stdout, ui.IsStdoutTTY(), ui.TermWidth(), false, version)
		return runStart(context.Background(), args[1:])
	case "credential":
		return runCredential(args)
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
			cmd := exec.CommandContext(ctx, "git", "clone", repoURL, dir)
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
		Out:         os.Stdout,
	}

	return start.Run(ctx, projectArg, d)
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

	return credential.Helper(op, os.Stdin, os.Stdout, creds, hubHost)
}

// runAuth performs the full loopback + PKCE login, persists the credential,
// and prints a success message.
func runAuth() error {
	hub := hubURL()
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
	fmt.Println("  auth        Authenticate with the Keyto Hub")
	fmt.Println("  start       Clone and wire a Keyto project")
	fmt.Println("  credential  Git credential helper")
	fmt.Println("  help        Show this help message")
}
