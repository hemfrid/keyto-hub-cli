package main

// keyto status|pods|logs|events|db: read-only debugging of a deployed project
// via the Hub (GET /api/projects/<name>/debug/<kind>). Access, limits and the
// default pod for `logs` are decided by the Hub; this file only parses args
// and prints.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
	"github.com/hemfrid/keyto-hub-cli/internal/ui"
)

var debugKindByCommand = map[string]string{
	"status": "overview", "pods": "pods", "logs": "logs", "events": "events", "db": "database",
}

type debugArgs struct {
	Project, Env, Pod, Container string
	Previous, JSON, Raw          bool
	Tail, SinceSeconds           int
	SinceMinutes                 int
}

func markerProjectName() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	m, err := project.Read(dir)
	if err != nil || m == nil {
		return ""
	}
	return m.Name
}

const debugTimeout = 60 * time.Second

// parseDebugArgs accepts the project first (`keyto logs demo --tail 50`), after
// the flags (`keyto logs --env prod demo`), or not at all (falls back to
// ./.keyto/project.json). On -h/--help it prints usage to stdout and returns
// flag.ErrHelp.
func parseDebugArgs(kind string, args []string, markerProject func() string) (debugArgs, error) {
	return parseDebugArgsTo(os.Stdout, kind, args, markerProject)
}

func parseDebugArgsTo(out io.Writer, kind string, args []string, markerProject func() string) (debugArgs, error) {
	a := debugArgs{}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		a.Project, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(kind, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&a.Env, "env", "uat", "uat|prod")
	fs.BoolVar(&a.JSON, "json", false, "machine-readable output")
	var since string
	if kind == "logs" {
		fs.StringVar(&a.Pod, "pod", "", "pod name (default: most recently restarted)")
		fs.StringVar(&a.Container, "container", "", "container name")
		fs.BoolVar(&a.Raw, "raw", false, "print log lines byte-for-byte (trusts the app's output: it can carry terminal escape sequences)")
		fs.BoolVar(&a.Previous, "previous", false, "read the previous (crashed) container")
		fs.IntVar(&a.Tail, "tail", 0, "lines (default 200, max 2000)")
		fs.StringVar(&since, "since", "", "only newer than this duration, e.g. 30m, 1h")
	}
	if kind == "events" {
		fs.StringVar(&since, "since", "", "window, e.g. 15m (max 60m)")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(out, "usage: keyto %s [project] [flags]\n", commandFor(kind))
			fs.SetOutput(out)
			fs.PrintDefaults()
			return a, flag.ErrHelp
		}
		return a, fmt.Errorf("%s: %w", kind, err)
	}
	switch {
	case fs.NArg() > 1, fs.NArg() == 1 && a.Project != "":
		return a, fmt.Errorf("%s: unexpected argument %q", kind, fs.Arg(fs.NArg()-1))
	case fs.NArg() == 1:
		a.Project = fs.Arg(0)
	}
	if a.Env != "uat" && a.Env != "prod" {
		return a, fmt.Errorf("--env must be uat or prod")
	}
	if a.Tail < 0 {
		return a, fmt.Errorf("--tail must not be negative")
	}
	if since != "" {
		d, err := time.ParseDuration(since)
		if err != nil || d <= 0 {
			return a, fmt.Errorf("--since: %q is not a duration like 30m or 1h", since)
		}
		// Round UP: truncating a sub-unit window to 0 would drop the param and
		// make the Hub use its (much wider) maximum.
		if kind == "logs" {
			a.SinceSeconds = int(math.Ceil(d.Seconds()))
		} else {
			a.SinceMinutes = int(math.Ceil(d.Minutes()))
		}
	}
	if a.Project == "" {
		a.Project = markerProject()
	}
	if a.Project == "" {
		return a, errors.New("no project given and no .keyto/project.json here; usage: keyto " + commandFor(kind) + " <project>")
	}
	return a, nil
}

func commandFor(kind string) string {
	for cmd, k := range debugKindByCommand {
		if k == kind {
			return cmd
		}
	}
	return kind
}

func debugQuery(kind string, a debugArgs) url.Values {
	q := url.Values{"env": {a.Env}}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	if kind == "logs" {
		set("pod", a.Pod)
		set("container", a.Container)
		if a.Previous {
			q.Set("previous", "true")
		}
		if a.Tail > 0 {
			q.Set("tail", strconv.Itoa(a.Tail))
		}
		if a.SinceSeconds > 0 {
			q.Set("sinceSeconds", strconv.Itoa(a.SinceSeconds))
		}
	}
	if kind == "events" && a.SinceMinutes > 0 {
		q.Set("sinceMinutes", strconv.Itoa(a.SinceMinutes))
	}
	return q
}

func explainDebugError(err error, projectName, env string) error {
	var de *hub.DebugError
	if !errors.As(err, &de) {
		return err
	}
	switch de.Status {
	case 401:
		return errors.New("not signed in to the Hub (or the credential expired): run `keyto auth`")
	case 403:
		return fmt.Errorf("only %s's owners and collaborators can debug it", projectName)
	case 404:
		// A specific Hub message ("No such pod in this environment.") is worth
		// printing; the generic one, or none, means feature-off / unknown project.
		if de.Message != "" && de.Message != "Not found." {
			return errors.New(sanitizeForTerminal(de.Message, false))
		}
		return fmt.Errorf("not available on this Hub, or the project %q doesn't exist", projectName)
	case 409:
		return fmt.Errorf("debug access is not enabled for %s (%s) yet; it switches on after the project's next platform sync", projectName, env)
	case 429:
		return errors.New("too many debug requests; wait a minute and retry")
	}
	if de.Message != "" {
		return errors.New(sanitizeForTerminal(de.Message, false))
	}
	return fmt.Errorf("the Hub could not answer (HTTP %d); try again shortly", de.Status)
}

func str(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}

// palette colours the CLI's own output. Disabled (plain) when piped, with
// --json, or under NO_COLOR. raw (`keyto logs --raw`) prints log lines
// unsanitised. Server-provided text goes through s() before it is printed.
type palette struct{ on, raw bool }

// s neutralises terminal escape sequences in a server-provided string.
func (c palette) s(x string) string { return sanitizeForTerminal(x, false) }

func (c palette) wrap(code, s string) string {
	if !c.on || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func (c palette) red(s string) string    { return c.wrap("1;31", s) }
func (c palette) yellow(s string) string { return c.wrap("1;33", s) }
func (c palette) green(s string) string  { return c.wrap("1;32", s) }
func (c palette) dim(s string) string    { return c.wrap("2", s) }

func stdoutPalette(asJSON bool) palette {
	_, noColor := os.LookupEnv("NO_COLOR")
	return palette{on: !asJSON && !noColor && ui.IsStdoutTTY()}
}

// stateTone colours well-known Argo/pod state words; unknown words stay plain.
func (c palette) stateTone(s string) string {
	switch s {
	case "Healthy", "Synced", "Running", "Succeeded":
		return c.green(s)
	case "Degraded", "OutOfSync", "Failed", "Error", "Missing", "OOMKilled", "CrashLoopBackOff":
		return c.red(s)
	case "Progressing", "Pending", "Suspended", "Unknown", "waiting":
		return c.yellow(s)
	}
	return s
}

func (c palette) boolTone(b bool) string {
	if b {
		return c.green("true")
	}
	return c.red("false")
}

func (c palette) dimDash(s string) string {
	if s == "-" {
		return c.dim(s)
	}
	return s
}

// cleanCell keeps one record on one table line.
var cellCleaner = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")

func cleanCell(s string) string { return sanitizeForTerminal(cellCleaner.Replace(s), false) }

type cell struct {
	text  string
	style func(string) string // applied after padding is computed; nil = plain
}

// writeTable pads on the PLAIN text (two-space gutter, last column unpadded,
// matching text/tabwriter) and colours afterwards, so ANSI codes never skew
// alignment.
func writeTable(w io.Writer, header []string, rows [][]cell) {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len([]rune(h))
	}
	for _, r := range rows {
		for i := range r {
			r[i].text = cleanCell(r[i].text)
			if n := len([]rune(r[i].text)); n > widths[i] {
				widths[i] = n
			}
		}
	}
	line := func(cells []cell) {
		var b strings.Builder
		for i, c := range cells {
			t := c.text
			if c.style != nil {
				t = c.style(t)
			}
			b.WriteString(t)
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-len([]rune(c.text))+2))
			}
		}
		fmt.Fprintln(w, b.String())
	}
	hc := make([]cell, len(header))
	for i, h := range header {
		hc[i] = cell{text: h}
	}
	line(hc)
	for _, r := range rows {
		line(r)
	}
}

func lastProblem(p hub.DebugPod) string {
	for _, c := range p.Containers {
		if t := c.LastTermination; t != nil && t.Reason != nil {
			if t.ExitCode != nil {
				return fmt.Sprintf("%s (exit %d)", *t.Reason, *t.ExitCode)
			}
			return *t.Reason
		}
		if c.Reason != nil {
			return *c.Reason
		}
	}
	return "-"
}

func renderPods(w io.Writer, c palette, pods []hub.DebugPod) {
	rows := make([][]cell, 0, len(pods))
	for _, p := range pods {
		phase := c.stateTone
		if p.Phase == "Running" && !p.Ready {
			phase = c.yellow
		}
		lp := lastProblem(p)
		lpStyle := c.red
		if lp == "-" {
			lpStyle = c.dim
		}
		rows = append(rows, []cell{
			{p.Name, nil}, {p.Phase, phase},
			{strconv.FormatBool(p.Ready), func(string) string { return c.boolTone(p.Ready) }},
			{strconv.Itoa(p.Restarts), nil}, {lp, lpStyle},
		})
	}
	writeTable(w, []string{"NAME", "PHASE", "READY", "RESTARTS", "LAST PROBLEM"}, rows)
}

func renderEvents(w io.Writer, c palette, events []hub.DebugEvent) {
	if len(events) == 0 {
		fmt.Fprintln(w, "No events.")
		return
	}
	rows := make([][]cell, 0, len(events))
	for _, e := range events {
		typ := func(s string) string { return s }
		if e.Type == "Warning" {
			typ = c.red
		}
		rows = append(rows, []cell{
			{str(e.LastSeen, "-"), c.dim}, {e.Type, typ}, {e.Reason, nil}, {e.Object, nil}, {e.Message, nil},
		})
	}
	writeTable(w, []string{"LAST SEEN", "TYPE", "REASON", "OBJECT", "MESSAGE"}, rows)
}

func renderDbs(w io.Writer, c palette, dbs []hub.DebugDbCluster) {
	if len(dbs) == 0 {
		fmt.Fprintln(w, "No databases.")
		return
	}
	for _, d := range dbs {
		ready, total := "?", "?"
		if d.ReadyInstances != nil {
			ready = strconv.Itoa(*d.ReadyInstances)
		}
		if d.Instances != nil {
			total = strconv.Itoa(*d.Instances)
		}
		fmt.Fprintf(w, "%s: %s (%s/%s ready, primary %s, last failover %s)\n",
			c.s(d.Name), c.s(str(d.Phase, "unknown")), ready, total, c.s(str(d.Primary, "?")), c.dimDash(c.s(str(d.LastFailover, "-"))))
	}
}

func renderOverview(w io.Writer, c palette, o hub.DebugOverview) {
	if o.Argo != nil {
		fmt.Fprintf(w, "Deployment (%s): %s / %s\n", c.s(o.Env), c.stateTone(c.s(o.Argo.Sync)), c.stateTone(c.s(o.Argo.Health)))
		if o.Argo.LastOperation != nil && o.Argo.LastOperation.Message != nil {
			fmt.Fprintf(w, "  last sync: %s: %s\n", c.s(o.Argo.LastOperation.Phase), c.s(*o.Argo.LastOperation.Message))
		}
		for _, cd := range o.Argo.Conditions {
			fmt.Fprintf(w, "  %s: %s\n", c.s(cd.Type), c.s(cd.Message))
		}
		for _, r := range o.Argo.UnhealthyResources {
			fmt.Fprintf(w, "  %s/%s: %s %s\n", c.s(r.Kind), c.s(r.Name), c.stateTone(c.s(r.Health)), c.s(str(r.Message, "")))
		}
	} else {
		fmt.Fprintf(w, "Deployment (%s): ArgoCD status unavailable\n", c.s(o.Env))
	}
	fmt.Fprintln(w, "\nPods:")
	renderPods(w, c, o.Pods)
	fmt.Fprintln(w, "\nWarning events (last hour):")
	renderEvents(w, c, o.Warnings)
	if o.ExternalSecrets == nil {
		fmt.Fprintln(w, "\nSecret sync: unavailable")
	} else {
		fmt.Fprintln(w, "\nSecret sync:")
		if len(*o.ExternalSecrets) == 0 {
			fmt.Fprintln(w, "  none")
		}
		for _, s := range *o.ExternalSecrets {
			state := c.green("synced")
			if !s.Ready {
				state = c.red("NOT synced: " + c.s(str(s.Message, str(s.Reason, "unknown"))))
			}
			fmt.Fprintf(w, "  %s: %s\n", c.s(s.Name), state)
		}
	}
	if o.Databases == nil {
		fmt.Fprintln(w, "\nDatabases: unavailable")
	} else {
		fmt.Fprintln(w, "\nDatabases:")
		renderDbs(w, c, *o.Databases)
	}
}

func renderDebug(w io.Writer, kind string, raw json.RawMessage, asJSON bool) error {
	return renderDebugColour(w, kind, raw, asJSON, palette{})
}

func renderDebugColour(w io.Writer, kind string, raw json.RawMessage, asJSON bool, c palette) error {
	if asJSON {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	switch kind {
	case "pods":
		var pods []hub.DebugPod
		if err := json.Unmarshal(raw, &pods); err != nil {
			return err
		}
		renderPods(w, c, pods)
	case "events":
		var ev []hub.DebugEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return err
		}
		renderEvents(w, c, ev)
	case "database":
		var dbs []hub.DebugDbCluster
		if err := json.Unmarshal(raw, &dbs); err != nil {
			return err
		}
		renderDbs(w, c, dbs)
	case "logs":
		var l hub.DebugLogs
		if err := json.Unmarshal(raw, &l); err != nil {
			return err
		}
		which := "current container"
		if l.Previous {
			which = "previous container"
		}
		fmt.Fprintf(w, "==> %s/%s (%s)\n", c.s(l.Pod), c.s(l.Container), which)
		if l.Truncated {
			fmt.Fprintln(w, c.dim("… earlier lines omitted (use --tail up to 2000) …"))
		}
		for _, line := range l.Lines {
			switch {
			case c.raw: // opt-out: byte-for-byte
				fmt.Fprintln(w, line)
			default: // app output is untrusted: keep only SGR colour, and only on a colour TTY
				fmt.Fprintln(w, sanitizeForTerminal(line, c.on))
			}
		}
	case "overview":
		var o hub.DebugOverview
		if err := json.Unmarshal(raw, &o); err != nil {
			return err
		}
		renderOverview(w, c, o)
	}
	return nil
}

var runDebug = func(ctx context.Context, kind string, args []string) error {
	a, err := parseDebugArgs(kind, args, markerProjectName)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, debugTimeout)
	defer cancel()
	creds, err := config.Load()
	if errors.Is(err, config.ErrNotAuthed) {
		return errors.New("not signed in to the Hub: run `keyto auth`")
	}
	if err != nil {
		return err
	}
	c := &hub.Client{BaseURL: creds.HubURL, Credential: creds.Credential}
	var raw json.RawMessage
	if err := c.Debug(ctx, a.Project, kind, debugQuery(kind, a), &raw); err != nil {
		return explainDebugError(err, a.Project, a.Env)
	}
	pal := stdoutPalette(a.JSON)
	pal.raw = a.Raw && !a.JSON
	return renderDebugColour(os.Stdout, kind, raw, a.JSON, pal)
}
