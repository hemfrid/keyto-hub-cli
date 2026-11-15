package boot

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

// `keyto start` decides on a re-login from the error boot.Run returns, by
// type (errors.As). These pin that each step's error keeps its type through
// boot.Run's own wrapping.

func TestRun_AHub401FromEnvSyncKeepsItsType(t *testing.T) {
	var rec []string
	d := baseDeps(&rec)
	d.EnvSync = func(ctx context.Context) error {
		return fmt.Errorf("env sync: fetch values from Hub: %w", &hub.StatusError{Op: "fetch-env-values", StatusCode: 401})
	}
	err := Run(context.Background(), d, Flags{})
	if !hub.IsUnauthorized(err) {
		t.Errorf("the Hub's 401 lost its type on the way up: %v", err)
	}
}

// installHint stands in for the typed error the install step returns.
type installHint struct{ err error }

func (e *installHint) Error() string { return "hint: " + e.err.Error() }
func (e *installHint) Unwrap() error { return e.err }

func TestRun_ATypedErrorFromInstallKeepsItsType(t *testing.T) {
	var rec []string
	d := baseDeps(&rec)
	d.NodeModulesPresent = func() bool { return false }
	exit := errors.New("exit status 1")
	d.Install = func(ctx context.Context) error { return &installHint{err: exit} }

	err := Run(context.Background(), d, Flags{})
	var hint *installHint
	if !errors.As(err, &hint) || !errors.Is(err, exit) {
		t.Errorf("the install step's error lost its type on the way up: %v", err)
	}

	d.Install = func(ctx context.Context) error { return &hub.StatusError{Op: "npm install", StatusCode: 401} }
	if err := Run(context.Background(), d, Flags{}); !hub.IsUnauthorized(err) {
		t.Errorf("a typed 401 from the install step lost its type: %v", err)
	}
}
