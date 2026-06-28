// Package envset implements `keyto env set` - upserting env vars in UAT/PROD
// via the Hub. It is the write counterpart to package envsync.
package envset

import (
	"context"
	"flag"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// Setter upserts values in env and returns the keys the Hub reports updated.
// It matches hub.Client.SetEnvValues so the real client can be passed directly.
type Setter func(ctx context.Context, org, repo, env string, values map[string]string) (updated []string, err error)

// Deps holds the injectable dependencies for Run.
type Deps struct {
	Creds   *config.Creds                                                       // nil -> not authenticated
	Cwd     string                                                              // dir containing .keyto/project.json
	Set     Setter                                                              // Hub write call
	Resolve func(ctx context.Context, app string) (org, repo string, err error) // map --app name -> org/repo
	Prompt  func(label string) (string, error)                                  // hidden value prompt (bare-key form)
	Confirm func(msg string) bool                                               // prod y/N; nil -> skip (non-TTY)
	Out     io.Writer                                                           // status line
}

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Run implements `keyto env set [flags] KEY=VALUE... | KEY`.
//
// Flags: --env uat|prod (default uat), --allow-prod (required for prod).
func Run(ctx context.Context, args []string, d Deps) error {
	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	targetEnv := fs.String("env", "uat", "target environment (uat|prod)")
	allowProd := fs.Bool("allow-prod", false, "required to use --env prod")
	app := fs.String("app", "", "target another project by name instead of the current checkout")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("env set: parse flags: %w", err)
	}

	if d.Creds == nil {
		return fmt.Errorf("not authenticated - run `keyto auth`")
	}

	env := *targetEnv
	if env != "uat" && env != "prod" {
		return fmt.Errorf("env set: --env must be uat or prod, got %q", env)
	}
	if env == "prod" && !*allowProd {
		return fmt.Errorf("env set: --env prod requires --allow-prod (writing production secrets)")
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("env set: nothing to set - usage: keyto env set KEY=VALUE [KEY2=VALUE2 ...]")
	}

	values, err := parseAssignments(rest, d.Prompt)
	if err != nil {
		return err
	}

	// Resolve the target project. --app <name> targets another project (looked
	// up by name via Resolve) and works from anywhere; without it we read the
	// .keyto/project.json marker of the current checkout.
	var org, repo string
	if *app != "" {
		if d.Resolve == nil {
			return fmt.Errorf("env set: --app needs authentication - run `keyto auth`")
		}
		org, repo, err = d.Resolve(ctx, *app)
		if err != nil {
			return err
		}
	} else {
		marker, merr := project.Read(d.Cwd)
		if merr != nil {
			return fmt.Errorf("env set: read project marker: %w", merr)
		}
		if marker == nil {
			return fmt.Errorf("env set: no .keyto/project.json found - run `keyto checkout` first, or pass --app <name>")
		}
		org, repo = marker.Org, marker.Repo
	}

	keys := sortedKeys(values)

	if env == "prod" && d.Confirm != nil {
		msg := fmt.Sprintf("About to set %d key(s) in PROD: %s\nContinue? [y/N] ", len(keys), strings.Join(keys, ", "))
		if !d.Confirm(msg) {
			return fmt.Errorf("env set: aborted")
		}
	}

	if _, err := d.Set(ctx, org, repo, env, values); err != nil {
		return fmt.Errorf("env set: %w", err)
	}

	if d.Out != nil {
		fmt.Fprintf(d.Out, "set %d key(s) in %s: %s\n", len(keys), env, strings.Join(keys, ", "))
	}
	return nil
}

// parseAssignments turns args into a key→value map. Args are either all
// KEY=VALUE pairs, or exactly one bare KEY (value read via prompt). Mixing the
// two, or more than one bare key, is an error. Values keep any '=' after the
// first. Keys must match keyRe.
func parseAssignments(args []string, prompt func(string) (string, error)) (map[string]string, error) {
	var bare []string
	hasPair := false
	for _, a := range args {
		if strings.Contains(a, "=") {
			hasPair = true
		} else {
			bare = append(bare, a)
		}
	}
	if len(bare) > 0 && hasPair {
		return nil, fmt.Errorf("env set: cannot mix KEY=VALUE pairs with a bare KEY - pass either all assignments or a single key to be prompted")
	}
	if len(bare) > 1 {
		return nil, fmt.Errorf("env set: only one key may be set via prompt at a time - use KEY=VALUE for several")
	}

	values := map[string]string{}
	if len(bare) == 1 {
		key := bare[0]
		if !keyRe.MatchString(key) {
			return nil, fmt.Errorf("env set: invalid key name %q", key)
		}
		if prompt == nil {
			return nil, fmt.Errorf("env set: no value given for %s and no prompt available", key)
		}
		val, err := prompt(fmt.Sprintf("Value for %s: ", key))
		if err != nil {
			return nil, fmt.Errorf("env set: read value: %w", err)
		}
		values[key] = val
		return values, nil
	}

	for _, a := range args {
		key, val, _ := strings.Cut(a, "=")
		if !keyRe.MatchString(key) {
			return nil, fmt.Errorf("env set: invalid key name %q", key)
		}
		values[key] = val
	}
	return values, nil
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
