// Package huborigin says whether two URLs name the same Hub. The Hub's URL
// decides where keyto wires a git remote and to whom its credential helper
// hands the Hub token, so both places compare origins with this one rule.
package huborigin

import (
	"net"
	"net/url"
	"strings"
)

// Of returns the origin of an http(s) URL as "scheme://host:port", with the
// scheme and host lowercased and the default port (443, 80) filled in. It
// returns false for any other scheme, for a URL with user info, and for one
// with no host.
func Of(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	switch scheme {
	case "https":
		if port == "" {
			port = "443"
		}
	case "http":
		if port == "" {
			port = "80"
		}
	default:
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", false
	}
	return scheme + "://" + net.JoinHostPort(host, port), true
}

// Same reports whether a and b are both valid and name the same origin. http
// and https on one host and port are different origins.
func Same(a, b string) bool {
	oa, okA := Of(a)
	ob, okB := Of(b)
	return okA && okB && oa == ob
}
