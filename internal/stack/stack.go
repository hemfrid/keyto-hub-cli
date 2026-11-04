// Package stack tells which kind of Keyto project a checkout is, so `keyto
// start`, `keyto env sync` and `keyto doctor` can pick the right local loop.
package stack

import (
	"os"
	"path/filepath"
)

// Kind is a project's stack.
type Kind string

const (
	// NextJS is the default stack: Docker Compose backing services, `.env`
	// from .keyto/env-inventory.json, `next dev`.
	NextJS Kind = "nextjs"
	// Workers is a hemfrid/keyto-hub-workers-template project: no Docker,
	// `.dev.vars` from .environments, `wrangler dev`.
	Workers Kind = "workers"
)

// wranglerConfigs are the config files the Workers template ships. wrangler
// also reads wrangler.toml, but the template never uses it.
var wranglerConfigs = []string{"wrangler.json", "wrangler.jsonc"}

// Detect returns Workers when dir holds a wrangler.json or wrangler.jsonc
// file, and NextJS otherwise.
func Detect(dir string) Kind {
	for _, name := range wranglerConfigs {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Mode().IsRegular() {
			return Workers
		}
	}
	return NextJS
}
