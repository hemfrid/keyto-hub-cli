package main

import (
	"context"
	"fmt"
	"io"
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
	writeAIInit(os.Stderr, res)
	return nil
}

func writeAIInit(w io.Writer, res *ai.InitResult) {
	fmt.Fprintf(w, "Installed AI capabilities bundle %s: %d files written", res.Tag, len(res.Written))
	if len(res.Skipped) > 0 {
		fmt.Fprintf(w, ", %d existing files left untouched:\n", len(res.Skipped))
		for _, p := range res.Skipped {
			fmt.Fprintf(w, "  kept: %s\n", bundlePath(p))
		}
	} else {
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "\nNext:")
	fmt.Fprintln(w, "  1. Review the diff (git status / git diff), then commit it.")
	fmt.Fprintln(w, "  2. Open Claude Code in this repo and run /setup to adapt the")
	fmt.Fprintln(w, "     capabilities to this project (stack, commands, tracker).")
}

func runAIUpdate(ctx context.Context, root string, deps ai.Deps) error {
	res, err := ai.Update(ctx, root, deps)
	if err != nil {
		return err
	}
	writeAIUpdate(os.Stderr, res)
	return nil
}

func writeAIUpdate(w io.Writer, res *ai.UpdateResult) {
	if res.UpToDate {
		fmt.Fprintf(w, "Up to date (%s).\n", res.Tag)
		return
	}
	fmt.Fprintf(w, "Updated AI capabilities bundle %s -> %s\n", res.FromTag, res.Tag)
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
			fmt.Fprintf(w, "  %s: %s\n", r.label, bundlePath(p))
		}
	}
	fmt.Fprintln(w, "\nReview the diff, then commit it.")
}

func runAIStatus(ctx context.Context, root string, deps ai.Deps) error {
	st, err := ai.GetStatus(ctx, root, deps)
	if err != nil {
		return err
	}
	writeAIStatus(os.Stderr, st)
	return nil
}

func writeAIStatus(w io.Writer, st *ai.Status) {
	if st.UpToDate {
		fmt.Fprintf(w, "Bundle %s — up to date.\n", st.InstalledTag)
	} else {
		fmt.Fprintf(w, "Bundle %s — newer release available: %s. Run 'keyto ai update'.\n", st.InstalledTag, st.LatestTag)
	}
	for _, p := range st.Modified {
		fmt.Fprintf(w, "  locally modified: %s\n", bundlePath(p))
	}
	for _, p := range st.Missing {
		fmt.Fprintf(w, "  locally deleted:  %s\n", bundlePath(p))
	}
}

// bundlePath is a bundle file path safe to print. The paths come from the
// Hub-relayed manifest and from the pin file; the tarball check refuses one
// that escapes the repo, not one that carries terminal control characters.
func bundlePath(p string) string { return sanitizeForTerminal(p, false) }
