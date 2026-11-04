package prereq

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// A Workers project runs wrangler 4, which refuses Node below 22.0.0 (the
// Workers template's engines.node is >=22). NodeWorkers carries that floor;
// Node keeps the Next.js floor of 20.9.

func TestEnsure_NodeWorkers_Node20Refused(t *testing.T) {
	for _, v := range []string{"v20.20.2", "v21.7.3"} {
		t.Run(v, func(t *testing.T) {
			d, ran := fakeEnv(map[string]string{"git": "2.40", "node": v, "brew": ""}, true, true, nil)
			d.Out = &bytes.Buffer{}
			err := Ensure(context.Background(), []Tool{NodeWorkers}, Opts{Deps: *d, AutoYes: true})
			if err == nil {
				t.Fatalf("Node %s must be refused for a Workers project", v)
			}
			if len(*ran) != 0 {
				t.Fatalf("must not install over an existing Node, ran: %v", *ran)
			}
			msg := err.Error()
			for _, want := range []string{v, ">=22", "Workers", "wrangler", "nvm install 24"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error should contain %q, got: %v", want, msg)
				}
			}
		})
	}
}

func TestEnsure_NodeWorkers_Node22AndUpAccepted(t *testing.T) {
	for _, v := range []string{"v22.0.0", "v22.11.0", "v24.2.0", "v25.0.0"} {
		t.Run(v, func(t *testing.T) {
			d, ran := fakeEnv(map[string]string{"node": v}, true, false, nil)
			d.Out = &bytes.Buffer{}
			if err := Ensure(context.Background(), []Tool{NodeWorkers}, Opts{Deps: *d}); err != nil {
				t.Fatalf("Node %s should pass the Workers floor, got: %v", v, err)
			}
			if len(*ran) != 0 {
				t.Fatalf("must not install, ran: %v", *ran)
			}
		})
	}
}

func TestEnsure_NodeWorkers_MissingInstallsNode24(t *testing.T) {
	d, ran := fakeEnv(map[string]string{"git": "2.40", "brew": ""}, true, true, nil)
	d.Out = &bytes.Buffer{}
	if err := Ensure(context.Background(), []Tool{NodeWorkers}, Opts{Deps: *d}); err != nil {
		t.Fatalf("accepted install should succeed, got: %v", err)
	}
	if strings.Join(*ran, "|") != "brew install node@24" {
		t.Fatalf("want the node@24 brew install, ran: %v", *ran)
	}
}

// The Next.js floor is unchanged: Node 20.9+ still passes plain Node.
func TestEnsure_Node_NextJSFloorUnchanged(t *testing.T) {
	d, _ := fakeEnv(map[string]string{"node": "v20.9.0"}, true, false, nil)
	d.Out = &bytes.Buffer{}
	if err := Ensure(context.Background(), []Tool{Node}, Opts{Deps: *d}); err != nil {
		t.Fatalf("Node 20.9 must still pass the Next.js floor, got: %v", err)
	}
	d, _ = fakeEnv(map[string]string{"node": "v20.8.1"}, true, false, nil)
	d.Out = &bytes.Buffer{}
	err := Ensure(context.Background(), []Tool{Node}, Opts{Deps: *d})
	if err == nil || !strings.Contains(err.Error(), ">=20.9") || strings.Contains(err.Error(), "Workers") {
		t.Fatalf("want the unchanged >=20.9 Next.js error, got: %v", err)
	}
}

func TestDiagnose_NodeWorkers_Node20IsWrongVersion(t *testing.T) {
	o := diagOpts("darwin", map[string]string{"node": "v20.20.2", "brew": ""}, true, true, nil)
	c := findCheck(t, Diagnose(context.Background(), []Tool{NodeWorkers}, o), "node")
	if c.Status != StatusWrongVersion {
		t.Fatalf("status = %q, want wrong_version", c.Status)
	}
	if !strings.Contains(c.Detail, "v20.20.2") || !strings.Contains(c.Detail, ">=22") || !strings.Contains(c.Detail, "wrangler") {
		t.Errorf("detail should name the version, the >=22 floor and why, got %q", c.Detail)
	}
	if c.Fix != "brew install node@24" || c.FixType != FixAuto {
		t.Errorf("fix = %q (%s), want the auto node@24 install", c.Fix, c.FixType)
	}
}

func TestDiagnose_NodeWorkers_Node22OKAndMissingNamesFloor(t *testing.T) {
	o := diagOpts("darwin", map[string]string{"node": "v22.0.0"}, true, true, nil)
	if c := findCheck(t, Diagnose(context.Background(), []Tool{NodeWorkers}, o), "node"); c.Status != StatusOK {
		t.Fatalf("Node 22.0.0 status = %q, want ok", c.Status)
	}
	o = diagOpts("darwin", map[string]string{"brew": ""}, true, true, nil)
	c := findCheck(t, Diagnose(context.Background(), []Tool{NodeWorkers}, o), "node")
	if c.Status != StatusMissing || !strings.Contains(c.Detail, ">=22") {
		t.Fatalf("missing node should name the >=22 floor, got %+v", c)
	}
}

func TestInstallMethod_NodeWorkers_SameAsNode(t *testing.T) {
	for _, os := range []string{"darwin", "linux", "windows"} {
		o := Opts{Deps: Deps{OS: os, HasCommand: func(n string) bool { return n == "brew" || n == "apt-get" }}}
		d1, c1, a1 := o.installMethod(Node)
		d2, c2, a2 := o.installMethod(NodeWorkers)
		if d1 != d2 || strings.Join(c1, " ") != strings.Join(c2, " ") || a1 != a2 {
			t.Errorf("%s: NodeWorkers install (%q) differs from Node (%q)", os, d2, d1)
		}
	}
}
