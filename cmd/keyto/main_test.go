package main

import (
	"strings"
	"testing"
)

func TestDispatch_UnknownCommand(t *testing.T) {
	err := dispatch([]string{"bogus"})
	if err == nil {
		t.Fatal("expected non-nil error for unknown command, got nil")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("expected error to contain 'unknown command', got: %q", err.Error())
	}
}

func TestDispatch_NoArgs(t *testing.T) {
	err := dispatch([]string{})
	if err != nil {
		t.Fatalf("expected nil error for no args, got: %v", err)
	}
}

func TestDispatch_Help(t *testing.T) {
	err := dispatch([]string{"help"})
	if err != nil {
		t.Fatalf("expected nil error for help command, got: %v", err)
	}
}
