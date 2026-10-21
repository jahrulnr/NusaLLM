package toolweb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jahrulnr/searchwire"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// openCodeKinds are gateway-id kinds mapped to the OpenCode gateway (Zen and
// Go lanes both speak the Responses API at {endpoint}/responses). OpenCode's
// native web search is the web_search server tool: the provider executes the
// search inside the response and returns web_search_call actions plus a
// grounded answer — no client-side search backend involved.
var openCodeKinds = map[string]bool{"opencode": true, "opencode-go": true}

const openCodeBackend = "opencode"

// openCodeClient calls the gateway's Responses API with the web_search
// server tool. The output is normalized into the same document shape the
// search tool emits: query, results, plus the grounded answer and the
// provider's recorded search actions.
type openCodeClient struct {
	http *http.Client
}

func (t *Tool) openCodeClient() *openCodeClient {
	if t.httpClient != nil {
		return &openCodeClient{http: t.httpClient}
	}
	return &openCodeClient{http: &http.Client{Timeout: omniRouteTimeout}}
}

type openCodeResponsesRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
	Tools []struct {
		Type string `json:"type"`
	} `json:"tools"`
}

type openCodeResponsesResponse struct {
	Output []struct {
		Type   string `json:"type"`
		Action struct {
			Type  string `json:"type"`
			Query string `json:"query"`
			URL   string `json:"url"`
		} `json:"action"`
		Content []struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Annotations []struct {
				Type       string `json:"type"`
				URL        string `json:"url"`
				Title      string `json:"title"`
				StartIndex int    `json:"start_index"`
				EndIndex   int    `json:"end_index"`
			} `json:"annotations"`
		} `json:"content"`
	} `json:"output"`
}

// search issues one Responses request carrying the web_search server tool.
// It fails — rather than returning an ungrounded answer — when the response
// records no web_search_call: models behind oa-compat lanes silently drop
// non-function tools, and a plain answer would pretend a search happened.
func (c *openCodeClient) search(ctx context.Context, p service.WebProvider, argsJSON json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return nil, fmt.Errorf("decode search arguments: %w", err)
	}
	payload, err := json.Marshal(openCodeResponsesRequest{
		Model: p.Model,
		Input: args.Query,
		Tools: []struct {
			Type string `json:"type"`
		}{{Type: "web_search"}},
	})
	if err != nil {
		return nil, err
	}
	var response openCodeResponsesResponse
	// OpenCode requires x-opencode-session for routing — the same header the
	// text adapter sends. Without it Zen answers MissingSessionID and the
	// call silently degrades to the searchwire fallback every time.
	var headers map[string]string
	if convID := service.ConversationID(ctx); convID != "" && strings.Contains(p.Endpoint, string(enum.OpenCode)) {
		headers = map[string]string{"x-opencode-session": convID}
	}
	if err := postProviderJSON(ctx, c.http, p, "/responses", payload, &response, headers); err != nil {
		return nil, err
	}

	var (
		searches int
		actions  []map[string]string
		answer   []string
		results  []searchwire.Result
		seenURLs = map[string]bool{}
	)
	for _, item := range response.Output {
		switch item.Type {
		case "web_search_call":
			searches++
			action := map[string]string{"type": item.Action.Type}
			if item.Action.Query != "" {
				action["query"] = item.Action.Query
			}
			if item.Action.URL != "" {
				action["url"] = item.Action.URL
			}
			actions = append(actions, action)
			// open_page actions name the pages the server actually read —
			// the strongest source signal this API exposes.
			if item.Action.Type == "open_page" && item.Action.URL != "" && !seenURLs[item.Action.URL] {
				seenURLs[item.Action.URL] = true
				results = append(results, searchwire.Result{
					Title:   item.Action.URL,
					URL:     item.Action.URL,
					Sources: []string{openCodeBackend},
				})
			}
		case "message":
			for _, part := range item.Content {
				if part.Type != "output_text" || strings.TrimSpace(part.Text) == "" {
					continue
				}
				text := part.Text
				answer = append(answer, text)
				for _, ann := range part.Annotations {
					if ann.Type != "url_citation" || ann.URL == "" || seenURLs[ann.URL] {
						continue
					}
					seenURLs[ann.URL] = true
					snippet := ""
					if ann.StartIndex >= 0 && ann.EndIndex > ann.StartIndex && ann.EndIndex <= len(text) {
						snippet = text[ann.StartIndex:ann.EndIndex]
					}
					results = append(results, searchwire.Result{
						Title:   ann.Title,
						URL:     ann.URL,
						Snippet: snippet,
						Sources: []string{openCodeBackend},
					})
				}
			}
		}
	}
	if searches == 0 {
		return nil, fmt.Errorf("opencode returned no web_search_call — model %q likely does not execute the web_search server tool", p.Model)
	}
	doc := map[string]any{
		"query":   args.Query,
		"answer":  strings.TrimSpace(strings.Join(answer, "\n\n")),
		"results": results,
		"actions": actions,
	}
	return json.MarshalIndent(doc, "", "  ")
}
