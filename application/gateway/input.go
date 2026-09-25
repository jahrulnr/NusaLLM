package appgateway

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// GatewayInput is the full gateway document — Upsert replaces, it does
// not merge. Every readable field is authoritative on each call; the one
// write-only field, ApiKey, is the exception because callers can never
// read back what they would resend:
//
//   - nil: keep the stored credential verbatim — env:/secret:
//     indirections survive edits to other fields. On a new gateway nil
//     configures no key.
//   - pointer to "": clear the credential — the ref and the vault entry.
//   - pointer to a value: set it — a literal is written to the
//     SecretStore under "gateway/<id>/api_key" and the record stores its
//     "secret:" name; an "env:NAME"/"secret:<key>" ref persists as-is.
//
// Stream nil takes the domain default (enabled); a pointer toggles it.
// Models replaces the whole catalog — omit it to clear.
type GatewayInput struct {
	ID       string
	Name     string
	Style    enum.Style
	Endpoint string
	ApiKey   *string
	Stream   *bool
	Models   []ModelInput
}

// ModelInput carries one catalog entry. Pointers mirror the entity's
// "unset ≠ zero" rule — nil means the param is omitted from the wire,
// not sent as 0. Enum fields accept the "" zero value as unset; a cast
// value outside the enum's set is a 400, never silently dropped.
type ModelInput struct {
	ID               string
	Route            string
	Temperature      *float64
	TopP             *float64
	TopK             *int
	MaxOutputTokens  *int
	PresencePenalty  *float64
	FrequencyPenalty *float64
	Seed             *int64
	StopSequences    []string
	Effort           enum.Effort
	ModelType        enum.ModelType
	ContextWindow    int
	Pricing          *valueobject.Pricing
	InputModalities  []enum.Modality
	OutputModalities []enum.Modality
	OptionalContent  map[string]string
}

// GatewayInfo is the sanitized read projection for transports: the whole
// document minus key material. KeyRef carries the stored indirection so a
// UI can distinguish "env-managed" from "vaulted" from "unset"; KeySet
// reports a credential is configured even when it sits inline (a stored
// literal never crosses — KeyRef stays "" there). Broken marks a record
// the send path can't use — undecodable file or unresolvable ref — so the
// management view can surface it instead of hiding it.
type GatewayInfo struct {
	ID       string
	Name     string
	Style    enum.Style
	Endpoint string
	KeyRef   string // "env:NAME" | "secret:<key>" | ""
	KeySet   bool   // a credential is stored in any form — ref or literal
	Broken   bool   // record can't decode, or its key ref won't resolve
	Stream   bool
	Models   []entity.Model
}

// buildModel hydrates one catalog entry — the input→entity codec,
// mirroring repogateway's decode side.
func (app *GatewayApp) buildModel(in ModelInput) (entity.Model, error) {
	m, err := entity.NewModel(in.ID, in.Route)
	if err != nil {
		return entity.Model{}, apperror.New(400, "invalid model", err)
	}
	for _, n := range []int{in.ContextWindow, app.derefInt(in.TopK), app.derefInt(in.MaxOutputTokens)} {
		if n < 0 {
			return entity.Model{}, apperror.New(400, "invalid model",
				fmt.Errorf("model %q: negative token count %d", in.ID, n))
		}
	}
	m.SetTemperature(in.Temperature)
	m.SetTopP(in.TopP)
	m.SetTopK(in.TopK)
	m.SetMaxOutputTokens(in.MaxOutputTokens)
	m.SetPresencePenalty(in.PresencePenalty)
	m.SetFrequencyPenalty(in.FrequencyPenalty)
	m.SetSeed(in.Seed)
	m.SetStopSequences(in.StopSequences)
	if in.Effort != "" {
		e, ok := enum.ParseEffort(in.Effort.String())
		if !ok {
			return entity.Model{}, app.badModel(in.ID, "effort", in.Effort.String())
		}
		m.SetEffort(e)
	}
	if in.ModelType != "" {
		t, ok := enum.ParseModelType(in.ModelType.String())
		if !ok {
			return entity.Model{}, app.badModel(in.ID, "modelType", in.ModelType.String())
		}
		m.SetModelType(t)
	}
	m.SetContextWindow(in.ContextWindow)
	if in.Pricing != nil {
		m.SetPricing(*in.Pricing)
	}
	mods, err := app.parseModalities(in.InputModalities, in.ID)
	if err != nil {
		return entity.Model{}, err
	}
	m.SetInputModalities(mods)
	mods, err = app.parseModalities(in.OutputModalities, in.ID)
	if err != nil {
		return entity.Model{}, err
	}
	m.SetOutputModalities(mods)
	for k, v := range in.OptionalContent {
		m.SetOptionalContent(k, v)
	}
	return m, nil
}

func (app *GatewayApp) parseModalities(mods []enum.Modality, modelID string) ([]enum.Modality, error) {
	var out []enum.Modality
	for _, mod := range mods {
		v, ok := enum.ParseModality(mod.String())
		if !ok {
			return nil, app.badModel(modelID, "modality", mod.String())
		}
		out = append(out, v)
	}
	return out, nil
}

func (app *GatewayApp) badModel(modelID, field, v string) error {
	return apperror.New(400, "invalid model",
		fmt.Errorf("model %q: unknown %s %q", modelID, field, v))
}

func (app *GatewayApp) derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// toInfo projects a snapshot for transport reads. The stored apiKey value
// crosses only when it has ref shape — a literal sitting in the record is
// key material, so it collapses to KeySet=true with no ref. Broken marks
// both an undecodable record and a ref that won't resolve: "env:" checks
// the variable, "secret:" hits the vault, anything else resolves verbatim
// by construction.
func (app *GatewayApp) toInfo(ctx context.Context, snap drepo.GatewaySnapshot) GatewayInfo {
	info := GatewayInfo{
		ID:     snap.ID,
		KeySet: snap.ApiKeyRef != "",
	}
	if valueobject.IsKeyRef(snap.ApiKeyRef) {
		info.KeyRef = snap.ApiKeyRef
	}
	if snap.Gateway == nil {
		info.Broken = true
		return info
	}
	gw := snap.Gateway
	info.Name = gw.Name()
	info.Style = gw.Style()
	info.Endpoint = gw.Endpoint()
	info.Stream = gw.StreamMode()
	info.Models = gw.Models()
	if name, ok := strings.CutPrefix(snap.ApiKeyRef, valueobject.KeyRefEnv); ok {
		info.Broken = name == "" || os.Getenv(name) == ""
	} else if key, ok := strings.CutPrefix(snap.ApiKeyRef, valueobject.KeyRefSecret); ok {
		if app.secrets == nil {
			info.Broken = true
		} else if _, err := app.secrets.Get(ctx, key); err != nil {
			info.Broken = true
		}
	}
	return info
}
