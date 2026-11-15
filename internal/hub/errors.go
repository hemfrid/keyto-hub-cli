package hub

import (
	"errors"
	"net/http"

	"github.com/hemfrid/keyto-hub-cli/internal/termsafe"
)

// StatusError is a non-2xx answer from a Hub call. It carries the status code
// as a number, so callers decide on the code (errors.As) and never on the
// error text, which can contain a project name, a URL or a tool's output.
type StatusError struct {
	// Op names the call, e.g. "list-projects".
	Op         string
	StatusCode int
	// Message replaces the default text when the call has its own wording.
	Message string
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Op + " failed: " + termsafe.StatusLine(e.StatusCode)
}

// IsUnauthorized reports whether err is a 401 the Hub answered: the stored
// credential was refused (revoked or stale). It looks only at the typed status
// code of the Hub's own error types, anywhere in err's chain.
func IsUnauthorized(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.StatusCode == http.StatusUnauthorized
	}
	var sev *SetEnvValuesError
	if errors.As(err, &sev) {
		return sev.StatusCode == http.StatusUnauthorized
	}
	var de *DebugError
	if errors.As(err, &de) {
		return de.Status == http.StatusUnauthorized
	}
	return false
}
