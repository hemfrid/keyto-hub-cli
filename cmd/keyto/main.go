package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/hemfrid/keyto-cli/internal/auth"
	"github.com/hemfrid/keyto-cli/internal/browser"
	"github.com/hemfrid/keyto-cli/internal/config"
	"github.com/hemfrid/keyto-cli/internal/credential"
	"github.com/hemfrid/keyto-cli/internal/ui"
)

// defaultHubURL is the production Hub.  Override with KEYTO_HUB_URL.
const defaultHubURL = "https://hub.keytolabs.com"

// hubURL returns the Hub base URL, preferring the KEYTO_HUB_URL env var.
func hubURL() string {
	if u := os.Getenv("KEYTO_HUB_URL"); u != "" {
		return u
	}
	return defaultHubURL
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
		return errors.New("not implemented yet")
	case "credential":
		return runCredential(args)
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
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
