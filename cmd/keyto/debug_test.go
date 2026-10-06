package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
