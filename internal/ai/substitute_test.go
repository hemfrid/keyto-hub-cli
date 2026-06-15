package ai

import (
	"bytes"
	"testing"
)

func TestSubstitute(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"telemetry endpoint",
			"export TELEMETRY_ENDPOINT={{TELEMETRY_ENDPOINT}}",
			"export TELEMETRY_ENDPOINT=https://hub.test/api/telemetry/events",
		},
		{
			"base branch",
			"git merge origin/{{BASE_BRANCH}}",
			"git merge origin/main",
		},
		{
			"both, repeated",
			"{{BASE_BRANCH}} {{TELEMETRY_ENDPOINT}} {{BASE_BRANCH}}",
			"main https://hub.test/api/telemetry/events main",
		},
		{
			"unknown placeholders untouched — /setup owns them",
			"run {{FORMAT_COMMAND}}",
			"run {{FORMAT_COMMAND}}",
		},
	}
	for _, tc := range cases {
		got := Substitute([]byte(tc.in), "https://hub.test/api/telemetry/events", "main")
		if !bytes.Equal(got, []byte(tc.want)) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
