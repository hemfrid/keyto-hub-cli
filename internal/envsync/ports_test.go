package envsync

// White-box tests for the pure per-project isolation helpers (compose project
// name + deterministic host ports). These live in package envsync so they can
// reach the unexported functions directly.

import "testing"

func TestNormalizeComposeProjectName(t *testing.T) {
	cases := map[string]string{
		"expense-summarizer": "expense-summarizer", // already valid
		"Acme-Web":           "acme-web",           // lower-cased
		"my project!":        "my-project-",        // space + bang → dashes
		"__leading":          "leading",            // leading separators trimmed
		"":                   "app",                // empty → fallback
		"-_-":                "app",                // all separators → fallback
	}
	for in, want := range cases {
		if got := normalizeComposeProjectName(in); got != want {
			t.Errorf("normalizeComposeProjectName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPortOffset_DeterministicAndInRange(t *testing.T) {
	// Stable across calls — the same project must hash to the same port every
	// sync, or a previously-written DATABASE_URL would go stale.
	a := portOffset("expense-summarizer")
	b := portOffset("expense-summarizer")
	if a != b {
		t.Fatalf("portOffset not deterministic: %d vs %d", a, b)
	}
	if a < 0 || a >= 4000 {
		t.Fatalf("portOffset out of range: %d", a)
	}
}

func TestDefaultPorts_NonOverlappingBands(t *testing.T) {
	// For any project the three service ports must fall in distinct,
	// non-overlapping, unprivileged bands below the ephemeral range.
	for _, name := range []string{"a", "expense-summarizer", "zzz-very-long-name", "shop"} {
		p := defaultPorts(name)
		if !(p.Postgres >= 15000 && p.Postgres <= 18999) {
			t.Errorf("%s: postgres port %d outside 15000–18999", name, p.Postgres)
		}
		if !(p.MySQL >= 23000 && p.MySQL <= 26999) {
			t.Errorf("%s: mysql port %d outside 23000–26999", name, p.MySQL)
		}
		if !(p.Redis >= 27000 && p.Redis <= 30999) {
			t.Errorf("%s: redis port %d outside 27000–30999", name, p.Redis)
		}
		if p.Postgres == p.MySQL || p.Postgres == p.Redis || p.MySQL == p.Redis {
			t.Errorf("%s: service ports collide: %+v", name, p)
		}
	}
}

func TestDefaultPorts_DifferByProject(t *testing.T) {
	// Two different projects almost always get different ports so their stacks
	// can run side by side without a host-port collision.
	if defaultPorts("expense-summarizer").Postgres == defaultPorts("acme-web").Postgres {
		t.Error("expected distinct postgres ports for distinct projects")
	}
}
