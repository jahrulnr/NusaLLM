package transport

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// MaxBodyBytes caps any mutating request body — generous for a gateway
// catalog document, far above a settings patch.
const MaxBodyBytes = 1 << 20

// DecodeJSON enforces the write-body rules shared by every mutating
// route: Content-Type application/json, ≤ MaxBodyBytes, one JSON
// document, and no unknown fields. Answers its own problem and returns
// ok=false on failure.
func DecodeJSON(c *gin.Context, v any) bool {
	if mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type")); err != nil ||
		mt != "application/json" {
		WriteProblem(c, 400, "invalid_argument", "Content-Type must be application/json")
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, MaxBodyBytes+1))
	if err != nil {
		RespondError(c, err)
		return false
	}
	if len(raw) > MaxBodyBytes {
		WriteProblem(c, 413, "input_too_large", "request body exceeds 1 MiB")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		WriteProblem(c, 400, "invalid_argument",
			"body must be a single JSON document matching the schema")
		return false
	}
	// A second document or trailing garbage must decode to io.EOF —
	// dec.More() alone cannot distinguish trailing junk reliably.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		WriteProblem(c, 400, "invalid_argument",
			"body must be a single JSON document matching the schema")
		return false
	}
	return true
}

// PathID returns the :id route parameter after the domain's path-safe
// identity check — ids name files downstream, so separators and parent
// references are a 400, not a repository 404.
func PathID(c *gin.Context) (string, bool) {
	id := c.Param("id")
	if err := valueobject.ValidID(id); err != nil {
		WriteProblem(c, 400, "invalid_argument", "id must be a non-empty path-safe segment")
		return "", false
	}
	return id, true
}
