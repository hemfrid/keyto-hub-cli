package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
			de.Code, de.Message = body.Error, body.Message
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

type DebugOverview struct {
	Env             string                 `json:"env"`
	Argo            *DebugArgo             `json:"argo"`
	Pods            []DebugPod             `json:"pods"`
	Warnings        []DebugEvent           `json:"warnings"`
	ExternalSecrets *[]DebugExternalSecret `json:"externalSecrets"`
	Databases       *[]DebugDbCluster      `json:"databases"`
}

type DebugLogs struct {
	Pod       string   `json:"pod"`
	Container string   `json:"container"`
	Previous  bool     `json:"previous"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}
