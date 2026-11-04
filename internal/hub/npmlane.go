package hub

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"
)

// npmLaneProbePath is one of the lane's allow-listed packages. The route
// checks the credential before it parses the path, so any path under the lane
// answers 401 without one.
const (
	npmLaneProbePath    = "/api/npm/@hemfrid/workers-auth"
	npmLaneProbeTimeout = 5 * time.Second
	npmLaneDrainLimit   = 4096
)

// NpmLaneServed reports whether this Hub serves the @hemfrid package lane
// (keyto-hub#825). It asks without a credential, so the lane answers 401 with
// a JSON body; a Hub without it answers the Next.js 404 page. detail names
// what came back when the answer is no. The credential is never sent.
func (c *Client) NpmLaneServed(ctx context.Context) (served bool, detail string) {
	ctx, cancel := context.WithTimeout(ctx, npmLaneProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+npmLaneProbePath, nil)
	if err != nil {
		return false, "bad Hub URL"
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return false, "Hub unreachable"
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, npmLaneDrainLimit))
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode == http.StatusUnauthorized && mt == "application/json" {
		return true, ""
	}
	return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
}
