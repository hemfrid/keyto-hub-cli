// Package auth provides PKCE (RFC 7636 S256) helpers for the keyto CLI login flow.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCE holds the three values needed for an S256 PKCE exchange.
type PKCE struct {
	// Verifier is the code_verifier sent to the token endpoint.
	Verifier string
	// Challenge is the code_challenge sent to the authorization endpoint.
	// It equals base64.RawURLEncoding(sha256(Verifier)) — no padding.
	Challenge string
	// State is the OAuth state parameter used to bind the redirect.
	State string
}

// NewPKCE generates a fresh PKCE triple using crypto/rand.
// The Challenge is computed as base64url(sha256(Verifier)) with no padding,
// matching the Hub's Node.js implementation:
//
//	createHash('sha256').update(verifier).digest('base64url')
func NewPKCE() (PKCE, error) {
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return PKCE{}, err
	}

	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return PKCE{}, err
	}

	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])

	return PKCE{
		Verifier:  verifier,
		Challenge: challenge,
		State:     base64.RawURLEncoding.EncodeToString(stateBytes),
	}, nil
}
