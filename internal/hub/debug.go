package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hemfrid/keyto-hub-cli/internal/termsafe"
)

// DebugError is a non-200 answer from the Hub's debug API. Code/Message come
// from the Hub's {error, message} body (our own text, safe to print); they are
// empty when the body was not that JSON, and the raw body is never kept.
type DebugError struct {
	Status  int
	Code    string
	Message string
}

func (e *DebugError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("hub debug: %s (%d)", e.Message, e.Status)
	}
	return fmt.Sprintf("hub debug: HTTP %d", e.Status)
}

// Debug GETs /api/projects/<project>/debug/<kind>?<q> and decodes into out.
func (c *Client) Debug(ctx context.Context, project, kind string, q url.Values, out any) error {
	u := fmt.Sprintf("%s/api/projects/%s/debug/%s", c.BaseURL, url.PathEscape(project), url.PathEscape(kind))
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("build debug request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.Credential)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("debug request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		de := &DebugError{Status: resp.StatusCode}
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.NewDecoder(resp.Body).Decode(&body) == nil {
			de.Code = termsafe.Sanitize(body.Error, false)
			de.Message = termsafe.Sanitize(body.Message, false)
		}
		return de
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode debug response: %w", err)
	}
	return nil
}

type DebugTermination struct {
	Reason     *string `json:"reason"`
	ExitCode   *int    `json:"exitCode"`
	FinishedAt *string `json:"finishedAt"`
}

type DebugContainer struct {
	Name            string            `json:"name"`
	Ready           bool              `json:"ready"`
	Restarts        int               `json:"restarts"`
	State           string            `json:"state"`
	Reason          *string           `json:"reason"`
	LastTermination *DebugTermination `json:"lastTermination"`
}

type DebugPod struct {
	Name       string           `json:"name"`
	Phase      string           `json:"phase"`
	Ready      bool             `json:"ready"`
	Restarts   int              `json:"restarts"`
	StartedAt  *string          `json:"startedAt"`
	Image      *string          `json:"image"`
	Containers []DebugContainer `json:"containers"`
}

type DebugEvent struct {
	Type     string  `json:"type"`
	Reason   string  `json:"reason"`
	Object   string  `json:"object"`
	Message  string  `json:"message"`
	Count    int     `json:"count"`
	LastSeen *string `json:"lastSeen"`
}

type DebugDbCluster struct {
	Name           string  `json:"name"`
	Phase          *string `json:"phase"`
	Instances      *int    `json:"instances"`
	ReadyInstances *int    `json:"readyInstances"`
	Primary        *string `json:"primary"`
	LastFailover   *string `json:"lastFailover"`
}

type DebugExternalSecret struct {
	Name     string  `json:"name"`
	Ready    bool    `json:"ready"`
	Reason   *string `json:"reason"`
	Message  *string `json:"message"`
	LastSync *string `json:"lastSync"`
}

type DebugArgo struct {
	Sync       string `json:"sync"`
	Health     string `json:"health"`
	Conditions []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"conditions"`
	UnhealthyResources []struct {
		Kind    string  `json:"kind"`
		Name    string  `json:"name"`
		Health  string  `json:"health"`
		Message *string `json:"message"`
	} `json:"unhealthyResources"`
	LastOperation *struct {
		Phase   string  `json:"phase"`
		Message *string `json:"message"`
	} `json:"lastOperation"`
}

// A Workers project's runtime prints a status line once a minute; the Hub
// passes on the newest one (keyto-hub lib/workers-status.ts). Its keys are a
// floor: the runtime may add keys, which decoding ignores. Every string in it
// is the app's own claim, so it is printed sanitised like any server text.
// Sizes and counts are JSON numbers, kept as float64 so an unexpected
// fraction can't fail the whole decode.
type DebugWorkersD1 struct {
	Binding    string   `json:"binding"`
	Bytes      float64  `json:"bytes"`
	Migrations []string `json:"migrations"`
}

type DebugWorkersKV struct {
	Binding string  `json:"binding"`
	Keys    float64 `json:"keys"`
}

type DebugWorkersSnapshot struct {
	Key     string `json:"key"`
	TakenAt string `json:"takenAt"`
	Reason  string `json:"reason"`
}

type DebugWorkersStatus struct {
	Runtime      string                `json:"runtime"`
	Workerd      string                `json:"workerd"`
	D1           []DebugWorkersD1      `json:"d1"`
	KV           []DebugWorkersKV      `json:"kv"`
	LastSnapshot *DebugWorkersSnapshot `json:"lastSnapshot"`
}

// DebugD1 is a Workers project's database view. Status is nil when the
// runtime logged no status line in the Hub's window.
type DebugD1 struct {
	Status *DebugWorkersStatus `json:"status"`
}

// DebugDatabase is GET …/debug/database: the bare CNPG cluster array for a
// Next.js project, or {"kind":"d1","status":…} for a Workers project. D1 is
// nil for the array.
type DebugDatabase struct {
	Clusters []DebugDbCluster
	D1       *DebugD1
}

// ErrUnknownDatabaseShape is a database answer that is neither shape, e.g. a
// kind a newer Hub added.
var ErrUnknownDatabaseShape = errors.New("the Hub answered a database shape this keyto doesn't know; run `keyto update`")

func (d *DebugDatabase) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimLeft(b, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		*d = DebugDatabase{}
		return json.Unmarshal(b, &d.Clusters)
	}
	var obj struct {
		Kind string `json:"kind"`
		DebugD1
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	if obj.Kind != "d1" {
		return ErrUnknownDatabaseShape
	}
	*d = DebugDatabase{D1: &obj.DebugD1}
	return nil
}

type DebugOverview struct {
	Env             string                 `json:"env"`
	Argo            *DebugArgo             `json:"argo"`
	Pods            []DebugPod             `json:"pods"`
	Warnings        []DebugEvent           `json:"warnings"`
	ExternalSecrets *[]DebugExternalSecret `json:"externalSecrets"`
	Databases       *[]DebugDbCluster      `json:"databases"`
	// D1 is a Workers project's runtime status. HasD1 tells a Workers
	// overview whose D1 is null (status unavailable) from a Next.js one,
	// which has no d1 key at all.
	D1    *DebugD1 `json:"d1"`
	HasD1 bool     `json:"-"`
}

func (o *DebugOverview) UnmarshalJSON(b []byte) error {
	type plain DebugOverview
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		return err
	}
	_, p.HasD1 = keys["d1"]
	*o = DebugOverview(p)
	return nil
}

type DebugLogs struct {
	Pod       string   `json:"pod"`
	Container string   `json:"container"`
	Previous  bool     `json:"previous"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}
