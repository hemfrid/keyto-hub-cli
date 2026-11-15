package envsync_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/envsync"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

// `keyto start` signs in again only when the error it gets back still carries
// the Hub's typed 401 (hub.IsUnauthorized). These pin that env sync hands it
// up with its type intact, on both stacks.

func TestRun_NextJS_AHub401FromFetchKeepsItsType(t *testing.T) {
	m := &project.Marker{Name: "acme-web", Org: "hemfrid", Repo: "acme-web", HubURL: "https://hub.example.com"}
	dir := makeProjectDir(t, m)
	writeJSON(t, filepath.Join(dir, ".keyto", "env-inventory.json"), inventory{
		SchemaVersion: 1,
		Keys:          []inventoryKey{{Key: "SENDGRID_API_KEY", LocalSource: "uat", Usages: []string{}}},
	})
	d := baseDeps(t, dir)
	d.Fetch = errorFetch(&hub.StatusError{Op: "fetch-env-values", StatusCode: 401})

	err := envsync.Run(context.Background(), []string{"--print"}, d)
	if !hub.IsUnauthorized(err) {
		t.Errorf("the Hub's 401 lost its type on the way up: %v", err)
	}
}

func TestRun_Workers_AHub401FromFetchKeepsItsType(t *testing.T) {
	dir := makeWorkersDir(t, environmentsFor([]string{"API_KEY"}))
	d := baseDeps(t, dir)
	d.Fetch = errorFetch(&hub.StatusError{Op: "fetch-env-values", StatusCode: 401})

	err := envsync.Run(context.Background(), nil, d)
	if !hub.IsUnauthorized(err) {
		t.Errorf("the Hub's 401 lost its type on the way up: %v", err)
	}
}

func TestRun_AHub403FromFetchIsNotAnAuthError(t *testing.T) {
	dir := makeWorkersDir(t, environmentsFor([]string{"API_KEY"}))
	d := baseDeps(t, dir)
	d.Fetch = errorFetch(&hub.StatusError{Op: "fetch-env-values", StatusCode: 403})

	err := envsync.Run(context.Background(), nil, d)
	if err == nil || hub.IsUnauthorized(err) {
		t.Errorf("want a non-401 error, got: %v", err)
	}
}
