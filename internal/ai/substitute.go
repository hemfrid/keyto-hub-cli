package ai

import "bytes"

// Substitute fills the two placeholders the CLI actually knows. Everything
// else ({{FORMAT_COMMAND}}, {{TRACKER_CONFIG}}, ...) is left for the /setup
// skill to adapt in-session — by design (spec: "CLI installs, /setup adjusts").
//
// Update MUST apply the same substitution with the same pinned inputs before
// hashing, or every bundle file would false-positive as locally modified.
func Substitute(content []byte, telemetryEndpoint, baseBranch string) []byte {
	out := bytes.ReplaceAll(content, []byte("{{TELEMETRY_ENDPOINT}}"), []byte(telemetryEndpoint))
	return bytes.ReplaceAll(out, []byte("{{BASE_BRANCH}}"), []byte(baseBranch))
}
