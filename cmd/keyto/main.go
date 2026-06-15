package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hemfrid/keyto-hub-cli/internal/auth"
	"github.com/hemfrid/keyto-hub-cli/internal/boot"
	"github.com/hemfrid/keyto-hub-cli/internal/browser"
	"github.com/hemfrid/keyto-hub-cli/internal/checkout"
	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/credential"
	"github.com/hemfrid/keyto-hub-cli/internal/envsync"
	"github.com/hemfrid/keyto-hub-cli/internal/gitwire"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/prereq"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
	"github.com/hemfrid/keyto-hub-cli/internal/selfupdate"
	"github.com/hemfrid/keyto-hub-cli/internal/shellinit"
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

// authRun is a seam over auth.Run so tests can stub the browser login flow.
var authRun = auth.Run

// checkoutRun is a seam over checkout.Run so the auto-reauth retry path can be
// tested without a real clone or network call.
var checkoutRun = checkout.Run

// bootRun is a seam over boot.Run so the auto-reauth retry path can be tested
// without running a real local boot loop.
var bootRun = boot.Run

// isAuthError reports whether err looks like a server-side 401/Unauthorized.
// The Hub client surfaces a non-200 status as `fmt.Errorf("...: %s", resp.Status)`,
// so the error string contains "401" / "Unauthorized" — we match on that.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "401") || strings.Contains(strings.ToLower(msg), "unauthorized")
}

// reauth re-runs the browser login flow (always minting a fresh credential),
// persists it, and returns the new creds. It is the single recovery path used
// both up front (nil/expired creds) and on a server-side 401 during a command.
// It is a package var so tests can stub it without launching a real browser.
var reauth = func(ctx context.Context) (*config.Creds, error) {
	fmt.Fprintln(os.Stderr, "your sign-in is no longer valid — re-authenticating in your browser…")
	hub := hubURL()
	tr, err := authRun(ctx, auth.Options{
		HubURL:  hub,
		OpenURL: browser.OpenURL,
	})
	if err != nil {
		return nil, fmt.Errorf("re-authentication failed: %w", err)
	}
	creds := &config.Creds{
		Credential: tr.Credential,
		HubURL:     hub,
		UserEmail:  tr.UserEmail,
		UserName:   tr.UserName,
		ExpiresAt:  tr.ExpiresAt,
	}
	if err := config.Save(creds); err != nil {
		return nil, fmt.Errorf("save credentials: %w", err)
	}
	return creds, nil
}

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
	case "checkout":
		// Banner + all interactive output go to stderr so stdout stays clean
		// for shell integration (which captures the resolved project dir).
		ui.Banner(os.Stderr, ui.IsStderrTTY(), ui.TermWidth(), false, version)
		selfupdate.MaybeNotify(version, ui.IsStderrTTY(), os.Stderr)
		return runCheckout(context.Background(), args[1:])
	case "start":
		// Banner + the self-update nudge go to stderr so a foreground boot
		// (npm run dev) keeps stdout clean. The router decides boot-vs-checkout.
		ui.Banner(os.Stderr, ui.IsStderrTTY(), ui.TermWidth(), false, version)
		selfupdate.MaybeNotify(version, ui.IsStderrTTY(), os.Stderr)
		return runStartRouter(context.Background(), args[1:])
	case "doctor":
		return runDoctor(context.Background(), args[1:])
	case "update":
		return runUpdate()
	case "shell-init":
		return runShellInit(args[1:])
	case "credential":
		return runCredential(args)
	case "env":
		return runEnvDispatch(context.Background(), args[1:])
	case "ai":
		selfupdate.MaybeNotify(version, ui.IsStderrTTY(), os.Stderr)
		return runAIDispatch(context.Background(), args[1:])
	case "dev":
		fmt.Fprintln(os.Stderr, "note: 'keyto dev' is deprecated — use 'keyto start'. Compose passthrough args are ignored.")
		return runBoot(context.Background(), nil)
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

// runCheckout is a package var so dispatch routing can be tested without
// performing a real clone or network call.
var runCheckout = func(ctx context.Context, args []string) error {
	return runCheckoutImpl(ctx, args)
}

// runCheckoutImpl implements `keyto checkout [project]`.
// It loads creds (nil/expired → re-auth up front), builds the Deps wrappers,
// and delegates to checkout.Run. If the server rejects the credential with a
// 401 mid-flow it re-auths in the browser and retries the run exactly once.
func runCheckoutImpl(ctx context.Context, args []string) error {
	// checkout needs git; guide the install (consent-gated) rather than just
	// erroring on a missing binary. AutoYes:false — checkout is interactive.
	if err := prereq.Ensure(ctx, []prereq.Tool{prereq.Git},
		prereq.Opts{Deps: realPrereqDeps(ctx), AutoYes: false}); err != nil {
		return err
	}

	creds, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotAuthed) {
			creds = nil // checkout.Run will return the helpful "run keyto auth" error
		} else {
			return fmt.Errorf("checkout: load config: %w", err)
		}
	}

	// Self-healing re-login (S4.3 / v0.3.2): a missing or stored-but-expired
	// credential would otherwise fail mid-flow with a cryptic error. Re-auth in
	// the browser up front so the user never has to run `keyto auth --force`.
	if creds == nil || creds.Expired() {
		creds, err = reauth(ctx)
		if err != nil {
			return err
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("checkout: get working directory: %w", err)
	}

	projectArg := ""
	if len(args) > 0 {
		projectArg = args[0]
	}

	projectDir, err := checkoutRun(ctx, projectArg, checkoutDeps(ctx, creds, cwd))
	if isAuthError(err) {
		// The credential looked valid locally but the server rejected it
		// (revoked/stale). Re-auth in the browser and retry exactly once.
		creds, err = reauth(ctx)
		if err != nil {
			return err
		}
		projectDir, err = checkoutRun(ctx, projectArg, checkoutDeps(ctx, creds, cwd))
	}
	if err != nil {
		return err
	}
	emitProjectDir(projectDir)
	// Non-blocking heads-up: `keyto start` will also need Docker + Node 20.
	// Detect-only here — never install or block during checkout.
	if tip := startPrereqTip(realPrereqDeps(ctx)); tip != "" {
		fmt.Fprintln(os.Stderr, tip)
	}
	return nil
}

// checkoutDeps builds the checkout.Deps wired to real os/exec/git
// implementations for the given creds. It is extracted so the deps can be
// rebuilt with a freshly-minted credential on the 401 retry path.
func checkoutDeps(ctx context.Context, creds *config.Creds, cwd string) checkout.Deps {
	var hubClient *hub.Client
	if creds != nil {
		hubClient = &hub.Client{
			BaseURL:    creds.HubURL,
			Credential: creds.Credential,
		}
	}
	return checkout.Deps{
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
}

// startPrereqTip detects (without installing or blocking) whether Docker and a
// supported Node are present and returns a one-line heads-up for `keyto start`.
// Returns "" when both are already satisfied. Detect-only: it must never mutate
// the machine or fail the checkout. Takes prereq.Deps so it's unit-testable.
func startPrereqTip(deps prereq.Deps) string {
	var missing []string
	if !deps.HasCommand("docker") {
		missing = append(missing, "Docker")
	}
	if v, err := deps.Version("node"); err != nil || !deps.HasCommand("node") || !nodeVersionTipOK(v) {
		missing = append(missing, "Node 20")
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("tip: `keyto start` will also need Docker + Node 20 — missing: %s (run `keyto start` to install)", strings.Join(missing, ", "))
}

// nodeVersionTipOK is the detect-only Node-20 check used by the checkout
// heads-up. It accepts v20.x and is deliberately lenient (a heads-up, not a
// gate): `keyto start`'s prereq.Ensure does the authoritative >=20.9 <21 check.
func nodeVersionTipOK(v string) bool {
	return strings.HasPrefix(strings.TrimSpace(v), "v20.") || strings.HasPrefix(strings.TrimSpace(v), "20.")
}

// readMarker reads the keyto project marker from dir. It is a package var so
// the start router's boot-vs-checkout decision can be tested with a fake.
var readMarker = func(dir string) (*project.Marker, error) { return project.Read(dir) }

// runBoot is a package var so dispatch/router routing can be tested without
// running a real local boot loop (prereqs, compose, npm).
var runBoot = func(ctx context.Context, args []string) error { return runBootImpl(ctx, args) }

// runStartRouter decides what `keyto start [name]` does now that start means
// "boot the current project". With no name: boot when we're in a project, else
// fall back (with a deprecation note) to the checkout picker. With a name:
// boot only when it matches the current project's marker; otherwise treat it as
// the old `start <name>` clone request and run checkout (with a note).
func runStartRouter(ctx context.Context, args []string) error {
	name := ""
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			name = a
			break
		}
	}
	cwd, _ := os.Getwd()
	m, _ := readMarker(cwd)
	if name == "" {
		if m != nil {
			return runBoot(ctx, args)
		}
		fmt.Fprintln(os.Stderr, "note: 'keyto start' now boots the current project; clone with 'keyto checkout'. Running checkout for you this time.")
		return runCheckout(ctx, args)
	}
	if m != nil && m.Name == name {
		return runBoot(ctx, args)
	}
	fmt.Fprintln(os.Stderr, "note: 'keyto start <name>' is now 'keyto checkout <name>'. Running checkout for you this time.")
	return runCheckout(ctx, args)
}

// runBootImpl implements `keyto start`: the one-command local boot loop. It
// parses the start flags, builds boot.Deps from real os/exec implementations,
// and delegates the orchestration to boot.Run.
func runBootImpl(ctx context.Context, args []string) error {
	// A stale shell-integration wrapper captured stdout (for the old `start`
	// cd-handoff) and would swallow the foreground `npm run dev` output. Nudge
	// the user to refresh it. checkout still emits the cd target on stdout.
	if os.Getenv("KEYTO_SHELL_INTEGRATION") == "1" {
		fmt.Fprintln(os.Stderr, `note: your shell integration is outdated — run 'eval "$(keyto shell-init)"' (or reinstall) so 'keyto start' streams output; 'keyto checkout' is now the clone command.`)
	}

	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	noSync := fs.Bool("no-sync", false, "reuse the existing .env instead of running env sync")
	noMigrate := fs.Bool("no-migrate", false, "skip applying migrations")
	noInstall := fs.Bool("no-install", false, "skip npm install even when node_modules is absent")
	yes := fs.Bool("yes", false, "auto-confirm prerequisite installs")
	noOpen := fs.Bool("no-open", false, "do not open the app in the browser when it's ready")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("start: parse flags: %w", err)
	}
	flags := boot.Flags{
		NoSync:    *noSync,
		NoMigrate: *noMigrate,
		NoInstall: *noInstall,
		Yes:       *yes,
		NoOpen:    *noOpen,
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("start: get working directory: %w", err)
	}

	// First run uses creds==nil — the env-sync step loads them from disk as it
	// did before. If the Hub rejects the credential with a 401 (the only
	// network-touching step in boot), re-auth in the browser and retry with the
	// freshly-minted token threaded through, exactly once.
	err = bootRun(ctx, bootDeps(ctx, cwd, flags, nil), flags)
	if isAuthError(err) {
		creds, rerr := reauth(ctx)
		if rerr != nil {
			return rerr
		}
		err = bootRun(ctx, bootDeps(ctx, cwd, flags, creds), flags)
	}
	return err
}

// bootDeps builds the boot.Deps wired to real os/exec implementations for cwd.
// It is extracted so the deps can be rebuilt with a freshly-minted credential
// on the 401 retry path. When creds is nil the env-sync step loads them from
// disk (the original behaviour); on retry the minted token is threaded through.
func bootDeps(ctx context.Context, cwd string, flags boot.Flags, creds *config.Creds) boot.Deps {
	return boot.Deps{
		HasMarker: func() bool {
			m, _ := readMarker(cwd)
			return m != nil
		},
		Scripts: func() (map[string]string, error) {
			return readPackageScripts(cwd)
		},
		EnsurePrereqs: func(ctx context.Context) error {
			deps := realPrereqDeps(ctx)
			// start runs `docker compose up`, so the Compose v2 plugin is a
			// hard prerequisite alongside the docker engine itself.
			if err := prereq.Ensure(ctx,
				[]prereq.Tool{prereq.Git, prereq.Docker, prereq.DockerCompose, prereq.Node},
				prereq.Opts{Deps: deps, AutoYes: flags.Yes}); err != nil {
				return err
			}
			// Non-fatal Linux advisory: a low inotify watch limit makes
			// `next dev` hot reload fail with ENOSPC. Print and continue.
			if w := (prereq.Opts{Deps: deps}).InotifyWarning(os.ReadFile); w != "" {
				fmt.Fprintln(os.Stderr, w)
			}
			return nil
		},
		EnvSync:       func(ctx context.Context) error { return runEnvSyncWithCreds(ctx, nil, creds) },
		EnvFileExists: func() bool { return fileExists(filepath.Join(cwd, ".env")) },
		HasCompose: func() bool {
			return fileExists(filepath.Join(cwd, "docker-compose.yml")) ||
				fileExists(filepath.Join(cwd, "docker-compose.yaml"))
		},
		ComposeUp: func(ctx context.Context) error {
			cmd := exec.CommandContext(ctx, "docker", "compose", "up", "-d", "--wait")
			cmd.Dir = cwd
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr
			return cmd.Run()
		},
		DBRunning:          func(ctx context.Context) bool { return composeDBRunning(ctx, cwd) },
		NodeModulesPresent: func() bool { return dirExists(filepath.Join(cwd, "node_modules")) },
		Install:            func(ctx context.Context) error { return npmInstall(ctx, cwd) },
		RunScript:          func(ctx context.Context, script string) error { return runNpmScript(ctx, cwd, script) },
		// nil on CI / non-interactive shells (browserOpener returns nil) — boot
		// then skips the open. --no-open is handled separately via flags.NoOpen.
		OpenApp: browserOpener(),
		Out:     os.Stderr,
	}
}

// realPrereqDeps builds the prereq.Deps wired to real os/exec implementations.
func realPrereqDeps(ctx context.Context) prereq.Deps {
	return prereq.Deps{
		OS:         runtime.GOOS,
		HasCommand: func(name string) bool { _, err := exec.LookPath(name); return err == nil },
		Version: func(name string) (string, error) {
			out, err := exec.CommandContext(ctx, name, "--version").Output()
			return strings.TrimSpace(string(out)), err
		},
		DaemonUp: func(ctx context.Context) bool {
			return exec.CommandContext(ctx, "docker", "info").Run() == nil
		},
		ComposeOK: func(ctx context.Context) bool {
			return exec.CommandContext(ctx, "docker", "compose", "version").Run() == nil
		},
		VirtualizationOK: prereq.VirtualizationOK,
		Prompt:           promptYesNo,
		Run: func(ctx context.Context, name string, args ...string) error {
			c := exec.CommandContext(ctx, name, args...)
			c.Stdout = os.Stderr
			c.Stderr = os.Stderr
			c.Stdin = os.Stdin
			return c.Run()
		},
		Out: os.Stderr,
	}
}

// promptYesNo reads a y/N answer from stdin. It is TTY-gated: on a non-interactive
// stdin it returns false (decline) so an unattended run never blocks or
// auto-mutates the machine — pass --yes to opt in instead.
func promptYesNo(question string) bool {
	if !ui.IsStdinTTY() {
		return false
	}
	fmt.Fprint(os.Stderr, question)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes"
}

// readPackageScripts reads <dir>/package.json and returns its "scripts" object
// as a name→command map. A missing package.json yields an empty map (boot.Run
// reports the "no dev script" error), not an I/O failure.
func readPackageScripts(dir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, err
	}
	if pkg.Scripts == nil {
		return map[string]string{}, nil
	}
	return pkg.Scripts, nil
}

// composeDBRunning reports whether a postgres or mysql service is currently up
// under docker compose in dir. It is best-effort: any docker error means "no DB"
// so migrations are skipped rather than run against an unreachable database.
func composeDBRunning(ctx context.Context, dir string) bool {
	cmd := exec.CommandContext(ctx, "docker", "compose", "ps", "--status", "running", "--services")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch strings.TrimSpace(line) {
		case "postgres", "mysql":
			return true
		}
	}
	return false
}

// npmInstall runs `npm ci` when a lockfile is present (reproducible install),
// else `npm install`. Output streams to stderr to keep stdout clean.
func npmInstall(ctx context.Context, dir string) error {
	sub := "install"
	if fileExists(filepath.Join(dir, "package-lock.json")) {
		sub = "ci"
	}
	cmd := exec.CommandContext(ctx, "npm", sub)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runNpmScript runs `npm run <script>` in dir. The "dev" script runs in the
// foreground with stdin/stdout/stderr inherited (it is the app the user
// interacts with). Every other script (e.g. "migrate") is captured so its
// output can be folded into the wrapped error — boot.Run greps the message for
// "connect" to distinguish a DB-connection failure from a schema failure.
func runNpmScript(ctx context.Context, dir, script string) error {
	cmd := exec.CommandContext(ctx, "npm", "run", script)
	cmd.Dir = dir
	if script == "dev" {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// fileExists reports whether path exists and is a regular file (or any
// non-directory). dirExists reports whether path exists and is a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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
// It loads creds from disk, builds the real Deps, and delegates to envsync.Run.
func runEnvSync(ctx context.Context, args []string) error {
	return runEnvSyncWithCreds(ctx, args, nil)
}

// runEnvSyncWithCreds is the credential-injecting form of runEnvSync. When
// creds is nil it loads them from disk (the standalone `keyto env sync` path);
// when non-nil it uses the supplied credential — this is how `keyto start`
// threads a freshly-minted token through after a 401 re-auth without round-tripping
// back to disk.
func runEnvSyncWithCreds(ctx context.Context, args []string, creds *config.Creds) error {
	if creds == nil {
		loaded, err := config.Load()
		if err != nil {
			if errors.Is(err, config.ErrNotAuthed) {
				loaded = nil // envsync.Run returns the helpful "run keyto auth" error
			} else {
				return fmt.Errorf("env sync: load config: %w", err)
			}
		}
		if loaded != nil && loaded.Expired() {
			return fmt.Errorf("your sign-in has expired — run `keyto auth` to sign in again")
		}
		creds = loaded
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

// emitProjectDir reports the resolved project directory after `keyto checkout`.
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
	fmt.Println("  checkout    Clone and wire a Keyto project, then cd into it")
	fmt.Println("  start       Boot the current project locally (prereqs, env sync, compose, migrate, npm run dev)")
	fmt.Println("              Flags: --no-sync  --no-migrate  --no-install  --yes")
	fmt.Println("  doctor      Diagnose local prerequisites (git, Docker, Node) and print how to fix them")
	fmt.Println("              Flags: --json (machine output)  --fix (install fixable ones, consent-gated)")
	fmt.Println("                     --report/--no-report (upload the report to the Hub; default-on when authed)")
	fmt.Println("  update      Update keyto to the latest release")
	fmt.Println("  shell-init  Print the shell integration snippet (eval \"$(keyto shell-init)\")")
	fmt.Println("  credential  Git credential helper")
	fmt.Println("  env sync    Sync UAT secrets into .env for local docker-compose dev")
	fmt.Println("              Flags: --env uat|prod  --out <file>  --print  --allow-prod")
	fmt.Println("  ai [init|update|status]   Install / update the AI capabilities bundle in this repo")
	fmt.Println("  dev         Deprecated alias for `keyto start`")
	fmt.Println("  help        Show this help message")
}
