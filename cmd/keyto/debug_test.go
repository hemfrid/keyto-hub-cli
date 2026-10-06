package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"regexp"
	"strings"
	"testing"

	"github.com/hemfrid/keyto-hub-cli/internal/hub"
)

func noMarker() string   { return "" }
func demoMarker() string { return "demo-app" }

func TestParseDebugArgs_FlagsAfterPositional(t *testing.T) {
	a, err := parseDebugArgs("logs", []string{"demo-app", "--env", "prod", "--tail", "50", "--previous", "--pod", "web-1"}, noMarker)
	if err != nil {
		t.Fatal(err)
	}
	if a.Project != "demo-app" || a.Env != "prod" || a.Tail != 50 || !a.Previous || a.Pod != "web-1" {
		t.Fatalf("got %+v", a)
	}
}

func TestParseDebugArgs_DefaultsToMarkerAndUat(t *testing.T) {
	a, err := parseDebugArgs("pods", nil, demoMarker)
	if err != nil {
		t.Fatal(err)
	}
	if a.Project != "demo-app" || a.Env != "uat" {
		t.Fatalf("got %+v", a)
	}
}

func TestParseDebugArgs_NoProjectAnywhere(t *testing.T) {
	if _, err := parseDebugArgs("pods", nil, noMarker); err == nil {
		t.Fatal("want error when no project arg and no .keyto/project.json")
	}
}

func TestParseDebugArgs_RejectsBadEnv(t *testing.T) {
	if _, err := parseDebugArgs("pods", []string{"demo-app", "--env", "dev"}, noMarker); err == nil {
		t.Fatal("want error for --env dev")
	}
}

func TestParseDebugArgs_SinceDuration(t *testing.T) {
	a, err := parseDebugArgs("logs", []string{"demo-app", "--since", "1h"}, noMarker)
	if err != nil || a.SinceSeconds != 3600 {
		t.Fatalf("got %+v err %v", a, err)
	}
}

func TestDebugQuery_OmitsUnsetLogParams(t *testing.T) {
	q := debugQuery("logs", debugArgs{Env: "uat"})
	if q.Encode() != "env=uat" {
		t.Fatalf("got %q", q.Encode())
	}
	q = debugQuery("logs", debugArgs{Env: "prod", Pod: "web-1", Container: "app", Previous: true, Tail: 50, SinceSeconds: 60})
	if q.Encode() != "container=app&env=prod&pod=web-1&previous=true&sinceSeconds=60&tail=50" {
		t.Fatalf("got %q", q.Encode())
	}
}

func TestRenderDebug_PodsTableShowsLastTermination(t *testing.T) {
	raw := json.RawMessage(`[{"name":"web-1","phase":"Running","ready":false,"restarts":3,"containers":[
	  {"name":"app","ready":false,"restarts":3,"state":"waiting","reason":"CrashLoopBackOff",
	   "lastTermination":{"reason":"OOMKilled","exitCode":137,"finishedAt":"2026-10-06T10:05:00Z"}}]}]`)
	var buf bytes.Buffer
	if err := renderDebug(&buf, "pods", raw, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "web-1", "Running", "3", "OOMKilled (exit 137)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderDebug_JSONIsPassthrough(t *testing.T) {
	raw := json.RawMessage(`{"pod":"web-1","lines":["a"]}`)
	var buf bytes.Buffer
	_ = renderDebug(&buf, "logs", raw, true)
	if !strings.Contains(buf.String(), `"pod": "web-1"`) {
		t.Fatalf("got %s", buf.String())
	}
}

func TestRenderDebug_LogsPrintsLinesAndHeader(t *testing.T) {
	raw := json.RawMessage(`{"pod":"web-1","container":"app","previous":true,"lines":["boot","killed"],"truncated":true}`)
	var buf bytes.Buffer
	_ = renderDebug(&buf, "logs", raw, false)
	out := buf.String()
	if !strings.Contains(out, "web-1/app (previous container)") || !strings.HasSuffix(out, "boot\nkilled\n") {
		t.Fatalf("got:\n%s", out)
	}
}

func TestExplainDebugError(t *testing.T) {
	cases := map[int]string{401: "keyto auth", 403: "owners and collaborators", 404: "not available", 409: "not enabled"}
	for status, want := range cases {
		err := explainDebugError(&hub.DebugError{Status: status}, "demo-app", "uat")
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%d: got %q, want it to mention %q", status, err, want)
		}
	}
	other := errors.New("dial tcp: refused")
	if explainDebugError(other, "demo-app", "uat") != other {
		t.Fatal("non-DebugError must pass through")
	}
}

func TestParseDebugArgs_SinceRoundsUp(t *testing.T) {
	a, err := parseDebugArgs("events", []string{"demo-app", "--since", "30s"}, noMarker)
	if err != nil || a.SinceMinutes != 1 {
		t.Fatalf("events 30s: %+v %v", a, err)
	}
	a, err = parseDebugArgs("logs", []string{"demo-app", "--since", "500ms"}, noMarker)
	if err != nil || a.SinceSeconds != 1 {
		t.Fatalf("logs 500ms: %+v %v", a, err)
	}
	a, _ = parseDebugArgs("events", []string{"demo-app", "--since", "90s"}, noMarker)
	if a.SinceMinutes != 2 {
		t.Fatalf("90s -> %d", a.SinceMinutes)
	}
}

func TestParseDebugArgs_RejectsNegativeTail(t *testing.T) {
	if _, err := parseDebugArgs("logs", []string{"demo-app", "--tail", "-5"}, noMarker); err == nil {
		t.Fatal("want error for negative --tail")
	}
}

func TestParseDebugArgs_HelpPrintsUsage(t *testing.T) {
	var buf bytes.Buffer
	_, err := parseDebugArgsTo(&buf, "logs", []string{"--help"}, noMarker)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(buf.String(), "usage: keyto logs") || !strings.Contains(buf.String(), "-tail") {
		t.Fatalf("got %q", buf.String())
	}
}

func TestParseDebugArgs_PositionalAfterFlags(t *testing.T) {
	a, err := parseDebugArgs("logs", []string{"--env", "prod", "demo"}, noMarker)
	if err != nil || a.Project != "demo" || a.Env != "prod" {
		t.Fatalf("got %+v %v", a, err)
	}
	if _, err := parseDebugArgs("logs", []string{"--env", "prod", "a", "b"}, noMarker); err == nil {
		t.Fatal("want error for two positionals")
	}
	if _, err := parseDebugArgs("logs", []string{"a", "--env", "prod", "b"}, noMarker); err == nil {
		t.Fatal("want error for project given twice")
	}
}

func TestExplainDebugError_Messages(t *testing.T) {
	cases := []struct {
		de   hub.DebugError
		want string
	}{
		{hub.DebugError{Status: 404, Message: "No such pod in this environment."}, "No such pod in this environment."},
		{hub.DebugError{Status: 404, Message: "Not found."}, "not available"},
		{hub.DebugError{Status: 429}, "too many debug requests"},
		{hub.DebugError{Status: 502, Message: "cluster unreachable"}, "cluster unreachable"},
		{hub.DebugError{Status: 502}, "HTTP 502"},
	}
	for _, c := range cases {
		de := c.de
		got := explainDebugError(&de, "demo-app", "uat").Error()
		if !strings.Contains(got, c.want) {
			t.Fatalf("%+v: got %q want %q", c.de, got, c.want)
		}
	}
}

func render(t *testing.T, kind, raw string, c palette) string {
	t.Helper()
	var buf bytes.Buffer
	if err := renderDebugColour(&buf, kind, json.RawMessage(raw), false, c); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

const overviewBase = `{"env":"uat","pods":[],"warnings":[]`

func TestRenderOverview_NullVsEmptySections(t *testing.T) {
	out := render(t, "overview", overviewBase+`,"argo":null,"externalSecrets":null,"databases":null}`, palette{})
	for _, want := range []string{"ArgoCD status unavailable", "Secret sync: unavailable", "Databases: unavailable"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	out = render(t, "overview", overviewBase+`,"argo":{"sync":"Synced","health":"Healthy"},"externalSecrets":[],"databases":[]}`, palette{})
	for _, want := range []string{"Synced / Healthy", "Secret sync:\n  none", "Databases:\nNo databases."} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "unavailable") {
		t.Fatalf("empty must not read as unavailable:\n%s", out)
	}
}

func TestRenderEvents_EmptyAndSanitised(t *testing.T) {
	if out := render(t, "events", `[]`, palette{}); out != "No events.\n" {
		t.Fatalf("got %q", out)
	}
	raw := `[{"type":"Warning","reason":"BackOff","object":"pod/web-1","message":"line1\nline2\tx","count":1,"lastSeen":"t1"},
	{"type":"Normal","reason":"Pulled","object":"pod/web-1","message":"ok","count":1,"lastSeen":null}]`
	out := render(t, "events", raw, palette{})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "line1 line2 x") {
		t.Fatalf("got:\n%s", out)
	}
	col := strings.Index(lines[0], "TYPE")
	if strings.Index(lines[1], "Warning") != col || strings.Index(lines[2], "Normal") != col {
		t.Fatalf("misaligned:\n%s", out)
	}
}

func TestRenderDbs_EmptyAndPopulated(t *testing.T) {
	if out := render(t, "database", `[]`, palette{}); out != "No databases.\n" {
		t.Fatalf("got %q", out)
	}
	out := render(t, "database", `[{"name":"pg","phase":"Healthy","instances":2,"readyInstances":1,"primary":"pg-1","lastFailover":null}]`, palette{})
	if !strings.Contains(out, "pg: Healthy (1/2 ready, primary pg-1, last failover -)") {
		t.Fatalf("got %q", out)
	}
}

const podsRaw = `[{"name":"web-1","phase":"Running","ready":false,"restarts":3,"containers":[{"name":"app","ready":false,"restarts":3,"state":"waiting","reason":"CrashLoopBackOff","lastTermination":{"reason":"OOMKilled","exitCode":137}}]},
{"name":"worker-long-name","phase":"Running","ready":true,"restarts":0,"containers":[]}]`

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestColour_PlainHasNoEscapes_AndColourStripsToPlain(t *testing.T) {
	plain := render(t, "pods", podsRaw, palette{})
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("plain output has escapes: %q", plain)
	}
	col := render(t, "pods", podsRaw, palette{on: true})
	if !strings.Contains(col, "\x1b[1;31mOOMKilled (exit 137)") || !strings.Contains(col, "\x1b[1;32mtrue") || !strings.Contains(col, "\x1b[1;31mfalse") {
		t.Fatalf("missing colours: %q", col)
	}
	if ansiRE.ReplaceAllString(col, "") != plain {
		t.Fatalf("colour changed layout.\nplain:\n%s\nstripped:\n%s", plain, ansiRE.ReplaceAllString(col, ""))
	}
	lines := strings.Split(plain, "\n")
	c := strings.Index(lines[0], "PHASE")
	if strings.Index(lines[1], "Running") != c || strings.Index(lines[2], "Running") != c {
		t.Fatalf("plain misaligned:\n%s", plain)
	}
}

func TestColour_OverviewAndEvents(t *testing.T) {
	c := palette{on: true}
	out := render(t, "overview", overviewBase+`,"argo":{"sync":"OutOfSync","health":"Progressing"},"externalSecrets":[{"name":"a","ready":true},{"name":"b","ready":false,"message":"boom"}],"databases":[]}`, c)
	for _, want := range []string{"\x1b[1;31mOutOfSync", "\x1b[1;33mProgressing", "\x1b[1;32msynced", "\x1b[1;31mNOT synced: boom"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	ev := render(t, "events", `[{"type":"Warning","reason":"r","object":"o","message":"m","lastSeen":null}]`, c)
	if !strings.Contains(ev, "\x1b[1;31mWarning") || !strings.Contains(ev, "\x1b[2m-") {
		t.Fatalf("got %q", ev)
	}
}

func TestColour_LogLinesUntouched(t *testing.T) {
	out := render(t, "logs", `{"pod":"p","container":"c","lines":["\u001b[31mred app line\u001b[0m"]}`, palette{on: true})
	if !strings.HasSuffix(out, "\x1b[31mred app line\x1b[0m\n") {
		t.Fatalf("got %q", out)
	}
}

func TestRenderDebug_JSONNeverColoured(t *testing.T) {
	var buf bytes.Buffer
	_ = renderDebugColour(&buf, "pods", json.RawMessage(podsRaw), true, palette{on: true})
	if strings.Contains(buf.String(), "\x1b") {
		t.Fatal("json output coloured")
	}
}

func TestSanitizeForTerminal(t *testing.T) {
	cases := []struct {
		name, in string
		colour   bool
		want     string
	}{
		{"osc52 bel", "a\x1b]52;c;aGVsbG8=\x07b", true, "ab"},
		{"osc52 st", "a\x1b]52;c;aGVsbG8=\x1b\\b", false, "ab"},
		{"osc unterminated", "a\x1b]0;title", true, "a"},
		{"osc8 keeps text", "\x1b]8;;http://evil\x07click\x1b]8;;\x07", true, "click"},
		{"csi cursor up + erase", "x\x1b[2A\x1b[2Ky", true, "xy"},
		{"csi with intermediate", "x\x1b[1 qy", true, "xy"},
		{"sgr kept", "\x1b[1;31mred\x1b[0m", true, "\x1b[1;31mred\x1b[0m"},
		{"sgr dropped", "\x1b[1;31mred\x1b[0m", false, "red"},
		{"c1 csi bytes", "a\x9b2Ab", true, "ab"},
		{"c1 csi rune", "a\u009b2Ab", true, "ab"},
		{"c1 osc bytes", "a\x9d52;c;x\x07b", true, "ab"},
		{"c1 dcs", "a\x90qpayload\x1b\\b", true, "ab"},
		{"apc", "a\x1b_payload\x1b\\b", true, "ab"},
		{"lone esc", "a\x1bb", true, "a"},
		{"esc at end", "ab\x1b", true, "ab"},
		{"charset select", "a\x1b(Bb", true, "ab"},
		{"tab kept", "a\tb", false, "a\tb"},
		{"cr bell backspace dropped", "a\rb\x07c\x08d\x7fe", false, "abcde"},
		{"unicode kept", "héllo ✓", true, "héllo ✓"},
		{"csi interrupted by control", "a\x1b[1\x07b", true, "ab"},
	}
	for _, tc := range cases {
		if got := sanitizeForTerminal(tc.in, tc.colour); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestRenderLogs_SanitisesByMode(t *testing.T) {
	raw := `{"pod":"p","container":"c","lines":["\u001b]52;c;eA==\u0007ok \u001b[31mred\u001b[0m\u001b[2J"]}`
	if out := render(t, "logs", raw, palette{on: true}); !strings.Contains(out, "ok \x1b[31mred\x1b[0m\n") || strings.Contains(out, "52;c") || strings.Contains(out, "[2J") {
		t.Fatalf("colour mode: %q", out)
	}
	if out := render(t, "logs", raw, palette{}); !strings.Contains(out, "ok red\n") || strings.Contains(out, "\x1b") {
		t.Fatalf("plain mode: %q", out)
	}
	if out := render(t, "logs", raw, palette{raw: true}); !strings.Contains(out, "\x1b]52;c;eA==\x07ok") {
		t.Fatalf("--raw must pass bytes through: %q", out)
	}
}

func TestRenderLogs_JSONNotSanitised(t *testing.T) {
	raw := `{"pod":"p","container":"c","lines":["\u001b]52;c;eA==\u0007x"]}`
	var buf bytes.Buffer
	_ = renderDebugColour(&buf, "logs", json.RawMessage(raw), true, palette{})
	if !strings.Contains(buf.String(), `\u001b]52`) || strings.Contains(buf.String(), "\x1b") {
		t.Fatalf("json should stay encoded: %q", buf.String())
	}
}

func TestRender_MaliciousServerStringsNeutralised(t *testing.T) {
	evil := `\u001b]52;c;eA==\u0007\u001b[2J`
	for _, c := range []palette{{}, {on: true}} {
		pods := render(t, "pods", `[{"name":"web`+evil+`","phase":"Running`+evil+`","ready":true,"restarts":0,"containers":[]}]`, c)
		ev := render(t, "events", `[{"type":"Warning","reason":"R`+evil+`","object":"o`+evil+`","message":"m`+evil+`","lastSeen":null}]`, c)
		ov := render(t, "overview", overviewBase+`,"argo":{"sync":"Synced","health":"Healthy","conditions":[{"type":"T","message":"msg`+evil+`"}],"unhealthyResources":[{"kind":"K","name":"n`+evil+`","health":"Degraded","message":"m`+evil+`"}]},"externalSecrets":[{"name":"es`+evil+`","ready":false,"message":"bad`+evil+`","reason":null}],"databases":[{"name":"pg`+evil+`","phase":"ph`+evil+`","instances":1,"readyInstances":1,"primary":"p`+evil+`","lastFailover":null}]}`, c)
		for name, out := range map[string]string{"pods": pods, "events": ev, "overview": ov} {
			stripped := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(out, "")
			if strings.Contains(stripped, "\x1b") || strings.Contains(stripped, "\x07") {
				t.Errorf("%s (colour=%v) leaked escape: %q", name, c.on, out)
			}
		}
	}
}

func TestExplainDebugError_SanitisesHubMessage(t *testing.T) {
	err := explainDebugError(&hub.DebugError{Status: 500, Message: "boom\x1b]0;pwn\x07!"}, "p", "uat")
	if strings.Contains(err.Error(), "\x1b") || err.Error() != "boom!" {
		t.Fatalf("got %q", err.Error())
	}
}

func TestParseDebugArgs_RawFlagLogsOnly(t *testing.T) {
	a, err := parseDebugArgs("logs", []string{"demo-app", "--raw"}, noMarker)
	if err != nil || !a.Raw {
		t.Fatalf("got %+v err %v", a, err)
	}
	if _, err := parseDebugArgs("pods", []string{"demo-app", "--raw"}, noMarker); err == nil {
		t.Fatal("--raw must be rejected outside logs")
	}
}
