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

// workersStatusJSON builds a database answer with n D1 and n KV bindings,
// every string (binding names, migration names, snapshot fields, versions)
// set to long.
func workersStatusJSON(n int, long string) string {
	var b strings.Builder
	q := func(s string) string { j, _ := json.Marshal(s); return string(j) }
	b.WriteString(`{"kind":"d1","status":{"runtime":` + q(long) + `,"workerd":` + q(long) + `,"d1":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"binding":` + q(long) + `,"bytes":1,"migrations":[` + q(long) + `]}`)
	}
	b.WriteString(`],"kv":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"binding":` + q(long) + `,"keys":1}`)
	}
	b.WriteString(`],"lastSnapshot":{"key":` + q(long) + `,"takenAt":` + q(long) + `,"reason":` + q(long) + `}}}`)
	return b.String()
}

func TestRenderD1_RowsCappedAtFiftyEachWithMoreLine(t *testing.T) {
	out := render(t, "database", workersStatusJSON(60, "B"), palette{})
	if n := strings.Count(out, "\nB  "); n != 2*maxStatusRows {
		t.Fatalf("%d binding rows printed, want %d:\n%s", n, 2*maxStatusRows, out)
	}
	if !strings.Contains(out, "\n… 20 more bindings (10 D1, 10 KV)\n") {
		t.Fatalf("the hidden rows should be counted on a … more line:\n%s", out)
	}
	if strings.Contains(render(t, "database", workersStatusJSON(maxStatusRows, "B"), palette{}), "more bindings") {
		t.Fatal("a … more line was printed with nothing hidden")
	}
}

func TestRenderD1_LongCellsTruncated(t *testing.T) {
	long := strings.Repeat("é", 300) // multi-byte: the cap is in runes
	out := render(t, "database", workersStatusJSON(1, long), palette{})
	capped := strings.Repeat("é", maxStatusCell-1) + "…"
	if strings.Contains(out, strings.Repeat("é", maxStatusCell)) {
		t.Fatalf("a string longer than %d runes was printed whole", maxStatusCell)
	}
	// Binding cells, the migration name, the versions and the three snapshot
	// fields each come out capped.
	if n := strings.Count(out, capped); n < 7 {
		t.Fatalf("%d capped strings, want at least 7:\n%s", n, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if n := len([]rune(line)); n > 6*maxStatusCell {
			t.Fatalf("a %d-rune line: padding used the uncapped width", n)
		}
	}
}

func TestRenderD1_HugeForgedAnswerHasBoundedOutput(t *testing.T) {
	huge := strings.Repeat("A", 3<<20) // a 3 MiB binding name
	raw := workersStatusJSON(20000, "x")
	raw = strings.Replace(raw, `"binding":"x"`, `"binding":"`+huge+`"`, 1)
	for _, kind := range []string{"database", "overview"} {
		in := raw
		if kind == "overview" {
			// {"kind":"d1",<status>} → the overview's "d1":{<status>}
			in = overviewBase + `,"argo":null,"externalSecrets":[],"databases":[],"d1":{` + strings.TrimPrefix(raw, `{"kind":"d1",`) + `}`
		}
		out := render(t, kind, in, palette{})
		if len(out) > 128<<10 {
			t.Fatalf("%s: %d bytes of output for a forged status line", kind, len(out))
		}
		if !strings.Contains(out, "more bindings") {
			t.Fatalf("%s: the forged answer was not rendered:\n%.500s", kind, out)
		}
	}
}
