package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

// The Hub's debug API answers two shapes (keyto-hub lib/project-debug-shape.ts):
// a Next.js project's `database` is the bare CNPG array and its overview has
// no `d1` key; a Workers project's `database` is {"kind":"d1","status":…} and
// its overview keeps `databases: []` and adds `d1: {status} | null`. The
// fixtures in testdata/debug carry both, the Workers ones with extra keys the
// CLI must ignore. The Next.js goldens were written by the CLI before it knew
// about Workers, so they pin that output byte for byte.

var updateGolden = flag.Bool("update", false, "rewrite testdata/debug/*.golden")

func renderFixture(t *testing.T, kind, fixture string, c palette) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "debug", fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := renderDebugColour(&buf, kind, json.RawMessage(raw), false, c); err != nil {
		t.Fatalf("%s: %v", fixture, err)
	}
	return buf.String()
}

func TestRenderDebug_Golden(t *testing.T) {
	for _, tc := range []struct{ kind, fixture string }{
		{"database", "database-nextjs"},
		{"overview", "overview-nextjs"},
		{"database", "database-workers"},
		{"database", "database-workers-no-status"},
		{"overview", "overview-workers"},
		{"overview", "overview-workers-d1-unavailable"},
		{"overview", "overview-workers-no-status"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			got := renderFixture(t, tc.kind, tc.fixture, palette{})
			golden := filepath.Join("testdata", "debug", tc.fixture+".golden")
			if *updateGolden {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("output differs from %s\n--- got\n%s\n--- want\n%s", golden, got, want)
			}
		})
	}
}

func TestRenderD1_NoBindingsAndNoSnapshot(t *testing.T) {
	out := render(t, "database", `{"kind":"d1","status":{"runtime":"v1.0.0","workerd":"w","d1":[],"kv":[],"lastSnapshot":null}}`, palette{})
	want := "Workers runtime v1.0.0 (workerd w):\nNo D1 or KV bindings.\nLast snapshot: none yet\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestRenderD1_UnknownDatabaseKindIsAnError(t *testing.T) {
	var buf bytes.Buffer
	err := renderDebugColour(&buf, "database", json.RawMessage(`{"kind":"mongo","clusters":[]}`), false, palette{})
	if !errors.Is(err, hub.ErrUnknownDatabaseShape) {
		t.Fatalf("err = %v, want ErrUnknownDatabaseShape", err)
	}
}

// The status line shares stdout with the app, so the app can print a
// look-alike: none of its strings may reach the terminal raw or forge lines.
func TestRenderD1_StatusStringsAreSanitised(t *testing.T) {
	// JSON escapes: an OSC title sequence, a screen clear, forged lines.
	evil := `\u001b]0;pwned\u0007\u001b[2J`
	forge := `x\nSecret sync:\n  db: synced`
	status := `{"runtime":"v1` + evil + `","workerd":"` + forge + `",` +
		`"d1":[{"binding":"DB` + evil + `","bytes":1,"migrations":["0001` + forge + `"]}],` +
		`"kv":[{"binding":"K` + forge + `","keys":2}],` +
		`"lastSnapshot":{"key":"k` + evil + `","takenAt":"` + forge + `","reason":"r` + evil + `"}}`
	for _, c := range []palette{{}, {on: true}} {
		for _, out := range []string{
			render(t, "database", `{"kind":"d1","status":`+status+`}`, c),
			render(t, "overview", overviewBase+`,"argo":null,"externalSecrets":[],"databases":[],"d1":{"status":`+status+`}}`, c),
		} {
			if strings.Contains(out, "\x1b]") || strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\a") {
				t.Fatalf("a terminal escape from the status line got through: %q", out)
			}
			if strings.Contains(out, "\nSecret sync:\n  db: synced") {
				t.Fatalf("the status line forged output lines: %q", out)
			}
			if !strings.Contains(out, "Workers runtime v1") {
				t.Fatalf("the status was not rendered: %q", out)
			}
		}
	}
}

func TestFormatBytes_MatchesTheHub(t *testing.T) {
	for in, want := range map[float64]string{
		0: "0 B", 1023: "1023 B", 1024: "1 KiB", 1536: "1.5 KiB", 10 * 1024: "10 KiB",
		10.4 * 1024 * 1024: "10 MiB", 9.96 * 1024 * 1024: "10 MiB", 1 << 50: "1024 TiB",
	} {
		if got := formatBytes(in); got != want {
			t.Errorf("formatBytes(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestDebugOverview_D1AbsentVsNull(t *testing.T) {
	for _, tc := range []struct {
		raw          string
		hasD1, nilD1 bool
	}{
		{overviewBase + `,"databases":[]}`, false, true},
		{overviewBase + `,"databases":[],"d1":null}`, true, true},
		{overviewBase + `,"databases":[],"d1":{"status":null}}`, true, false},
	} {
		var o hub.DebugOverview
		if err := json.Unmarshal([]byte(tc.raw), &o); err != nil {
			t.Fatal(err)
		}
		if o.HasD1 != tc.hasD1 || (o.D1 == nil) != tc.nilD1 {
			t.Errorf("%s: HasD1=%v D1=%v, want HasD1=%v nil=%v", tc.raw, o.HasD1, o.D1, tc.hasD1, tc.nilD1)
		}
	}
}

func TestRenderDebug_WorkersJSONIsPassthrough(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "debug", "database-workers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := renderDebug(&buf, "database", raw, true); err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &want)
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("--json changed the answer:\n%s\nwant\n%s", gotJSON, wantJSON)
	}
}
