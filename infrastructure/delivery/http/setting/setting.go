// Package setting is the WebUI settings boundary — the effective
// persisted config projected down to whitelisted leaves on read, and a
// single-leaf PATCH on write. storage.* paths and app metadata never
// cross.
package setting

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
)

// Reader supplies the effective persisted config document as JSON; the
// projection below reduces it to the browser-safe leaves.
// *setting.SettingApp satisfies it.
type Reader interface {
	Show() ([]byte, error)
}

// Writer patches one whitelisted leaf and returns the rewritten
// document. *setting.SettingApp satisfies it.
type Writer interface {
	Set(key, rawValue string) ([]byte, error)
}

// Deps wires the use cases; Reads and Writes are optional — nil leaves
// the corresponding routes unregistered.
type Deps struct {
	Reads  Reader
	Writes Writer
}

// Register mounts this boundary's routes under /api.
func Register(api *gin.RouterGroup, d Deps) {
	s := &server{reads: d.Reads, writes: d.Writes}
	if d.Reads != nil {
		api.GET("/settings", s.get)
	}
	if d.Writes != nil {
		api.PATCH("/settings", s.patch)
	}
}

type server struct {
	reads  Reader
	writes Writer
}

// leaves is the exact set of config keys the browser may read or write —
// the contract's Settings schema, fixed so a future config field holding
// a path or secret can never cross by accident of projection.
// storage.* (filesystem paths) and app.* (internal metadata) stay out.
var leaves = map[string]map[string]bool{
	"server": {
		"port":  true,
		"hosts": true,
	},
	"conversation": {
		"mailbox_cap":        true,
		"max_turn_steps":     true,
		"compact_budget_pct": true,
		"compact_keep_tail":  true,
	},
	"service": {
		"header_timeout": true,
		"idle_timeout":   true,
	},
	"log": {
		"max_lines": true,
		"max_files": true,
	},
}

// project reduces the persisted config document to the whitelisted leaf
// set — sections always appear, possibly empty.
func project(raw []byte) (map[string]map[string]json.RawMessage, error) {
	var doc map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]json.RawMessage, len(leaves))
	for section, allowed := range leaves {
		proj := make(map[string]json.RawMessage, len(allowed))
		for leaf := range allowed {
			if v, ok := doc[section][leaf]; ok {
				proj[leaf] = v
			}
		}
		out[section] = proj
	}
	return out, nil
}

func (s *server) get(c *gin.Context) {
	raw, err := s.reads.Show()
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	out, err := project(raw)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	c.JSON(200, out)
}

// patch writes one whitelisted leaf ("service.idle_timeout") through the
// settings use case and answers the projected document — the browser
// sees exactly what it could read.
func (s *server) patch(c *gin.Context) {
	var body struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	if !transport.DecodeJSON(c, &body) {
		return
	}
	section, leaf, found := strings.Cut(body.Key, ".")
	if !found || !leaves[section][leaf] {
		transport.WriteProblem(c, 400, "invalid_argument", "key is not a writable settings leaf")
		return
	}
	if body.Value == nil || bytes.Equal(bytes.TrimSpace(body.Value), []byte("null")) {
		transport.WriteProblem(c, 400, "invalid_argument", "value must be a non-null JSON literal")
		return
	}
	raw, err := s.writes.Set(body.Key, string(body.Value))
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	out, err := project(raw)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	c.JSON(200, out)
}
