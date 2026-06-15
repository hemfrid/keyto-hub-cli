package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hemfrid/keyto-hub-cli/internal/ai"
	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

// runAIDispatch routes `keyto ai [init|update|status]` to the appropriate
// handler. It loads creds from disk (same pattern as runEnvDispatch), builds a
// hub.Client, resolves the git root, and delegates to the internal/ai package.
func runAIDispatch(ctx context.Context, args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}

	creds, err := config.Load()
	if err != nil {
		return err // config.ErrNotAuthed already carries "run 'keyto auth'"
	}
	client := &hub.Client{BaseURL: creds.HubURL, Credential: creds.Credential}
	root, err := ai.GitRoot(".")
	if err != nil {
		return err
	}
	deps := ai.Deps{
		Meta:    client.AIBundleMeta,
		Tarball: client.AIBundleTarball,
		HubURL:  creds.HubURL,
	}

	switch sub {
	case "init":
		return runAIInit(ctx, root, deps)
	case "update":
		return runAIUpdate(ctx, root, deps)
	case "status":
		return runAIStatus(ctx, root, deps)
	default:
		return fmt.Errorf("unknown ai subcommand: %s — try `keyto ai init|update|status`", sub)
	}
}

func runAIInit(ctx context.Context, root string, deps ai.Deps) error {
	res, err := ai.Init(ctx, root, deps)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Installed AI capabilities bundle %s: %d files written", res.Tag, len(res.Written))
	if len(res.Skipped) > 0 {
		fmt.Fprintf(os.Stderr, ", %d existing files left untouched:\n", len(res.Skipped))
		for _, p := range res.Skipped {
			fmt.Fprintf(os.Stderr, "  kept: %s\n", p)
		}
	} else {
		fmt.Fprintln(os.Stderr)
	}
	fmt.Fprintln(os.Stderr, "\nNext:")
	fmt.Fprintln(os.Stderr, "  1. Review the diff (git status / git diff), then commit it.")
	fmt.Fprintln(os.Stderr, "  2. Open Claude Code in this repo and run /setup to adapt the")
	fmt.Fprintln(os.Stderr, "     capabilities to this project (stack, commands, tracker).")
	return nil
}

func runAIUpdate(ctx context.Context, root string, deps ai.Deps) error {
	res, err := ai.Update(ctx, root, deps)
	if err != nil {
		return err
	}
	if res.UpToDate {
		fmt.Fprintf(os.Stderr, "Up to date (%s).\n", res.Tag)
		return nil
	}
	fmt.Fprintf(os.Stderr, "Updated AI capabilities bundle %s -> %s\n", res.FromTag, res.Tag)
	report := []struct {
		label string
		paths []string
	}{
		{"updated", res.Updated},
		{"added", res.Added},
		{"skipped (locally modified)", res.SkippedModified},
		{"skipped (pre-existing local file)", res.SkippedExisting},
		{"locally deleted (not restored)", res.MissingLocal},
		{"removed (gone upstream)", res.RemovedUpstream},
		{"kept (gone upstream, locally modified — now project-owned)", res.KeptModified},
	}
	for _, r := range report {
		for _, p := range r.paths {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", r.label, p)
		}
	}
	fmt.Fprintln(os.Stderr, "\nReview the diff, then commit it.")
	return nil
}

func runAIStatus(ctx context.Context, root string, deps ai.Deps) error {
	st, err := ai.GetStatus(ctx, root, deps)
	if err != nil {
		return err
	}
	if st.UpToDate {
		fmt.Fprintf(os.Stderr, "Bundle %s — up to date.\n", st.InstalledTag)
	} else {
		fmt.Fprintf(os.Stderr, "Bundle %s — newer release available: %s. Run 'keyto ai update'.\n", st.InstalledTag, st.LatestTag)
	}
	for _, p := range st.Modified {
		fmt.Fprintf(os.Stderr, "  locally modified: %s\n", p)
	}
	for _, p := range st.Missing {
		fmt.Fprintf(os.Stderr, "  locally deleted:  %s\n", p)
	}
	return nil
}
