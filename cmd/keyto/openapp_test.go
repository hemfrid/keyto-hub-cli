package main

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestAppURL_DefaultAndEnv(t *testing.T) {
	t.Setenv("PORT", "")
	if got := appURL(); got != "http://localhost:3000" {
		t.Errorf("default appURL = %q, want http://localhost:3000", got)
	}
	t.Setenv("PORT", "4123")
	if got := appURL(); got != "http://localhost:4123" {
		t.Errorf("appURL with PORT=4123 = %q", got)
	}
}

func TestWaitThenOpen_OpensWhenProbeSucceeds(t *testing.T) {
	var opened string
	probe := func(context.Context, string) bool { return true }
	open := func(url string) error { opened = url; return nil }

	var out bytes.Buffer
	waitThenOpen(context.Background(), "http://localhost:3000", probe, open, time.Millisecond, 5, &out)

	if opened != "http://localhost:3000" {
		t.Errorf("expected browser opened with the app URL; got %q", opened)
	}
	if out.Len() == 0 {
		t.Error("expected a status line when opening the browser")
	}
}

func TestWaitThenOpen_GivesUpAfterAttempts(t *testing.T) {
	var openCalls int
	probe := func(context.Context, string) bool { return false } // never up
	open := func(string) error { openCalls++; return nil }

	waitThenOpen(context.Background(), "http://localhost:3000", probe, open, time.Millisecond, 3, &bytes.Buffer{})

	if openCalls != 0 {
		t.Errorf("open must not be called when the server never responds; got %d", openCalls)
	}
}

func TestWaitThenOpen_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	var openCalls int
	probe := func(context.Context, string) bool { return false }
	open := func(string) error { openCalls++; return nil }

	// With a cancelled context the loop must return immediately (a huge attempt
	// count would hang if cancellation weren't honoured).
	done := make(chan struct{})
	go func() {
		waitThenOpen(ctx, "http://localhost:3000", probe, open, time.Hour, 1_000_000, &bytes.Buffer{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitThenOpen did not honour context cancellation")
	}
	if openCalls != 0 {
		t.Errorf("open must not run on a cancelled context; got %d", openCalls)
	}
}
