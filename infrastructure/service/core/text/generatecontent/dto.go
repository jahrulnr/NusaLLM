package textgeneratecontent

import "encoding/json"

// Wire DTOs for the Gemini /v1beta/models/{model}:generateContent surface.
// The wire is role-alternated contents of typed parts — text, thought
// parts, inlineData/fileData media, functionCall/functionResponse — and
// thoughtSignature fields on model parts must replay byte-exact, so model
// parts are stored raw in the turn template.

// RequestBody is POST {endpoint}/models/{model}:generateContent (or
// :streamGenerateContent?alt=sse). generationConfig params are omitempty —
// unset means "provider default", never send a zero.
type RequestBody struct {
	Contents          []ContentBody              `json:"contents"`
	SystemInstruction *ContentBody               `json:"systemInstruction,omitempty"`
	Tools             []ToolBody                 `json:"tools,omitempty"`
	GenerationConfig  *GenerationConfigBody      `json:"generationConfig,omitempty"`
	Extra             map[string]json.RawMessage `json:"-"` // flattened at marshal
}

// MarshalJSON merges Extra into the top level — adapter-namespaced escape
// params ("generatecontent.<param>") ride as raw JSON under their real
// wire names (safetySettings, toolConfig, cachedContent…).
func (b RequestBody) MarshalJSON() ([]byte, error) {
	type alias RequestBody
	raw, err := json.Marshal(alias(b))
	if err != nil {
		return nil, err
	}
	if len(b.Extra) == 0 {
		return raw, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for k, v := range b.Extra {
		m[k] = v
	}
	return json.Marshal(m)
}

// ContentBody is one contents turn — roles are only "user" and "model".
// Parts carry `any` so raw template entries (json.RawMessage) marshal
// verbatim next to typed PartBody values.
type ContentBody struct {
	Role  string `json:"role"`
	Parts []any  `json:"parts"`
}

// PartBody is both the encode shape for one content part and the decoded
// view of a wire part — a part carries exactly one payload field plus the
// part-level thought/thoughtSignature flags.
type PartBody struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	InlineData       *BlobBody         `json:"inlineData,omitempty"`
	FileData         *FileDataBody     `json:"fileData,omitempty"`
	FunctionCall     *FunctionCallBody `json:"functionCall,omitempty"`
	FunctionResponse *FunctionRespBody `json:"functionResponse,omitempty"`
}

// BlobBody is inline media — base64 bytes with a declared MIME type.
type BlobBody struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

// FileDataBody is a remote-media reference the provider fetches.
type FileDataBody struct {
	MIMEType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

// FunctionCallBody is a model-emitted call — args is a JSON object, id is
// the provider-issued correlation handle when present.
type FunctionCallBody struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// FunctionRespBody answers one call — response is a free-form object
// ({"result": …} here); id echoes the provider's call id for correlation.
type FunctionRespBody struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// ToolBody wraps all function declarations in one tools entry.
type ToolBody struct {
	FunctionDeclarations []FunctionDeclBody `json:"functionDeclarations"`
}

// FunctionDeclBody is one wire tool schema — parameters must be the
// Gemini Schema subset, sanitized at encode.
type FunctionDeclBody struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// GenerationConfigBody is the generationConfig envelope.
type GenerationConfigBody struct {
	Temperature    *float64            `json:"temperature,omitempty"`
	TopP           *float64            `json:"topP,omitempty"`
	TopK           *int                `json:"topK,omitempty"`
	MaxOutput      int                 `json:"maxOutputTokens,omitempty"`
	StopSequences  []string            `json:"stopSequences,omitempty"`
	ThinkingConfig *ThinkingConfigBody `json:"thinkingConfig,omitempty"`
}

// ThinkingConfigBody maps the family-dependent thinking controls —
// thinkingLevel for Gemini 3, includeThoughts on its own for 2.5
// (budgets are never invented); both together are a wire error.
type ThinkingConfigBody struct {
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts *bool  `json:"includeThoughts,omitempty"`
}

// ResponseBody is the answer (or one stream chunk — same shape, partial).
type ResponseBody struct {
	Candidates     []CandidateBody `json:"candidates"`
	Usage          *UsageBody      `json:"usageMetadata"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	Error *ErrorBody `json:"error"`
}

// CandidateBody is one completion candidate — this adapter reads the first
// only; Parts stay raw so signed/thought parts reach the template
// byte-exact.
type CandidateBody struct {
	Content struct {
		Role  string            `json:"role"`
		Parts []json.RawMessage `json:"parts"`
	} `json:"content"`
	FinishReason string `json:"finishReason"`
}

// UsageBody maps usageMetadata — promptTokenCount is the INCLUSIVE total
// (cachedContentTokenCount is a subset); thoughts bill as output, so they
// fold into the generated-token count.
type UsageBody struct {
	PromptTokens     int `json:"promptTokenCount"`
	CachedTokens     int `json:"cachedContentTokenCount"`
	CandidatesTokens int `json:"candidatesTokenCount"`
	ThoughtsTokens   int `json:"thoughtsTokenCount"`
	TotalTokens      int `json:"totalTokenCount"`
}

// ErrorBody is the error envelope — code is the HTTP number, status the
// gRPC-style name (RESOURCE_EXHAUSTED, INVALID_ARGUMENT, …).
type ErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}
