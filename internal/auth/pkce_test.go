package auth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/hemfrid/keyto-cli/internal/auth"
)

func TestNewPKCE_Challenge(t *testing.T) {
	p, err := auth.NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE() error = %v", err)
	}

	// Independently recompute the expected challenge.
	h := sha256.Sum256([]byte(p.Verifier))
	want := base64.RawURLEncoding.EncodeToString(h[:])

	if p.Challenge != want {
		t.Errorf("Challenge = %q, want %q", p.Challenge, want)
	}
}

func TestNewPKCE_VerifierDecodes32Bytes(t *testing.T) {
	p, err := auth.NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE() error = %v", err)
	}

	b, err := base64.RawURLEncoding.DecodeString(p.Verifier)
	if err != nil {
		t.Fatalf("Verifier is not valid base64url: %v", err)
	}
	if len(b) != 32 {
		t.Errorf("Verifier decoded to %d bytes, want 32", len(b))
	}
}

func TestNewPKCE_VerifierAndStateNonEmpty(t *testing.T) {
	p, err := auth.NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE() error = %v", err)
	}
	if p.Verifier == "" {
		t.Error("Verifier is empty")
	}
	if p.State == "" {
		t.Error("State is empty")
	}
}

func TestNewPKCE_EntropyAcrossCalls(t *testing.T) {
	p1, err := auth.NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE() first call error = %v", err)
	}
	p2, err := auth.NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE() second call error = %v", err)
	}

	if p1.Verifier == p2.Verifier {
		t.Error("Verifier is identical across two calls — no entropy")
	}
	if p1.State == p2.State {
		t.Error("State is identical across two calls — no entropy")
	}
}
