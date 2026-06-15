// Package prereq detects local-dev prerequisites (git, docker, node) and,
// with the user's consent, installs missing ones via the platform-appropriate
// method. All OS/exec interaction is behind injected funcs so it is fully
// unit-testable. Never mutates the machine without a 'y'. Reused by `keyto
// start` (defensive preflight) and the future `keyto setup`.
package prereq

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

type Tool int

const (
	Git Tool = iota
	Docker
	// DockerCompose is the Compose v2 plugin (`docker compose`), which is
	// separate from the `docker` binary — a machine can have the engine but
	// lack the plugin. `keyto start` runs `docker compose up`, so it needs both.
	DockerCompose
	Node
)

func (t Tool) name() string {
	switch t {
	case Git:
		return "git"
	case Docker:
		return "docker"
	case DockerCompose:
		return "docker compose"
	case Node:
		return "node"
	}
	return "?"
}

// nodeMin mirrors the template package.json engines floor (>=20.9). There is no
// upper bound: any newer Node major (22, 24, …) is accepted, so the gate keeps
// pace with modern LTS lines without a re-bump each release. Keep the template
// package.json engines in sync (drop its `<21` cap) or `npm install` inside a
// booted project will warn with EBADENGINE.
const (
	nodeMinMajor = 20
	nodeMinMinor = 9
)

// Deps are the injected seams. main.go wires real implementations.
type Deps struct {
	OS         string                            // runtime.GOOS
	HasCommand func(name string) bool            // exec.LookPath != nil
	Version    func(name string) (string, error) // e.g. `node --version` -> "v20.20.2"
	DaemonUp   func(ctx context.Context) bool    // `docker info` succeeds
	ComposeOK  func(ctx context.Context) bool    // `docker compose version` exits 0
	// VirtualizationOK reports whether CPU virtualization is enabled. Consulted
	// only on Windows (Diagnose) to distinguish a daemon that's down because the
	// machine can't run Docker Desktop/WSL2 (virtualization disabled in BIOS/UEFI)
	// from a daemon that's merely not started. The real impl shells out to
	// PowerShell; on non-windows it is never called and may be nil.
	VirtualizationOK func(ctx context.Context) bool
	Prompt           func(question string) bool // y/N consent (false on non-TTY unless AutoYes)
	Run              func(ctx context.Context, name string, args ...string) error
	Out              io.Writer
}

// Opts wraps Deps + behavior flags.
type Opts struct {
	Deps
	AutoYes bool // --yes: auto-confirm installs
}

// Ensure verifies each requested tool, installing missing ones with consent.
func Ensure(ctx context.Context, want []Tool, o Opts) error {
	for _, t := range want {
		if err := ensureOne(ctx, t, o); err != nil {
			return err
		}
	}
	return nil
}

func ensureOne(ctx context.Context, t Tool, o Opts) error {
	switch t {
	case Node:
		if o.HasCommand("node") {
			v, _ := o.Version("node")
			if nodeVersionOK(v) {
				return nil
			}
			return fmt.Errorf("node %s is out of range — keyto needs Node >=%d.%d. Fix with `nvm install 24 && nvm use 24` (or `brew install node@24`), then re-run",
				v, nodeMinMajor, nodeMinMinor)
		}
	case Docker:
		if o.HasCommand("docker") {
			if o.DaemonUp(ctx) {
				return nil
			}
			return o.dockerDaemonDown(ctx)
		}
	case DockerCompose:
		// The Compose v2 plugin lives inside docker. If the docker binary is
		// missing we let the Docker tool report that (start lists Docker first);
		// don't duplicate the "install docker" error here.
		if !o.HasCommand("docker") {
			return nil
		}
		// The daemon being down is a Docker concern, not a Compose-plugin one —
		// `docker compose version` answers "is the plugin installed?" without a
		// running daemon. Trust the seam.
		if o.ComposeOK(ctx) {
			return nil
		}
		return o.composeMissing()
	default: // Git
		if o.HasCommand(t.name()) {
			return nil
		}
	}
	return o.install(ctx, t)
}

// inotifyMinWatches is the floor below which `next dev`'s file watcher tends to
// hit ENOSPC ("System limit for number of file watchers reached") on Linux. The
// suggested fix raises the limit well above this.
const (
	inotifyMinWatches         = 65536
	inotifyWatchesPath        = "/proc/sys/fs/inotify/max_user_watches"
	inotifyRecommendedWatches = 524288
)

// InotifyWarning returns a non-empty advisory string when the Linux inotify
// watch limit is too low for `next dev`'s hot reload, and "" otherwise (limit
// healthy, non-Linux, or the value couldn't be read/parsed). readFile is
// injected (real impl: os.ReadFile of inotifyWatchesPath) so the threshold
// logic is unit-testable. It is purely advisory — the caller prints it and
// never blocks on it.
func (o Opts) InotifyWarning(readFile func(path string) ([]byte, error)) string {
	if o.OS != "linux" {
		return ""
	}
	data, err := readFile(inotifyWatchesPath)
	if err != nil {
		return "" // can't read it → stay silent rather than nag
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return ""
	}
	if n >= inotifyMinWatches {
		return ""
	}
	return fmt.Sprintf("note: fs.inotify.max_user_watches is %d (low) — `next dev` hot reload may fail with ENOSPC. Raise it with:\n    sudo sysctl fs.inotify.max_user_watches=%d", n, inotifyRecommendedWatches)
}

// nodeVersionRe extracts major.minor from `node --version` output (e.g. "v20.20.2").
var nodeVersionRe = regexp.MustCompile(`v?(\d+)\.(\d+)`)

func nodeVersionOK(v string) bool {
	m := nodeVersionRe.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	if maj < nodeMinMajor {
		return false
	}
	if maj == nodeMinMajor && min < nodeMinMinor {
		return false
	}
	return true
}

// Check status values. A check is "ok" when the tool is present and usable,
// "missing" when the binary isn't found, "wrong_version" when present but
// outside the supported range, and "blocked" when present-but-unusable for an
// environmental reason (e.g. the Docker daemon is down).
const (
	StatusOK           = "ok"
	StatusMissing      = "missing"
	StatusWrongVersion = "wrong_version"
	StatusBlocked      = "blocked"
)

// Fix-type tiers, ordered by how much agency the tool has to remediate:
//   - auto:    keyto can run an install command itself (consent-gated).
//   - command: a single copy-pasteable command the user runs (we don't auto-run,
//     e.g. the Compose plugin is bundled, not safely scriptable).
//   - manual:  a multi-step human action (reboot into BIOS, enable a setting).
//   - none:    nothing to do (the check is ok).
const (
	FixAuto    = "auto"
	FixCommand = "command"
	FixManual  = "manual"
	FixNone    = "none"
)

// CheckResult is one diagnosed prerequisite. Diagnose returns a slice of these;
// the doctor command renders them (human + --json) and the Hub's
// /api/cli/diagnostics ingests the JSON form. Field tags match that contract.
type CheckResult struct {
	Name    string `json:"name"`     // git, docker-engine, docker-daemon, docker-compose, node, inotify
	Status  string `json:"status"`   // ok | missing | wrong_version | blocked
	FixType string `json:"fix_type"` // auto | command | manual | none
	Fix     string `json:"fix"`      // the remediation command/instruction ("" when ok)
	Detail  string `json:"detail"`   // human context (version found, why blocked, …)
}

// IsBlocking reports whether a result represents an issue that should fail the
// doctor run (anything not ok). Used to set the process exit code and the
// top-level `ok` flag in the JSON form.
func (r CheckResult) IsBlocking() bool { return r.Status != StatusOK }

// Diagnose runs a DETECT-ONLY pass over the requested tools — it never installs
// or mutates the machine. For each tool it classifies presence/version/daemon
// state and attaches the remediation (Fix/FixType) derived from installMethod
// (and OS-specific guidance for the daemon/compose cases). It reuses the same
// detection seams as Ensure (HasCommand, Version, DaemonUp, ComposeOK).
func Diagnose(ctx context.Context, want []Tool, o Opts) []CheckResult {
	out := make([]CheckResult, 0, len(want))
	for _, t := range want {
		switch t {
		case Git:
			out = append(out, o.diagnoseSimple(Git, "git"))
		case Node:
			out = append(out, o.diagnoseNode())
		case Docker:
			out = append(out, o.diagnoseDockerEngine(), o.diagnoseDockerDaemon(ctx))
		case DockerCompose:
			out = append(out, o.diagnoseCompose(ctx))
		}
	}
	return out
}

// diagnoseSimple classifies a binary that is either present (ok) or missing.
// Used for git, which has no version gate or daemon.
func (o Opts) diagnoseSimple(t Tool, name string) CheckResult {
	if o.HasCommand(t.name()) {
		v, _ := o.Version(t.name())
		return CheckResult{Name: name, Status: StatusOK, FixType: FixNone, Detail: firstLine(v)}
	}
	desc, _, auto := o.installMethod(t)
	return CheckResult{
		Name:    name,
		Status:  StatusMissing,
		FixType: fixTypeFor(auto),
		Fix:     desc,
		Detail:  "not installed",
	}
}

func (o Opts) diagnoseNode() CheckResult {
	if !o.HasCommand("node") {
		desc, _, auto := o.installMethod(Node)
		return CheckResult{
			Name:    "node",
			Status:  StatusMissing,
			FixType: fixTypeFor(auto),
			Fix:     desc,
			Detail:  fmt.Sprintf("not installed — keyto needs Node >=%d.%d", nodeMinMajor, nodeMinMinor),
		}
	}
	v, _ := o.Version("node")
	if nodeVersionOK(v) {
		return CheckResult{Name: "node", Status: StatusOK, FixType: FixNone, Detail: firstLine(v)}
	}
	desc, _, auto := o.installMethod(Node)
	return CheckResult{
		Name:    "node",
		Status:  StatusWrongVersion,
		FixType: fixTypeFor(auto),
		Fix:     desc,
		Detail:  fmt.Sprintf("%s is out of range — keyto needs Node >=%d.%d", firstLine(v), nodeMinMajor, nodeMinMinor),
	}
}

// diagnoseDockerEngine reports only on the docker binary's presence — the
// daemon's state is a separate `docker-daemon` check.
func (o Opts) diagnoseDockerEngine() CheckResult {
	if o.HasCommand("docker") {
		v, _ := o.Version("docker")
		return CheckResult{Name: "docker-engine", Status: StatusOK, FixType: FixNone, Detail: firstLine(v)}
	}
	desc, _, auto := o.installMethod(Docker)
	return CheckResult{
		Name:    "docker-engine",
		Status:  StatusMissing,
		FixType: fixTypeFor(auto),
		Fix:     desc,
		Detail:  "not installed",
	}
}

// diagnoseDockerDaemon reports whether the daemon is reachable. It is only
// meaningful when the engine is present, so a missing engine yields a `blocked`
// result that points at installing the engine first (no duplicate daemon noise).
// On Windows, a down daemon with virtualization disabled is the "can't run
// Docker at all" case — surfaced as a manual BIOS fix.
func (o Opts) diagnoseDockerDaemon(ctx context.Context) CheckResult {
	if !o.HasCommand("docker") {
		desc, _, auto := o.installMethod(Docker)
		return CheckResult{
			Name:    "docker-daemon",
			Status:  StatusBlocked,
			FixType: fixTypeFor(auto),
			Fix:     desc,
			Detail:  "the docker engine isn't installed, so the daemon can't run",
		}
	}
	if o.DaemonUp(ctx) {
		return CheckResult{Name: "docker-daemon", Status: StatusOK, FixType: FixNone, Detail: "running"}
	}
	// Daemon down. On Windows, the common root cause is CPU virtualization being
	// disabled in BIOS/UEFI — Docker Desktop/WSL2 cannot start at all. Detect it
	// so we give the right (manual) fix instead of "just start Docker".
	if o.OS == "windows" && o.VirtualizationOK != nil && !o.VirtualizationOK(ctx) {
		return CheckResult{
			Name:    "docker-daemon",
			Status:  StatusBlocked,
			FixType: FixManual,
			Fix:     "reboot → BIOS/UEFI → enable Virtualization (Intel VT-x / AMD-V), save; then: wsl --install",
			Detail:  "CPU virtualization disabled in BIOS/UEFI — Docker Desktop/WSL2 can't start. If you can't change BIOS (locked/managed machine), use the Keyto browser workspace instead.",
		}
	}
	fix := "start Docker Desktop (or `colima start`)"
	if o.OS == "darwin" && o.HasCommand("colima") {
		fix = "colima start"
	}
	return CheckResult{
		Name:    "docker-daemon",
		Status:  StatusBlocked,
		FixType: FixCommand,
		Fix:     fix,
		Detail:  "the Docker daemon is not running",
	}
}

// diagnoseCompose checks the Compose v2 plugin. When docker itself is absent the
// engine check already reports it, so compose returns ok-as-skipped (there is
// nothing actionable here that the engine check doesn't already cover).
func (o Opts) diagnoseCompose(ctx context.Context) CheckResult {
	if !o.HasCommand("docker") {
		return CheckResult{
			Name:    "docker-compose",
			Status:  StatusOK,
			FixType: FixNone,
			Detail:  "skipped — install the docker engine first",
		}
	}
	if o.ComposeOK(ctx) {
		return CheckResult{Name: "docker-compose", Status: StatusOK, FixType: FixNone, Detail: "plugin present"}
	}
	return CheckResult{
		Name:    "docker-compose",
		Status:  StatusMissing,
		FixType: FixCommand, // bundled with the engine/Desktop — instruct, don't auto-run
		Fix:     o.composeFix(),
		Detail:  "the Compose v2 plugin (`docker compose`) is missing — `keyto start` runs `docker compose up`",
	}
}

// composeFix is the OS-specific Compose-plugin remediation, mirroring the
// guidance composeMissing() bakes into its error (kept in one place).
func (o Opts) composeFix() string {
	switch o.OS {
	case "darwin":
		return "brew install docker-compose (or reinstall Docker Desktop, which bundles it)"
	case "linux":
		if mgr := o.linuxMgr(); mgr != "" {
			return mgr + " install -y docker-compose-plugin (or reinstall via https://get.docker.com, which includes the plugin)"
		}
		return "install the docker-compose-plugin package for your distro (or reinstall via https://get.docker.com)"
	default: // windows + unmatched
		return "reinstall Docker Desktop (it bundles the compose plugin)"
	}
}

// fixTypeFor maps installMethod's auto bool to a fix tier: auto-capable installs
// are FixAuto; instruct-only ones are FixCommand.
func fixTypeFor(auto bool) string {
	if auto {
		return FixAuto
	}
	return FixCommand
}

// firstLine returns the first line of s trimmed of surrounding whitespace —
// version output is sometimes multi-line, and the human/JSON detail wants one
// concise line.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// InotifyCheck mirrors InotifyWarning but as a CheckResult so the doctor command
// can fold the Linux inotify advisory into its check list. ok when the limit is
// healthy / not applicable; blocked (with a command fix) when it's low.
func (o Opts) InotifyCheck(readFile func(path string) ([]byte, error)) CheckResult {
	w := o.InotifyWarning(readFile)
	if w == "" {
		detail := "watch limit healthy"
		if o.OS != "linux" {
			detail = "not applicable (non-Linux)"
		}
		return CheckResult{Name: "inotify", Status: StatusOK, FixType: FixNone, Detail: detail}
	}
	return CheckResult{
		Name:    "inotify",
		Status:  StatusBlocked,
		FixType: FixCommand,
		Fix:     fmt.Sprintf("sudo sysctl fs.inotify.max_user_watches=%d", inotifyRecommendedWatches),
		Detail:  "fs.inotify.max_user_watches is low — `next dev` hot reload may fail with ENOSPC",
	}
}

// installMethod returns (description, command+args) for (os, pkgManager, tool),
// and whether auto-install is supported on this platform.
func (o Opts) installMethod(t Tool) (desc string, cmd []string, auto bool) {
	switch o.OS {
	case "darwin":
		switch t {
		case Git:
			return "xcode-select --install", []string{"xcode-select", "--install"}, true
		case Docker:
			if o.HasCommand("brew") {
				return "brew install colima docker (then colima start)", []string{"brew", "install", "colima", "docker"}, true
			}
			return "install Homebrew (https://brew.sh), then: brew install colima docker", nil, false
		case Node:
			if o.HasCommand("brew") {
				return "brew install node@24", []string{"brew", "install", "node@24"}, true
			}
			return "install Homebrew (https://brew.sh), then: brew install node@24", nil, false
		}
	case "linux":
		mgr := o.linuxMgr()
		if mgr == "" {
			return "", nil, false
		}
		switch t {
		case Git:
			return mgr + " install git", append(o.sudo(), mgr, "install", "-y", "git"), true
		case Docker:
			return "Docker Engine (get.docker.com)", []string{"sh", "-c", "curl -fsSL https://get.docker.com | sh"}, true
		case Node:
			// Distro `apt/dnf install nodejs` usually lands a Node older than
			// 20, which keyto rejects. Use the NodeSource v24 setup script on
			// apt-based distros; on dnf, point at NodeSource's rpm setup too.
			if mgr == "apt-get" {
				return "Node 24 via NodeSource (deb.nodesource.com)",
					[]string{"sh", "-c", "curl -fsSL https://deb.nodesource.com/setup_24.x | sudo -E bash - && sudo apt-get install -y nodejs"}, true
			}
			return "Node 24 via NodeSource (rpm.nodesource.com)",
				[]string{"sh", "-c", "curl -fsSL https://rpm.nodesource.com/setup_24.x | sudo -E bash - && sudo " + mgr + " install -y nodejs"}, true
		}
	}
	// windows + anything unmatched: instruct only.
	return o.windowsHint(t), nil, false
}

func (o Opts) linuxMgr() string {
	for _, m := range []string{"apt-get", "dnf"} {
		if o.HasCommand(m) {
			return m
		}
	}
	return ""
}

func (o Opts) sudo() []string {
	if o.HasCommand("sudo") {
		return []string{"sudo"}
	}
	return nil
}

func (o Opts) windowsHint(t Tool) string {
	switch t {
	case Git:
		return "winget install Git.Git"
	case Docker:
		return "winget install Docker.DockerDesktop"
	case Node:
		return "winget install OpenJS.NodeJS.LTS"
	}
	return ""
}

func (o Opts) install(ctx context.Context, t Tool) error {
	desc, cmd, auto := o.installMethod(t)
	if !auto || cmd == nil {
		return fmt.Errorf("%s is required but not installed — install it and re-run:\n    %s", t.name(), desc)
	}
	fmt.Fprintf(o.Out, "%s is required but not installed.\n", t.name())
	if !(o.AutoYes || o.Prompt(fmt.Sprintf("Install %s via %s? [y/N] ", t.name(), desc))) {
		return fmt.Errorf("%s install declined — run it yourself and re-run:\n    %s", t.name(), desc)
	}
	if err := o.Run(ctx, cmd[0], cmd[1:]...); err != nil {
		return fmt.Errorf("installing %s (%s) failed: %w", t.name(), desc, err)
	}
	// colima needs an explicit start after install on macOS.
	if t == Docker && o.OS == "darwin" && o.HasCommand("colima") {
		_ = o.Run(ctx, "colima", "start")
	}
	if !o.HasCommand(t.name()) {
		return fmt.Errorf("%s still not found after install — open a new shell and re-run", t.name())
	}
	return nil
}

func (o Opts) dockerDaemonDown(ctx context.Context) error {
	// Starting the daemon is a machine mutation — gate it behind the same
	// consent as install(). With colima present (macOS) we can offer to start
	// it; otherwise instruct the user.
	if o.OS == "darwin" && o.HasCommand("colima") {
		if o.AutoYes || o.Prompt("Docker daemon isn't running. Start it with `colima start`? [y/N] ") {
			fmt.Fprintln(o.Out, "Starting colima…")
			if err := o.Run(ctx, "colima", "start"); err == nil && o.DaemonUp(ctx) {
				return nil
			}
		}
	}
	return fmt.Errorf("the Docker daemon is not running — start Docker Desktop (or `colima start`) and re-run")
}

// composeMissing returns OS-specific guidance for installing the Compose v2
// plugin when `docker` is present but `docker compose` is not. The plugin is
// not safely auto-installable (it's bundled with the engine/Desktop or shipped
// as a distro package), so we instruct rather than mutate.
func (o Opts) composeMissing() error {
	var fix string
	switch o.OS {
	case "darwin":
		fix = "brew install docker-compose (or reinstall Docker Desktop, which bundles it)"
	case "linux":
		if mgr := o.linuxMgr(); mgr != "" {
			fix = mgr + " install -y docker-compose-plugin (or reinstall via https://get.docker.com, which includes the plugin)"
		} else {
			fix = "install the docker-compose-plugin package for your distro (or reinstall via https://get.docker.com)"
		}
	default: // windows + unmatched
		fix = "reinstall Docker Desktop (it bundles the compose plugin)"
	}
	return fmt.Errorf("docker is installed but the Compose v2 plugin (`docker compose`) is missing — keyto start runs `docker compose up`. Install it and re-run:\n    %s", fix)
}
