package huborigin_test

import (
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/huborigin"
)

func TestOf(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"https://hub.keytolabs.com", "https://hub.keytolabs.com:443", true},
		{"https://hub.keytolabs.com/", "https://hub.keytolabs.com:443", true},
		{"HTTPS://HUB.Keytolabs.com/path", "https://hub.keytolabs.com:443", true},
		{"https://hub.keytolabs.com:443", "https://hub.keytolabs.com:443", true},
		{"https://hub.keytolabs.com:8443", "https://hub.keytolabs.com:8443", true},
		{"http://127.0.0.1:3000", "http://127.0.0.1:3000", true},
		{"http://hub.keytolabs.com", "http://hub.keytolabs.com:80", true},
		{"http://hub.keytolabs.com:443", "http://hub.keytolabs.com:443", true},
		{"https://alice@hub.keytolabs.com", "", false},
		{"https://alice:pw@hub.keytolabs.com", "", false},
		{"ftp://hub.keytolabs.com", "", false},
		{"file:///tmp/x", "", false},
		{"ext::sh -c x", "", false},
		{"hub.keytolabs.com", "", false},
		{"https://", "", false},
		{"", "", false},
		{"://bad", "", false},
	} {
		got, ok := huborigin.Of(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Of(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// http and https on the same host and port are two origins: the scheme is part
// of it, so an http request can never stand in for the https Hub.
func TestSame(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"https://hub.keytolabs.com", "https://HUB.keytolabs.com:443/", true},
		{"http://hub.keytolabs.com:443", "https://hub.keytolabs.com", false},
		{"https://alice@hub.keytolabs.com", "https://hub.keytolabs.com", false},
		{"https://hub.keytolabs.com", "https://hub.keytolabs.com:8443", false},
		{"", "", false},
	} {
		if got := huborigin.Same(tc.a, tc.b); got != tc.same {
			t.Errorf("Same(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.same)
		}
	}
}
