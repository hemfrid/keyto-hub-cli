package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/hemfrid/keyto-cli/internal/ui"
)

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
		return errors.New("not implemented yet")
	case "start":
		return errors.New("not implemented yet")
	case "credential":
		return errors.New("not implemented yet")
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
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
