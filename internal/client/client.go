// Package client is the generated API client as the commands use it: pointed at
// an installation, authenticated with its key, saying which CLI and API level
// it is, and turning what the API answers into errors with exit codes.
package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hivepaas/hivepaas-cli/internal/api"
	"github.com/hivepaas/hivepaas-cli/internal/config"
	"github.com/hivepaas/hivepaas-cli/internal/version"
)

const (
	// apiPath is where an installation serves its API.
	apiPath = "/api"
	// HeaderCLI tells the server which CLI, built for which API level, is
	// calling: it refuses writes from a CLI below its own level.
	HeaderCLI = "HivePaaS-CLI"
	// HeaderAPILevel is the server's level, answered to every CLI request.
	HeaderAPILevel = "HivePaaS-API-Level"

	headerKeyID  = "HIVEPAAS-API-KEY-ID"
	headerSecret = "HIVEPAAS-API-SECRET-KEY" //nolint:gosec // a header's name

	requestTimeout = 60 * time.Second
)

// Client is the generated client, and what a hand-written call needs: the API's
// URL and the headers every request carries.
type Client struct {
	*api.ClientWithResponses

	// BaseURL is the installation's API: https://paas.example.com/api.
	BaseURL string
	Target  *config.Target

	http *http.Client
}

// Options are what a client is made with besides its target.
type Options struct {
	// Debug, when set, gets a line for each request and response.
	Debug io.Writer
	// Warn is told, once, that the server's API is newer than this CLI's.
	Warn func(message string)
}

// New is a client of target.
func New(target *config.Target, opts Options) (*Client, error) {
	transport := &transport{
		base:   http.DefaultTransport,
		header: Headers(target),
		debug:  opts.Debug,
		warn:   opts.Warn,
	}
	httpClient := &http.Client{Transport: transport, Timeout: requestTimeout}
	baseURL := target.URL + apiPath
	generated, err := api.NewClientWithResponses(baseURL+"/", api.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("making the API client for %s: %w", baseURL, err)
	}
	return &Client{ClientWithResponses: generated, BaseURL: baseURL, Target: target, http: httpClient}, nil
}

// Headers are what every request to target carries: the key, and which CLI is
// asking. The websocket streams send them on their upgrade request.
func Headers(target *config.Target) http.Header {
	header := http.Header{}
	header.Set(headerKeyID, target.KeyID)
	header.Set(headerSecret, target.Secret)
	header.Set(HeaderCLI, fmt.Sprintf("%s; api-level=%d", version.Version, api.APILevel))
	header.Set("User-Agent", "hivepaas-cli/"+version.Version)
	return header
}

// Raw sends a request the generated client has no method for: the `api`
// command. path is under the API, such as /projects.
func (c *Client) Raw(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/"+strings.TrimPrefix(path, "/"), body)
	if err != nil {
		return nil, fmt.Errorf("making the request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, unreachable(err)
	}
	return resp, nil
}

// transport adds the headers to every request, logs it when asked to, and
// reads the server's API level from what it answers.
type transport struct {
	base   http.RoundTripper
	header http.Header
	debug  io.Writer
	warn   func(string)
	once   sync.Once
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for key, values := range t.header {
		req.Header[key] = values
	}
	started := time.Now()
	resp, err := t.base.RoundTrip(req)
	if t.debug != nil {
		status := "error: " + fmt.Sprint(err)
		if err == nil {
			status = resp.Status
		}
		fmt.Fprintf(t.debug, "%s %s -> %s (%s)\n", req.Method, req.URL.Redacted(), status,
			time.Since(started).Round(time.Millisecond))
	}
	if err != nil {
		return nil, err //nolint:wrapcheck // the client wraps it
	}
	if level, convErr := strconv.Atoi(resp.Header.Get(HeaderAPILevel)); convErr == nil && level > api.APILevel &&
		t.warn != nil {
		t.once.Do(func() {
			t.warn(fmt.Sprintf("this server's API is at level %d and this CLI's at %d: "+
				"it reads, and the server refuses its changes. Update the CLI.", level, api.APILevel))
		})
	}
	return resp, nil
}
