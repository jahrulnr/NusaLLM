// Package toolweb adapts searchwire tool definitions into the domain tool
// port. Searchwire owns the tool semantics (names, descriptions, schemas,
// execution); this package only translates the contract — and carries the
// adapter concerns searchwire must not own: per-turn provider selection,
// conversation-owned output spilling, and document post-processing.
package toolweb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/jahrulnr/searchwire"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

const (
	// fetchTextBudget is the searchwire-side text cap for fetch — under the
	// domain's 32000-byte tool output budget so the JSON envelope (links,
	// llmsTxt, headers) fits alongside it. Larger pages spill to a
	// conversation artifact instead of silently eliding mid-document.
	fetchTextBudget = 20000
	// maxArtifactBytes bounds a spill copy — artifacts are extracted text,
	// already bounded upstream by searchwire's raw body limit.
	maxArtifactBytes = 32 << 20
	// backendFallbackMaxLen bounds the provider-fallback note embedded in
	// the output document.
	backendFallbackMaxLen = 200
)

// Tool runs one searchwire tool definition under the domain contract.
// When the turn's bound provider exposes the same capability as a plain
// API (currently only OmniRoute), Execute prefers that endpoint with the
// gateway's own credentials and falls back to searchwire on any failure.
type Tool struct {
	def        searchwire.ToolDef
	schema     valueobject.ToolSchema
	outputs    drepo.ToolOutput // conversation-owned spill store; may be nil
	httpClient *http.Client     // test seam; nil uses the default client
}

// Tools adapts the named searchwire capabilities (searchwire.ToolSearch,
// ToolFetch, ToolAnswer) into domain tools. Names advertised to models are
// prefixed "web_" (web_search, web_fetch, …) so they read as web
// capabilities rather than generic verbs.
func Tools(outputs drepo.ToolOutput, names ...string) ([]service.Tool, error) {
	searcher := searchwire.New(searchwire.Config{MaxResponseBytes: fetchTextBudget})
	want := make(map[string]bool, len(names))
	for _, name := range names {
		want[strings.ToLower(strings.TrimSpace(name))] = true
	}
	var tools []service.Tool
	for _, def := range searcher.Tools() {
		if len(want) > 0 && !want[def.Name] {
			continue
		}
		tool, err := New(def, outputs)
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// New wraps one searchwire ToolDef. The advertised name gains a "web_"
// prefix when it does not already carry one.
func New(def searchwire.ToolDef, outputs drepo.ToolOutput) (service.Tool, error) {
	name := def.Name
	if !strings.HasPrefix(name, "web_") {
		name = "web_" + name
	}
	schema, err := valueobject.NewToolSchema(name, def.Description, def.InputSchema, true, enum.ToolPermissionAgent)
	if err != nil {
		return nil, err
	}
	return &Tool{def: def, schema: schema, outputs: outputs}, nil
}

func (t *Tool) Schema() valueobject.ToolSchema {
	return t.schema
}

func (t *Tool) Execute(ctx context.Context, call entity.ToolCall, _ service.ToolEmitter) (service.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return service.ToolResult{}, err
	}
	args := json.RawMessage(call.Input())
	var fallbackErr error
	if p, ok := service.WebProviderFrom(ctx); ok && p.Endpoint != "" {
		var (
			out     json.RawMessage
			err     error
			backend string
		)
		switch {
		case omniRouteKinds[p.Kind]:
			client := t.omniRouteClient()
			switch t.def.Name {
			case searchwire.ToolSearch:
				out, err = client.search(ctx, p, args)
			case searchwire.ToolFetch:
				out, err = client.fetch(ctx, p, args)
			}
			backend = omniRouteBackend
		case openCodeKinds[p.Kind] && t.def.Name == searchwire.ToolSearch && p.Model != "":
			out, err = t.openCodeClient().search(ctx, p, args)
			backend = openCodeBackend
		}
		if err == nil && out != nil {
			return t.result(ctx, call, out, backend, nil)
		}
		if backend != "" {
			fallbackErr = err
		}
	}
	out, err := t.def.Run(ctx, args)
	if err != nil {
		return service.ToolResult{}, err
	}
	return t.result(ctx, call, out, searchwireBackend, fallbackErr)
}

// result annotates the emitted document with the backend that served it,
// moves a searchwire tempFile artifact into the conversation's tool-output
// store (the library's temp-dir path is unreachable for the turn's file
// tools — the spill path is reachable via exec like every other tool's),
// and spills oversized documents the way exec does so the middle-truncated
// text keeps a recovery pointer instead of silently eliding content.
func (t *Tool) result(ctx context.Context, call entity.ToolCall, out json.RawMessage, backend string, fallbackErr error) (service.ToolResult, error) {
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		return service.ToolResult{Text: string(out)}, nil
	}
	doc["backend"] = backend
	if fallbackErr != nil {
		doc["backendFallback"] = boundedErrorText(fallbackErr)
	}
	if src, _ := doc["tempFile"].(string); src != "" {
		if ref := t.respillArtifact(ctx, call, src); ref != "" {
			doc["tempFile"] = ref
		}
	}
	buf, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return service.ToolResult{Text: string(out)}, nil
	}
	if len(buf) > call.OutputBudget() {
		if ref := t.spillBytes(ctx, call, buf); ref != "" {
			return service.ToolResult{Text: call.OutputWithMiddleTruncateWindow(string(buf), string(buf), len(buf), "full output: "+ref)}, nil
		}
	}
	return service.ToolResult{Text: string(buf)}, nil
}

// reserve allocates a conversation-owned spill path; nil when no usable
// conversation or output store is bound (tests, detached contexts).
func (t *Tool) reserve(ctx context.Context, call entity.ToolCall) drepo.ToolOutputSpill {
	if t.outputs == nil {
		return nil
	}
	convID := service.ConversationID(ctx)
	if err := valueobject.ValidID(convID); err != nil {
		return nil
	}
	spill, err := t.outputs.Reserve(ctx, convID, call.ID())
	if err != nil {
		return nil
	}
	return spill
}

// spillBytes writes buf to a reserved conversation spill file and returns
// its reference — "" on any failure, and the caller keeps the inline doc.
func (t *Tool) spillBytes(ctx context.Context, call entity.ToolCall, buf []byte) string {
	spill := t.reserve(ctx, call)
	if spill == nil {
		return ""
	}
	f, err := spill.Open()
	if err != nil {
		return ""
	}
	_, werr := f.Write(buf)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return ""
	}
	return spill.Reference()
}

// respillArtifact copies a searchwire temp artifact into the conversation's
// tool-output store, returning the reachable reference. The original file
// is removed on success so spent artifacts do not accumulate.
func (t *Tool) respillArtifact(ctx context.Context, call entity.ToolCall, srcPath string) string {
	spill := t.reserve(ctx, call)
	if spill == nil {
		return ""
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return ""
	}
	defer src.Close()
	f, err := spill.Open()
	if err != nil {
		return ""
	}
	_, werr := io.Copy(f, io.LimitReader(src, maxArtifactBytes))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return ""
	}
	_ = os.Remove(srcPath)
	return spill.Reference()
}

func boundedErrorText(err error) string {
	s := strings.TrimSpace(err.Error())
	if len(s) > backendFallbackMaxLen {
		return s[:backendFallbackMaxLen] + "…"
	}
	return s
}
