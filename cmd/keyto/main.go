package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/hemfrid/keyto-cli/internal/auth"
	"github.com/hemfrid/keyto-cli/internal/browser"
	"github.com/hemfrid/keyto-cli/internal/config"
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
		return errors.New("not implemented yet")
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
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
