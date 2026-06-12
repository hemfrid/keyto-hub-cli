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
)

type Tool int

const (
	Git Tool = iota
	Docker
	Node
)

func (t Tool) name() string {
	switch t {
	case Git:
		return "git"
	case Docker:
		return "docker"
	case Node:
		return "node"
	}
	return "?"
}

// nodeMin/nodeMax mirror the template package.json engines: >=20.9 <21.
const (
	nodeMinMajor = 20
	nodeMinMinor = 9
	nodeMaxMajor = 21 // exclusive
)

// Deps are the injected seams. main.go wires real implementations.
type Deps struct {
	OS         string                            // runtime.GOOS
	HasCommand func(name string) bool            // exec.LookPath != nil
	Version    func(name string) (string, error) // e.g. `node --version` -> "v20.20.2"
	DaemonUp   func(ctx context.Context) bool    // `docker info` succeeds
	Prompt     func(question string) bool        // y/N consent (false on non-TTY unless AutoYes)
	Run        func(ctx context.Context, name string, args ...string) error
	Out        io.Writer
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
			return fmt.Errorf("node %s is out of range — keyto needs Node >=%d.%d <%d. Fix with `nvm install 20 && nvm use 20` (or `brew install node@20`), then re-run",
				v, nodeMinMajor, nodeMinMinor, nodeMaxMajor)
		}
	case Docker:
		if o.HasCommand("docker") {
			if o.DaemonUp(ctx) {
				return nil
			}
			return o.dockerDaemonDown(ctx)
		}
	default: // Git
		if o.HasCommand(t.name()) {
			return nil
		}
	}
	return o.install(ctx, t)
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
	if maj < nodeMinMajor || maj >= nodeMaxMajor {
		return false
	}
	if maj == nodeMinMajor && min < nodeMinMinor {
		return false
	}
	return true
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
				return "brew install node@20", []string{"brew", "install", "node@20"}, true
			}
			return "install Homebrew (https://brew.sh), then: brew install node@20", nil, false
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
			return mgr + " install nodejs (v20)", append(o.sudo(), mgr, "install", "-y", "nodejs"), true
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
