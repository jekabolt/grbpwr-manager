package entity

import (
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ───────────────────────── AI providers (0373 config, 0374 ledger) ─────────────────────────
//
// Every vocabulary below is a VARCHAR in the database and is closed HERE, not by an ENUM or a CHECK
// (the design band rule, IsDesignRunKind): a new provider or purpose is one Go line and one seed row,
// never an ALTER on a live table.

// Provider keys — ai_provider.provider_key.
const (
	AIProviderOpenAI     = "openai"
	AIProviderAnthropic  = "anthropic"
	AIProviderGoogle     = "google"
	AIProviderOpenRouter = "openrouter"
	AIProviderApibost    = "apibost"
	AIProviderFal        = "fal"
	AIProviderMeshy      = "meshy"
	AIProviderRunblob    = "runblob"
	AIProviderRecraft    = "recraft"
)

// AIProviderKeys — every provider, in the panel's fixed order. A copy on every call.
func AIProviderKeys() []string {
	return []string{
		AIProviderOpenAI, AIProviderAnthropic, AIProviderGoogle, AIProviderOpenRouter,
		AIProviderApibost, AIProviderFal, AIProviderMeshy, AIProviderRunblob, AIProviderRecraft,
	}
}

// IsAIProviderKey reports whether v names a known provider.
func IsAIProviderKey(v string) bool {
	switch v {
	case AIProviderOpenAI, AIProviderAnthropic, AIProviderGoogle, AIProviderOpenRouter,
		AIProviderApibost, AIProviderFal, AIProviderMeshy, AIProviderRunblob, AIProviderRecraft:
		return true
	}
	return false
}

// Capabilities — what a provider can serve; also ai_model.kind.
const (
	AICapabilityChat   = "chat"
	AICapabilityImage  = "image"
	AICapabilityCutout = "cutout"
	AICapabilityEdit   = "edit"
	AICapabilityThreed = "threed"
	AICapabilityVector = "vector"
	AICapabilityVideo  = "video"
)

// AICapabilities — every capability, in a fixed order. A copy on every call.
func AICapabilities() []string {
	return []string{
		AICapabilityChat, AICapabilityImage, AICapabilityCutout, AICapabilityEdit,
		AICapabilityThreed, AICapabilityVector, AICapabilityVideo,
	}
}

// IsAICapability reports whether v names a known capability.
func IsAICapability(v string) bool {
	switch v {
	case AICapabilityChat, AICapabilityImage, AICapabilityCutout, AICapabilityEdit,
		AICapabilityThreed, AICapabilityVector, AICapabilityVideo:
		return true
	}
	return false
}

// AIProviderCapabilities — what one provider can serve; nil for an unknown key. A copy on every call.
//
// This is what a route may ASK of a provider, not what is wired today: openai, anthropic, google,
// apibost and runblob have no transport until later commits, and a route to them is refused by the
// registry as keyless/disabled rather than by this table.
func AIProviderCapabilities(key string) []string {
	switch key {
	case AIProviderOpenAI, AIProviderGoogle, AIProviderOpenRouter, AIProviderApibost:
		return []string{AICapabilityChat, AICapabilityImage}
	case AIProviderAnthropic:
		return []string{AICapabilityChat}
	case AIProviderFal:
		return []string{AICapabilityImage, AICapabilityCutout, AICapabilityEdit, AICapabilityThreed}
	case AIProviderMeshy:
		return []string{AICapabilityThreed}
	case AIProviderRunblob:
		return []string{AICapabilityVideo}
	case AIProviderRecraft:
		return []string{AICapabilityVector}
	}
	return nil
}

// AIProviderServes reports whether provider key can serve capability.
func AIProviderServes(key, capability string) bool {
	for _, c := range AIProviderCapabilities(key) {
		if c == capability {
			return true
		}
	}
	return false
}

// Purposes — ai_route.purpose and ai_usage_event.purpose; exactly 02-PLAN §4.1. There is no video
// purpose until the owner names one (D-05).
const (
	AIPurposeTechCardOperationsDraft = "chat.techcard_operations_draft"
	AIPurposeTechCardEnhance         = "chat.techcard_enhance"
	AIPurposeTechCardAnalysis        = "chat.techcard_analysis"
	AIPurposeNoteMarkdown            = "chat.note_markdown"
	AIPurposeEmailTranslate          = "chat.email_translate"
	AIPurposeDesignDraftIdea         = "chat.design_draft_idea"
	AIPurposePlaygroundIdeas         = "chat.playground_ideas"
	AIPurposeImageGenerate           = "image.generate"
	AIPurposeImageCutout             = "image.cutout"
	AIPurposeImageExtend             = "image.extend"
	AIPurposeImageInpaint            = "image.inpaint"
	AIPurposeThreed                  = "threed"
	AIPurposeVector                  = "vector"
)

// AIPurposes — every purpose, in the panel's fixed order. A copy on every call.
func AIPurposes() []string {
	return []string{
		AIPurposeTechCardOperationsDraft, AIPurposeTechCardEnhance, AIPurposeTechCardAnalysis,
		AIPurposeNoteMarkdown, AIPurposeEmailTranslate, AIPurposeDesignDraftIdea, AIPurposePlaygroundIdeas,
		AIPurposeImageGenerate, AIPurposeImageCutout, AIPurposeImageExtend, AIPurposeImageInpaint,
		AIPurposeThreed, AIPurposeVector,
	}
}

// IsAIPurpose reports whether v names a known purpose.
func IsAIPurpose(v string) bool {
	return AIPurposeCapability(v) != ""
}

// AIPurposeCapability — the capability a purpose needs; "" for an unknown purpose.
func AIPurposeCapability(p string) string {
	switch p {
	case AIPurposeTechCardOperationsDraft, AIPurposeTechCardEnhance, AIPurposeTechCardAnalysis,
		AIPurposeNoteMarkdown, AIPurposeEmailTranslate, AIPurposeDesignDraftIdea, AIPurposePlaygroundIdeas:
		return AICapabilityChat
	case AIPurposeImageGenerate:
		return AICapabilityImage
	case AIPurposeImageCutout:
		return AICapabilityCutout
	case AIPurposeImageExtend, AIPurposeImageInpaint:
		return AICapabilityEdit
	case AIPurposeThreed:
		return AICapabilityThreed
	case AIPurposeVector:
		return AICapabilityVector
	}
	return ""
}

// AIPurposeOfRunKind — the purpose a design run of this kind spends under; "" for an unknown kind.
//
// Every run kind is named explicitly and there is no default: a new run kind that nobody mapped must
// come out as "" (and fail the coverage test) rather than be booked, plausibly and wrongly, as an
// image generation.
func AIPurposeOfRunKind(kind string) string {
	switch kind {
	case DesignRunKindFlat, DesignRunKindRender, DesignRunKindRecolor, DesignRunKindPattern,
		DesignRunKindFreeform:
		return AIPurposeImageGenerate
	case DesignRunKindCutout:
		return AIPurposeImageCutout
	case DesignRunKindExtend:
		return AIPurposeImageExtend
	case DesignRunKindInpaint:
		return AIPurposeImageInpaint
	case DesignRunKindThreed:
		return AIPurposeThreed
	case DesignRunKindVector:
		return AIPurposeVector
	case DesignRunKindDraftIdea:
		return AIPurposeDesignDraftIdea
	}
	return ""
}

// AIKeyKind names one of a provider's two key slots.
type AIKeyKind string

const (
	// AIKeyAPI is the key that serves generations.
	AIKeyAPI AIKeyKind = "api"
	// AIKeyAdmin is the reconciliation key for the provider's cost API (D-06); it never serves a
	// generation.
	AIKeyAdmin AIKeyKind = "admin"
)

// Ledger statuses — ai_usage_event.status (02-PLAN A1).
const (
	// AICallDispatching — the row exists, the request may be in flight. The ONLY status a row is born
	// with; FinishCall moves it on, the sweeper turns a stale one into AICallUnknown.
	AICallDispatching = "dispatching"
	// AICallOK — delivered.
	AICallOK = "ok"
	// AICallFree — failed before the request was written (not engaged): no money moved, cost 0.
	AICallFree = "free"
	// AICallFailed — failed after it was written, no charge reported.
	AICallFailed = "failed"
	// AICallChargedFailed — failed, and the provider reported a charge.
	AICallChargedFailed = "charged_failed"
	// AICallAccepted — an async submit was accepted; the collect that delivers prices this row.
	AICallAccepted = "accepted"
	// AICallUnknown — the outcome is not known (swept, or an engaged timeout): money may have moved.
	AICallUnknown = "unknown"
)

// AICallErrorSweeper is the error_code the sweeper writes on a stale dispatching row.
const AICallErrorSweeper = "sweeper"

// AICallStatuses — every ledger status. A copy on every call.
func AICallStatuses() []string {
	return []string{
		AICallDispatching, AICallOK, AICallFree, AICallFailed, AICallChargedFailed,
		AICallAccepted, AICallUnknown,
	}
}

// IsAICallStatus reports whether v names a known ledger status.
func IsAICallStatus(v string) bool {
	switch v {
	case AICallDispatching, AICallOK, AICallFree, AICallFailed, AICallChargedFailed,
		AICallAccepted, AICallUnknown:
		return true
	}
	return false
}

// Cost sources — ai_usage_event.cost_source: where the row's number came from, in rank order.
const (
	AICostProvider = "provider" // the provider reported USD for this call
	AICostUnits    = "units"    // billable units or credits × our tariff
	AICostTable    = "table"    // tokens × the code price table (price_version names it)
	AICostEstimate = "estimate" // draft-idea's booked estimate
	AICostFree     = "free"     // an AICallFree row: nothing was sent, nothing is owed
	AICostNone     = "none"     // no number: cost_usd stays NULL
)

// AICostSources — every cost source. A copy on every call.
func AICostSources() []string {
	return []string{AICostProvider, AICostUnits, AICostTable, AICostEstimate, AICostFree, AICostNone}
}

// IsAICostSource reports whether v names a known cost source.
func IsAICostSource(v string) bool {
	switch v {
	case AICostProvider, AICostUnits, AICostTable, AICostEstimate, AICostFree, AICostNone:
		return true
	}
	return false
}

// ErrAIVersionConflict — a config write named a config_version that is no longer current: somebody
// else saved first, and the page must reload instead of overwriting what it never saw.
var ErrAIVersionConflict = errors.New("ai config version conflict")

// ───────────────────────── config rows ─────────────────────────

// AIProvider is one ai_provider row. The *KeyEnc fields are CIPHERTEXT (nonce + AES-256-GCM under
// AI_KEYS_MASTER_KEY); only the registry opens them. nil = no key stored (the env key answers).
type AIProvider struct {
	Key               string     `db:"provider_key"`
	Label             string     `db:"label"`
	Enabled           bool       `db:"enabled"`
	APIKeyEnc         []byte     `db:"api_key_enc"`
	APIKeyLast4       string     `db:"api_key_last4"`
	APIKeyUpdatedAt   *time.Time `db:"api_key_updated_at"`
	APIKeyUpdatedBy   string     `db:"api_key_updated_by"`
	AdminKeyEnc       []byte     `db:"admin_key_enc"`
	AdminKeyLast4     string     `db:"admin_key_last4"`
	AdminKeyUpdatedAt *time.Time `db:"admin_key_updated_at"`
	AdminKeyUpdatedBy string     `db:"admin_key_updated_by"`
	UpdatedBy         string     `db:"updated_by"`
	UpdatedAt         time.Time  `db:"updated_at"`
}

// AIModel is one ai_model row: a custom slug somebody typed into a route. The curated catalogue is
// code (aiprov/pricing), not rows.
type AIModel struct {
	ProviderKey string `db:"provider_key"`
	Model       string `db:"model"`
	Label       string `db:"label"`
	Kind        string `db:"kind"` // a capability
	Disabled    bool   `db:"disabled"`
}

// AIRouteCandidate is one ai_route row of a purpose.
type AIRouteCandidate struct {
	Position    int    `db:"position"`
	ProviderKey string `db:"provider_key"` // "" = the capability's default provider
	Model       string `db:"model"`        // "" = the client's own default
}

// AIRoute is a purpose and its candidates, ordered by Position (1 = primary).
type AIRoute struct {
	Purpose    string
	Candidates []AIRouteCandidate
}

// AISettings is the ai_settings singleton.
type AISettings struct {
	ConfigVersion           uint64    `db:"config_version"`
	DefaultChatProviderKey  string    `db:"default_chat_provider_key"`
	DefaultImageProviderKey string    `db:"default_image_provider_key"`
	UpdatedBy               string    `db:"updated_by"`
	UpdatedAt               time.Time `db:"updated_at"`
}

// DefaultProviderFor is the provider a route candidate's "" names for capability under these
// settings: the stored default chat / image provider, openrouter when it is blank, and "" for a
// capability that has no default. It is the registry's rule (registry snapshot.defaultProvider); the
// store resolves a route's slugs with it and the panel compares a route's two candidates with it.
func (s AISettings) DefaultProviderFor(capability string) string {
	var k string
	switch capability {
	case AICapabilityChat:
		k = s.DefaultChatProviderKey
	case AICapabilityImage:
		k = s.DefaultImageProviderKey
	default:
		return ""
	}
	if k = strings.TrimSpace(k); k != "" {
		return k
	}
	return AIProviderOpenRouter
}

// AIConfig is the whole configuration the registry snapshots, as of Settings.ConfigVersion.
type AIConfig struct {
	Providers      []AIProvider
	Models         []AIModel
	Routes         []AIRoute
	Settings       AISettings
	BudgetTimezone string // from design_settings: the zone day_local is computed in
}

// AIProviderPatch — a partial provider write; nil = keep.
type AIProviderPatch struct {
	Enabled *bool
}

// AIDefaultsPatch — a partial write of the two default providers; nil = keep.
type AIDefaultsPatch struct {
	ChatProviderKey, ImageProviderKey *string
}

// ───────────────────────── ledger rows ─────────────────────────

// AICallStart opens one ledger row (status dispatching) BEFORE the physical call.
//
// ATTRIBUTION IS BY ACCOUNT ID, FIXED AT WRITE TIME (D-10). A nil ActorAdminID is not "nobody": the
// store's INSERT resolves it from Actor there and then — the admins row carrying that username at the
// moment of the call — so a row stays with the account that made it even after that account is
// deleted and another is created under the same username. It stays NULL only when no admin carries
// the username (system, unknown, an account already gone).
type AICallStart struct {
	OccurredAt   time.Time // UTC
	DayLocal     string    // YYYY-MM-DD in the budget timezone (BudgetDayKey); aiprov.Ledger fills it when empty
	ProviderKey  string    // the BILLING transport
	Model        string    // the requested model
	Purpose      string
	Actor        string // the JWT username; aiprov.ActorSystem / ActorUnknown when nobody asked
	ActorAdminID *int   // admins.id when the caller knows it; nil = the store resolves it (see above)
	RunID        *int
	AttemptNo    *int
	CallNo       int // ≥ 1; 0 is read as 1
	FallbackFrom string
}

// AICallEnd finalises a ledger row. Nil pointers and empty strings leave the column as it is (NULL on
// a row that is still dispatching), so the collect of an async call keeps what the submit wrote.
type AICallEnd struct {
	Status           string
	ErrorCode        string
	HTTPStatus       *int
	Engaged          *bool
	RequestID        string
	ModelActual      string
	PromptTokens     *int
	CompletionTokens *int
	CachedTokens     *int
	ReasoningTokens  *int
	Units            *decimal.Decimal
	Unit             string
	CostUSD          decimal.NullDecimal // invalid = unknown, never zero
	CostSource       string
	PriceVersion     string
	LatencyMs        *int
}

// AISpendReport is the spend of an inclusive range of day_local days. Every USD is NULL (invalid)
// when no row of its group carries a price: unknown is never shown as zero.
type AISpendReport struct {
	FromDay, ToDay string
	Timezone       string // design_settings.budget_timezone: the zone the days are counted in
	TotalUSD       decimal.NullDecimal
	Calls          int
	Failed         int
	Unpriced       int
	ByProvider     []AISpendByProvider
	ByActor        []AISpendByActor
}

// AISpendByProvider — our ledger's number beside the provider's own (ai_provider_cost_daily).
type AISpendByProvider struct {
	ProviderKey string              `db:"provider_key"`
	OurUSD      decimal.NullDecimal `db:"our_usd"`
	TheirUSD    decimal.NullDecimal `db:"their_usd"`
	Calls       int                 `db:"calls"`
	Failed      int                 `db:"failed"`
	Unpriced    int                 `db:"unpriced"`
}

// AISpendByActor — who spent it, on what, through which provider and model.
type AISpendByActor struct {
	Actor        string              `db:"actor"`
	ActorAdminID *int                `db:"actor_admin_id"`
	Purpose      string              `db:"purpose"`
	ProviderKey  string              `db:"provider_key"`
	Model        string              `db:"model"`
	USD          decimal.NullDecimal `db:"usd"`
	Calls        int                 `db:"calls"`
}

// AICostDaily is one ai_provider_cost_daily row: the provider's own number for one day.
type AICostDaily struct {
	ProviderKey string
	Day         string // YYYY-MM-DD
	AmountUSD   decimal.Decimal
	Currency    string // "" = USD
	FetchedAt   time.Time
}
