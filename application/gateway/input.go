package appgateway

import (
	"fmt"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
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
	Style            enum.Style // wire-surface override — "" inherits the gateway
	ContextWindow    int
	Pricing          *valueobject.Pricing
	InputModalities  []enum.Modality
	OutputModalities []enum.Modality
	OptionalContent  map[string]string

	// Catalog-declared metadata — same fields an import backfills. Set
	// them only for gateways whose catalog stays silent; a provider
	// declaration fills these on empty fields during merge.
	Name             string
	Canonical        string
	AliasOf          string
	OutputLimit      int
	MaxImages        int
	SupportedEfforts []enum.Effort
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

// UpsertOutcome reports what a write or refresh did beyond storing the
// record: Added lists the provider-catalog ids the post-write import
// merged in (empty when nothing new came or no catalog is wired), and
// CatalogError carries the fetch/merge failure when one happened. The
// record is already committed when the import runs, so a catalog
// failure is data, not an error — a caller must never retry the write
// itself on a non-empty CatalogError.
type UpsertOutcome struct {
	Added        []string
	CatalogError string
}

// InputFromInfo rebuilds the write document from a sanitized read
// projection — the read-modify-write codec for edits like "rename" or
// "rotate the key" where the caller sees GatewayInfo, not the record.
// ApiKey comes back nil (keep) so a projection-based rewrite can never
// echo key material into the store.
func InputFromInfo(info GatewayInfo) GatewayInput {
	in := GatewayInput{
		ID:       info.ID,
		Name:     info.Name,
		Style:    info.Style,
		Endpoint: info.Endpoint,
		Stream:   &info.Stream,
	}
	for _, m := range info.Models {
		pricing := m.Pricing()
		in.Models = append(in.Models, ModelInput{
			ID:               m.ID(),
			Route:            m.Route(),
			Temperature:      m.Temperature(),
			TopP:             m.TopP(),
			TopK:             m.TopK(),
			MaxOutputTokens:  m.MaxOutputTokens(),
			PresencePenalty:  m.PresencePenalty(),
			FrequencyPenalty: m.FrequencyPenalty(),
			Seed:             m.Seed(),
			StopSequences:    m.StopSequences(),
			Effort:           m.Effort(),
			ModelType:        m.ModelType(),
			Style:            m.Style(),
			ContextWindow:    m.ContextWindow(),
			Pricing:          &pricing,
			InputModalities:  m.InputModalities(),
			OutputModalities: m.OutputModalities(),
			OptionalContent:  m.OptionalContent(),
			Name:             m.Name(),
			Canonical:        m.Canonical(),
			AliasOf:          m.AliasOf(),
			OutputLimit:      m.OutputLimit(),
			MaxImages:        m.MaxImages(),
			SupportedEfforts: m.SupportedEfforts(),
		})
	}
	return in
}

// buildModel hydrates one catalog entry — the input→entity codec,
// mirroring repogateway's decode side.
func (app *GatewayApp) buildModel(in ModelInput) (entity.Model, error) {
	m, err := entity.NewModel(in.ID, in.Route)
	if err != nil {
		return entity.Model{}, apperror.New(400, "invalid model", err)
	}
	for _, n := range []int{in.ContextWindow, in.OutputLimit, in.MaxImages, app.derefInt(in.TopK), app.derefInt(in.MaxOutputTokens)} {
		if n < 0 {
			return entity.Model{}, apperror.New(400, "invalid model",
				fmt.Errorf("model %q: negative value %d", in.ID, n))
		}
	}
	// Generation params are provider contracts, not free-form knobs:
	// temperature rides within [0,2], TopP is a probability, and the
	// penalties sit in [-2,2]. A declared output limit above the context
	// window reserves more tokens than the model holds. Storing an
	// impossible value only defers the failure to every send.
	if in.Temperature != nil && (*in.Temperature < 0 || *in.Temperature > 2) {
		return entity.Model{}, apperror.New(400, "invalid model",
			fmt.Errorf("model %q: temperature %v must be within [0,2]", in.ID, *in.Temperature))
	}
	if in.TopP != nil && (*in.TopP < 0 || *in.TopP > 1) {
		return entity.Model{}, apperror.New(400, "invalid model",
			fmt.Errorf("model %q: topP %v must be within [0,1]", in.ID, *in.TopP))
	}
	for _, p := range []struct {
		name string
		v    *float64
	}{{"presencePenalty", in.PresencePenalty}, {"frequencyPenalty", in.FrequencyPenalty}} {
		if p.v != nil && (*p.v < -2 || *p.v > 2) {
			return entity.Model{}, apperror.New(400, "invalid model",
				fmt.Errorf("model %q: %s %v must be within [-2,2]", in.ID, p.name, *p.v))
		}
	}
	if in.ContextWindow > 0 && in.OutputLimit > in.ContextWindow {
		return entity.Model{}, apperror.New(400, "invalid model",
			fmt.Errorf("model %q: outputLimit %d exceeds contextWindow %d", in.ID, in.OutputLimit, in.ContextWindow))
	}
	// TopK is a positive cutoff — nil means unset, so a pointer to zero
	// crosses the wire as top_k:0: a request to sample from zero tokens,
	// which no provider accepts. Same treatment as maxOutputTokens below.
	if v := in.TopK; v != nil && *v <= 0 {
		return entity.Model{}, apperror.New(400, "invalid model",
			fmt.Errorf("model %q: topK %d must be positive", in.ID, *v))
	}
	// MaxOutputTokens is the per-request ask: nil means unset, so a pointer
	// to zero is a request to cap the reply at zero tokens — every provider
	// rejects it. A stored zero only defers that failure to every send.
	if v := in.MaxOutputTokens; v != nil {
		if *v <= 0 {
			return entity.Model{}, apperror.New(400, "invalid model",
				fmt.Errorf("model %q: maxOutputTokens %d must be positive", in.ID, *v))
		}
		if in.ContextWindow > 0 && *v > in.ContextWindow {
			return entity.Model{}, apperror.New(400, "invalid model",
				fmt.Errorf("model %q: maxOutputTokens %d exceeds contextWindow %d", in.ID, *v, in.ContextWindow))
		}
		if in.OutputLimit > 0 && *v > in.OutputLimit {
			return entity.Model{}, apperror.New(400, "invalid model",
				fmt.Errorf("model %q: maxOutputTokens %d exceeds outputLimit %d", in.ID, *v, in.OutputLimit))
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
	if in.Style != "" {
		s, ok := enum.ParseStyle(in.Style.String())
		if !ok {
			return entity.Model{}, app.badModel(in.ID, "style", in.Style.String())
		}
		m.SetStyle(s)
	}
	m.SetContextWindow(in.ContextWindow)
	m.SetName(in.Name)
	m.SetCanonical(in.Canonical)
	m.SetAliasOf(in.AliasOf)
	m.SetOutputLimit(in.OutputLimit)
	m.SetMaxImages(in.MaxImages)
	var supported []enum.Effort
	for _, v := range in.SupportedEfforts {
		e, ok := enum.ParseEffort(v.String())
		if !ok {
			return entity.Model{}, app.badModel(in.ID, "supportedEfforts", v.String())
		}
		if e.IsUnset() {
			continue // "default"/"" are sentinels, not levels the provider offers
		}
		supported = append(supported, e)
	}
	m.SetSupportedEfforts(supported)
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
// an undecodable record or a stored key that won't resolve — ref
// resolution is the repository adapter's rule, consumed here as
// GatewaySnapshot.KeyResolved.
func (app *GatewayApp) toInfo(snap drepo.GatewaySnapshot) GatewayInfo {
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
	info.Broken = !snap.KeyResolved
	return info
}

// ListedModels returns the catalog entries a user may select — Models
// filtered by the picker floor.
func (g GatewayInfo) ListedModels() []entity.Model {
	return ListedModels(g.Models)
}

// CatalogContains reports whether the gateway advertises model id.
func (g GatewayInfo) CatalogContains(id string) bool {
	return ModelCatalogContains(g.Models, id)
}

// ModelListed reports whether id resolves to a listed catalog entry —
// advertised by the gateway and over the picker floor.
func (g GatewayInfo) ModelListed(id string) bool {
	for _, m := range g.ListedModels() {
		if m.ID() == id {
			return true
		}
	}
	return false
}
