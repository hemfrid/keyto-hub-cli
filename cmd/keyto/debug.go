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
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hemfrid/keyto-hub-cli/internal/config"
	"github.com/hemfrid/keyto-hub-cli/internal/hub"
	"github.com/hemfrid/keyto-hub-cli/internal/project"
)

var debugKindByCommand = map[string]string{
	"status": "overview", "pods": "pods", "logs": "logs", "events": "events", "db": "database",
}

type debugArgs struct {
	Project, Env, Pod, Container string
	Previous, JSON               bool
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

// parseDebugArgs accepts the project either first (`keyto logs demo --tail 50`)
// or not at all (falls back to ./.keyto/project.json).
func parseDebugArgs(kind string, args []string, markerProject func() string) (debugArgs, error) {
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
		fs.BoolVar(&a.Previous, "previous", false, "read the previous (crashed) container")
		fs.IntVar(&a.Tail, "tail", 0, "lines (default 200, max 2000)")
		fs.StringVar(&since, "since", "", "only newer than this duration, e.g. 30m, 1h")
	}
	if kind == "events" {
		fs.StringVar(&since, "since", "", "window, e.g. 15m (max 60m)")
	}
	if err := fs.Parse(args); err != nil {
		return a, fmt.Errorf("%s: %w", kind, err)
	}
	if fs.NArg() > 0 {
		return a, fmt.Errorf("%s: unexpected argument %q", kind, fs.Arg(0))
	}
	if a.Env != "uat" && a.Env != "prod" {
		return a, fmt.Errorf("--env must be uat or prod")
	}
	if since != "" {
		d, err := time.ParseDuration(since)
		if err != nil || d <= 0 {
			return a, fmt.Errorf("--since: %q is not a duration like 30m or 1h", since)
		}
		if kind == "logs" {
			a.SinceSeconds = int(d.Seconds())
		} else {
			a.SinceMinutes = int(d.Minutes())
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
			return errors.New(de.Message)
		}
		return fmt.Errorf("not available on this Hub, or the project %q doesn't exist", projectName)
	case 409:
		return fmt.Errorf("debug access is not enabled for %s (%s) yet; it switches on after the project's next platform sync", projectName, env)
	case 429:
		return errors.New("too many debug requests; wait a minute and retry")
	}
	if de.Message != "" {
		return errors.New(de.Message)
	}
	return fmt.Errorf("the Hub could not answer (HTTP %d); try again shortly", de.Status)
}

func str(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
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

func renderPods(w io.Writer, pods []hub.DebugPod) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPHASE\tREADY\tRESTARTS\tLAST PROBLEM")
	for _, p := range pods {
		fmt.Fprintf(tw, "%s\t%s\t%t\t%d\t%s\n", p.Name, p.Phase, p.Ready, p.Restarts, lastProblem(p))
	}
	_ = tw.Flush()
}

func renderEvents(w io.Writer, events []hub.DebugEvent) {
	if len(events) == 0 {
		fmt.Fprintln(w, "No events.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "LAST SEEN\tTYPE\tREASON\tOBJECT\tMESSAGE")
	for _, e := range events {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", str(e.LastSeen, "-"), e.Type, e.Reason, e.Object, e.Message)
	}
	_ = tw.Flush()
}

func renderDbs(w io.Writer, dbs []hub.DebugDbCluster) {
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
			d.Name, str(d.Phase, "unknown"), ready, total, str(d.Primary, "?"), str(d.LastFailover, "-"))
	}
}

func renderDebug(w io.Writer, kind string, raw json.RawMessage, asJSON bool) error {
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
		renderPods(w, pods)
	case "events":
		var ev []hub.DebugEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return err
		}
		renderEvents(w, ev)
	case "database":
		var dbs []hub.DebugDbCluster
		if err := json.Unmarshal(raw, &dbs); err != nil {
			return err
		}
		renderDbs(w, dbs)
	case "logs":
		var l hub.DebugLogs
		if err := json.Unmarshal(raw, &l); err != nil {
			return err
		}
		which := "current container"
		if l.Previous {
			which = "previous container"
		}
		fmt.Fprintf(w, "==> %s/%s (%s)\n", l.Pod, l.Container, which)
		if l.Truncated {
			fmt.Fprintln(w, "… earlier lines omitted (use --tail up to 2000) …")
		}
		for _, line := range l.Lines {
			fmt.Fprintln(w, line)
		}
	case "overview":
		var o hub.DebugOverview
		if err := json.Unmarshal(raw, &o); err != nil {
			return err
		}
		if o.Argo != nil {
			fmt.Fprintf(w, "Deployment (%s): %s / %s\n", o.Env, o.Argo.Sync, o.Argo.Health)
			if o.Argo.LastOperation != nil && o.Argo.LastOperation.Message != nil {
				fmt.Fprintf(w, "  last sync: %s: %s\n", o.Argo.LastOperation.Phase, *o.Argo.LastOperation.Message)
			}
			for _, c := range o.Argo.Conditions {
				fmt.Fprintf(w, "  %s: %s\n", c.Type, c.Message)
			}
			for _, r := range o.Argo.UnhealthyResources {
				fmt.Fprintf(w, "  %s/%s: %s %s\n", r.Kind, r.Name, r.Health, str(r.Message, ""))
			}
		} else {
			fmt.Fprintf(w, "Deployment (%s): ArgoCD status unavailable\n", o.Env)
		}
		fmt.Fprintln(w, "\nPods:")
		renderPods(w, o.Pods)
		fmt.Fprintln(w, "\nWarning events (last hour):")
		renderEvents(w, o.Warnings)
		if o.ExternalSecrets != nil {
			fmt.Fprintln(w, "\nSecret sync:")
			for _, s := range *o.ExternalSecrets {
				state := "synced"
				if !s.Ready {
					state = "NOT synced: " + str(s.Message, str(s.Reason, "unknown"))
				}
				fmt.Fprintf(w, "  %s: %s\n", s.Name, state)
			}
		}
		if o.Databases != nil {
			fmt.Fprintln(w, "\nDatabases:")
			renderDbs(w, *o.Databases)
		}
	}
	return nil
}

var runDebug = func(ctx context.Context, kind string, args []string) error {
	a, err := parseDebugArgs(kind, args, markerProjectName)
	if err != nil {
		return err
	}
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
	return renderDebug(os.Stdout, kind, raw, a.JSON)
}
