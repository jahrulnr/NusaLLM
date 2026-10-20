package toolweb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jahrulnr/searchwire"

	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// omniRouteKinds are the gateway-id kinds that map to the OmniRoute/9Router
// gateway — the one provider whose search/fetch are plain APIs rather than
// a model-mediated or mid-turn tool. The router product ships under both
// names; users name their gateway either way.
var omniRouteKinds = map[string]bool{"omniroute": true, "9router": true}

const (
	omniRouteBackend  = "omniroute"
	searchwireBackend = "searchwire"
)

// omniRouteClient calls the router's own search/fetch endpoints. Both produce
// documents normalized to the same JSON the searchwire tools emit, so the
// model sees one output contract regardless of backend.
type omniRouteClient struct {
	http *http.Client
}

func (t *Tool) omniRouteClient() *omniRouteClient {
	if t.httpClient != nil {
		return &omniRouteClient{http: t.httpClient}
	}
	return &omniRouteClient{http: &http.Client{Timeout: omniRouteTimeout}}
}

const omniRouteTimeout = 30 * time.Second // router fan-out can outlive the searchwire default

// omniRouteResult mirrors the search tool's output document: the lower-case
// envelope keys from searchwire's toolSearchOutput plus capitalized Result
// fields exactly as the library marshals them.
type omniRouteResult struct {
	Query   string                   `json:"query"`
	Results []searchwire.Result      `json:"results"`
	Errors  []searchwire.SourceError `json:"errors,omitempty"`
}

type omniRouteSearchResponse struct {
	Provider string `json:"provider"`
	Results  []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
	} `json:"results"`
	Answer json.RawMessage `json:"answer"`
}

func (c *omniRouteClient) search(ctx context.Context, p service.WebProvider, argsJSON json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return nil, fmt.Errorf("decode search arguments: %w", err)
	}
	if args.Limit <= 0 {
		args.Limit = 10
	}
	payload, err := json.Marshal(map[string]any{"query": args.Query, "max_results": args.Limit})
	if err != nil {
		return nil, err
	}
	var response omniRouteSearchResponse
	if err := postProviderJSON(ctx, c.http, p, "/search", payload, &response); err != nil {
		return nil, err
	}
	out := omniRouteResult{Query: args.Query}
	for _, r := range response.Results {
		out.Results = append(out.Results, searchwire.Result{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Snippet,
			Sources: []string{"omniroute/" + response.Provider},
			Score:   1,
		})
	}
	if len(out.Results) == 0 {
		return nil, fmt.Errorf("omniroute search returned no results")
	}
	return json.MarshalIndent(out, "", "  ")
}

type omniRouteFetchResponse struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	Content  struct {
		Format string `json:"format"`
		Text   string `json:"text"`
		Length int    `json:"length"`
	} `json:"content"`
}

func (c *omniRouteClient) fetch(ctx context.Context, p service.WebProvider, argsJSON json.RawMessage) (json.RawMessage, error) {
	var args struct {
		URL      string `json:"url"`
		MaxBytes int64  `json:"max_bytes"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return nil, fmt.Errorf("decode fetch arguments: %w", err)
	}
	payloadMap := map[string]any{"url": args.URL, "format": "markdown"}
	if args.MaxBytes > 0 {
		payloadMap["max_characters"] = args.MaxBytes
	}
	payload, err := json.Marshal(payloadMap)
	if err != nil {
		return nil, err
	}
	var response omniRouteFetchResponse
	if err := postProviderJSON(ctx, c.http, p, "/web/fetch", payload, &response); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(response.Content.Text)
	if text == "" {
		return nil, fmt.Errorf("omniroute fetch returned no content")
	}
	out, err := json.MarshalIndent(map[string]any{
		"url":   args.URL,
		"title": response.Title,
		"text":  text,
		"bytes": response.Content.Length,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return out, nil
}

// postProviderJSON is the shared POST helper for provider-native web paths:
// bearer auth from the bound gateway's own credentials, bounded error body.
func postProviderJSON(ctx context.Context, client *http.Client, p service.WebProvider, path string, payload []byte, into any) error {
	url := strings.TrimRight(p.Endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s request: %w", p.Kind, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s returned %s: %s", p.Kind, path, resp.Status, truncateBody(body))
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("parse %s %s response: %w", p.Kind, path, err)
	}
	return nil
}

func truncateBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
