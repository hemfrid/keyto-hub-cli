package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/npmrc"
)

// ensureNpmLane points @hemfrid at the Hub's package lane in the user's npm
// config (internal/npmrc), for `keyto start` on a Workers checkout. creds is
// the credential `keyto start` threads through after a 401 re-auth; nil loads
// the stored one. The returned error is a tip for the user, never fatal.
func ensureNpmLane(ctx context.Context, creds *config.Creds, out io.Writer) error {
	if creds == nil {
		loaded, err := config.Load()
		if err != nil && !errors.Is(err, config.ErrNotAuthed) {
			return fmt.Errorf("load credentials: %w; ~/.npmrc left unchanged", err)
		}
		creds = loaded
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find your home directory: %w; ~/.npmrc left unchanged", err)
	}
	d := npmrc.Deps{
		Path:  npmrc.UserConfigPath(os.Getenv, home),
		Creds: creds,
		Out:   out,
	}
	if creds != nil {
		probe := &hub.Client{BaseURL: creds.HubURL} // no Credential: the probe asks unauthenticated
		d.Probe = probe.NpmLaneServed
	}
	return npmrc.Ensure(ctx, d)
}
