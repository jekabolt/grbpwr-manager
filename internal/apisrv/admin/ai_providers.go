package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/keyring"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/pricing"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/probe"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ───────────────────────── admin → AI providers (B-15) ─────────────────────────
//
// Five handlers over one read. GetAiProvidersConfig is the whole panel; every write returns the same
// config, built by the same aiProvidersConfig, so the client never needs a follow-up read. Every RPC
// here is SuperOnly (rbac): keys and routes are money.
//
// THE WRITE SHAPE, the same in all four: validate everything the request names BEFORE the store is
// touched (an unknown provider or purpose writes nothing) → the store's checked write, whose
// compare-and-swap on ai_settings.config_version turns a stale page into FailedPrecondition "reload"
// instead of an overwrite → Reload the registry, so THIS instance serves the write at once (the others
// learn it from their poller within a minute) → the config.
//
// ⚠ A KEY VALUE GOES IN AND NEVER COMES OUT. SetAiProviderKey seals it, stores the ciphertext and
// probes it; no log line, no error message and no response carries it — only provider, kind, last4
// and who. That includes the refusals: an unknown provider is not echoed either, because a client
// that swapped two fields would otherwise get its key printed back in the error.

const (
	// aiStaleMessage — the compare-and-swap lost: somebody saved since this page loaded.
	aiStaleMessage = "the page is stale — reload"
	// aiNoMasterKeyMessage — the key ring has no master key: nothing can be sealed, so nothing is stored.
	aiNoMasterKeyMessage = "AI_KEYS_MASTER_KEY is not set on this server"
	// aiFaultWindow — how far back the provider badge looks (AiProviderInfo.fault_code).
	aiFaultWindow = 24 * time.Hour
	// aiModelMaxRunes — ai_route.model / ai_model.model are VARCHAR(128).
	aiModelMaxRunes = 128
	// aiKeyMaxBytes bounds a pasted key. Real keys are under 200 characters; the sealed blob must fit
	// ai_provider.*_key_enc VARBINARY(2048) with its 28 bytes of nonce and tag.
	aiKeyMaxBytes = 1024
	// aiNoteViaOpenRouter / aiNoteDesignOff — AiProviderInfo.note.
	aiNoteViaOpenRouter = "via openrouter"
	aiNoteDesignOff     = "design generation is off on this server"
)

// AIProvidersWiring is what the AI providers handlers need from app.go.
type AIProvidersWiring struct {
	// Registry is the live configuration (app.go a.aireg): key sources, breakers, Reload after a write.
	Registry *registry.Registry
	// KeyRing seals a key before it is stored; a disabled ring (no AI_KEYS_MASTER_KEY) refuses to store.
	KeyRing *keyring.Ring
	// RecraftViaOpenRouter — RECRAFT_ROUTE resolved to the OpenRouter route (recraft.Client.Route):
	// recraft's own key then answers nothing, and the panel says so.
	RecraftViaOpenRouter bool
	// ProbeClient is the http.Client a key probe uses; nil = the probe package's default (8 s).
	ProbeClient *http.Client
	// Reconcile runs one provider's cost fetch after an accepted admin-key save. Nil = no worker;
	// existing Server tests and deliberately-disabled deployments keep doing nothing.
	Reconcile func(ctx context.Context, provider string)
}

// SetAIProviders wires the AI providers panel. A Server without it refuses the five RPCs with
// FailedPrecondition rather than panicking on a nil registry.
func (s *Server) SetAIProviders(w AIProvidersWiring) {
	s.aiReg = w.Registry
	s.aiKeyRing = w.KeyRing
	s.aiRecraftViaOpenRouter = w.RecraftViaOpenRouter
	s.aiProbeClient = w.ProbeClient
	s.aiReconcile = w.Reconcile
}

func (s *Server) aiPanelReady() error {
	if s.aiReg == nil {
		return status.Error(codes.FailedPrecondition, "the AI provider registry is not wired on this server")
	}
	return nil
}

// ───────────────────────── read ─────────────────────────

// GetAiProvidersConfig returns the whole panel in one read.
func (s *Server) GetAiProvidersConfig(ctx context.Context, _ *pb_admin.GetAiProvidersConfigRequest) (*pb_admin.GetAiProvidersConfigResponse, error) {
	if err := s.aiPanelReady(); err != nil {
		return nil, err
	}
	return s.aiProvidersConfig(ctx)
}

// aiProvidersConfig is THE config builder: GetAiProvidersConfig and every write answer with it.
//
// ONE VERSION ON BOTH SIDES (Codex B #7). The rows come from the store (fresh); what the registry knows
// — which key answers, its last four, whether a stored admin key opens — from a registry snapshot.
// The two are joined only when they describe the SAME config_version: ProvidersAt hands out the states
// together with the version of the one snapshot they were rendered from (Version() and Providers()
// side by side could straddle a concurrent Reload). When the versions differ — another instance wrote
// since this one's last poll, or a reload raced this read — the registry is reloaded and BOTH are read
// again, once. Still different → a WARN names both versions and the read is refused with Unavailable
// (REVIEW-FIXD P2 #3), never built: a page that joins two versions shows, beside one version's rows, a
// key source and last four that are not the key answering (a cleared key still «set», or the reverse).
// Only two things keep the versions apart past the retry: a Reload that failed — it fails only when
// its own read of the same database fails — or a second write landing inside this one read; either
// way the answer is the admin's «reload», and after a write the reloaded page shows the save. A
// failed badge read degrades to no badge.
func (s *Server) aiProvidersConfig(ctx context.Context) (*pb_admin.GetAiProvidersConfigResponse, error) {
	cfg, err := s.aiReadConfig(ctx)
	if err != nil {
		return nil, err
	}
	states, regVersion := s.aiReg.ProvidersAt()
	if regVersion != cfg.Settings.ConfigVersion {
		if err := s.aiReg.Reload(ctx); err != nil {
			slog.Default().WarnContext(ctx, "ai registry: reload for the panel failed; key state may lag until the next poll",
				slog.String("err", err.Error()))
		}
		if cfg, err = s.aiReadConfig(ctx); err != nil {
			return nil, err
		}
		states, regVersion = s.aiReg.ProvidersAt()
		if regVersion != cfg.Settings.ConfigVersion {
			slog.Default().WarnContext(ctx, "ai panel: the registry and the store still describe different config versions after a reload; the read is refused rather than joining them",
				slog.Uint64("store_version", cfg.Settings.ConfigVersion), slog.Uint64("registry_version", regVersion))
			return nil, status.Error(codes.Unavailable, "the AI configuration changed while it was being read; reload")
		}
	}
	faults, err := s.repo.AI().RecentFaults(ctx, time.Now().Add(-aiFaultWindow))
	if err != nil {
		slog.Default().WarnContext(ctx, "can't read the ai provider fault badges; the panel shows none",
			slog.String("err", err.Error()))
		faults = nil
	}
	return dto.AIConfigToPb(aiConfigView(cfg, states, faults, aiViewFlags{
		masterKeyPresent:        s.aiKeyRing.Enabled(),
		designGenerationEnabled: s.designGenerationEnabled,
		recraftViaOpenRouter:    s.aiRecraftViaOpenRouter,
	})), nil
}

// aiReadConfig is the store's configuration, or the panel's read error.
func (s *Server) aiReadConfig(ctx context.Context) (*entity.AIConfig, error) {
	cfg, err := s.repo.AI().GetConfig(ctx)
	if err == nil && cfg == nil {
		err = errors.New("the store returned no configuration")
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't read the ai provider configuration", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read the AI provider configuration; reload")
	}
	return cfg, nil
}

// aiViewFlags — the server facts the view needs besides the rows.
type aiViewFlags struct {
	masterKeyPresent, designGenerationEnabled, recraftViaOpenRouter bool
}

// aiConfigView joins the store's rows, the registry's live state, the fault badges and the code
// tables (purposes, pricing) into the panel. Pure: every input is an argument.
func aiConfigView(cfg *entity.AIConfig, states []registry.ProviderState, faults map[string]string, f aiViewFlags) dto.AIConfigView {
	rows := make(map[string]entity.AIProvider, len(cfg.Providers))
	for _, p := range cfg.Providers {
		rows[p.Key] = p
	}
	custom := map[string][]entity.AIModel{}
	for _, m := range cfg.Models {
		if !m.Disabled {
			custom[m.ProviderKey] = append(custom[m.ProviderKey], m)
		}
	}
	routes := make(map[string][]entity.AIRouteCandidate, len(cfg.Routes))
	for _, r := range cfg.Routes {
		routes[r.Purpose] = r.Candidates // ordered by position (store.groupRoutes)
	}

	v := dto.AIConfigView{
		Providers:               make([]dto.AIProviderView, 0, len(states)),
		DefaultChatProviderKey:  cfg.Settings.DefaultChatProviderKey,
		DefaultImageProviderKey: cfg.Settings.DefaultImageProviderKey,
		ConfigVersion:           cfg.Settings.ConfigVersion,
		MasterKeyPresent:        f.masterKeyPresent,
		Timezone:                cfg.BudgetTimezone,
		PriceVersion:            pricing.Version,
		DesignGenerationEnabled: f.designGenerationEnabled,
	}
	for _, st := range states {
		pv := dto.AIProviderView{
			Key:               st.Key,
			Label:             st.Key,
			Enabled:           st.Enabled,
			Capabilities:      entity.AIProviderCapabilities(st.Key),
			KeySource:         st.KeySource,
			KeyLast4:          st.KeyLast4,
			AdminKeySupported: aiAdminKeySupported(st.Key),
			AdminKeySource:    registry.KeySourceNone,
			Breaker:           st.Breaker,
			FaultCode:         faults[st.Key],
			Note:              aiProviderNote(st.Key, f),
			Models:            aiModels(st.Key, custom[st.Key]),
		}
		if row, ok := rows[st.Key]; ok {
			if row.Label != "" {
				pv.Label = row.Label
			}
			// The switch as SAVED — the page shows what the last write set, the moment it returns.
			pv.Enabled = row.Enabled
			// Who stored the key and when — only while a key IS stored: a cleared slot still records
			// who cleared it, and "set by im" beside "key: from env" would be a lie.
			if len(row.APIKeyEnc) > 0 {
				pv.KeyUpdatedBy = row.APIKeyUpdatedBy
				pv.KeyUpdatedAt = row.APIKeyUpdatedAt
			}
			if pv.AdminKeySupported && len(row.AdminKeyEnc) > 0 {
				if st.AdminKeySet {
					pv.AdminKeySource = registry.KeySourceDB
					pv.AdminKeyLast4 = row.AdminKeyLast4
				} else {
					pv.AdminKeySource = registry.KeySourceUnreadable
				}
			}
		}
		v.Providers = append(v.Providers, pv)
	}
	for _, p := range aiprov.Purposes() {
		pv := dto.AIPurposeView{Key: p.Key, Label: p.Label, Hint: p.Hint, Group: p.Group, Capability: p.Capability}
		if cands := routes[p.Key]; len(cands) > 0 {
			primary := cands[0]
			pv.Primary = &primary
			if len(cands) > 1 {
				fallback := cands[1]
				pv.Fallback = &fallback
			}
		}
		v.Purposes = append(v.Purposes, pv)
	}
	return v
}

// aiAdminKeySupported — the providers whose cost API needs a separate reconciliation key (D-06):
// exactly the admin-kind rows of the probe table (a test holds the two together).
func aiAdminKeySupported(provider string) bool {
	switch provider {
	case entity.AIProviderOpenAI, entity.AIProviderAnthropic, entity.AIProviderFal:
		return true
	}
	return false
}

// aiServesDesign — the provider serves a capability only the DESIGN band's generation uses.
func aiServesDesign(provider string) bool {
	for _, c := range entity.AIProviderCapabilities(provider) {
		switch c {
		case entity.AICapabilityImage, entity.AICapabilityCutout, entity.AICapabilityEdit,
			entity.AICapabilityThreed, entity.AICapabilityVector:
			return true
		}
	}
	return false
}

// aiProviderNote is the one short sentence beside a provider. "design generation is off" wins over
// "via openrouter": with generation off, recraft is not called by any route at all.
func aiProviderNote(provider string, f aiViewFlags) string {
	if !f.designGenerationEnabled && aiServesDesign(provider) {
		return aiNoteDesignOff
	}
	if provider == entity.AIProviderRecraft && f.recraftViaOpenRouter {
		return aiNoteViaOpenRouter
	}
	return ""
}

// aiModels is a provider's model list: the curated catalogue first (priced = a rate is on file), then
// the ai_model rows the catalogue does not already name. CUSTOM IS DECIDED HERE: SetRoute records
// every slug a route names in ai_model (the store does not know the catalogue), so a row whose slug
// the catalogue names is the catalogue's entry, listed once and not as custom.
func aiModels(provider string, custom []entity.AIModel) []dto.AIModelView {
	cat := pricing.Catalogue(provider)
	out := make([]dto.AIModelView, 0, len(cat)+len(custom))
	seen := make(map[string]bool, len(cat)+len(custom))
	for _, m := range cat {
		seen[m.Slug] = true
		out = append(out, dto.AIModelView{
			Slug: m.Slug, Label: m.Label, Kind: m.Kind,
			Priced: m.PerCallUSD.Valid || (m.InputUSDPer1M.Valid && m.OutputUSDPer1M.Valid),
		})
	}
	for _, m := range custom {
		if seen[m.Model] {
			continue
		}
		seen[m.Model] = true
		label := m.Label
		if label == "" {
			label = m.Model
		}
		out = append(out, dto.AIModelView{Slug: m.Model, Label: label, Kind: m.Kind, Custom: true})
	}
	return out
}

// ───────────────────────── writes ─────────────────────────

// UpdateAiProvider switches one provider on or off.
func (s *Server) UpdateAiProvider(ctx context.Context, req *pb_admin.UpdateAiProviderRequest) (*pb_admin.UpdateAiProviderResponse, error) {
	if err := s.aiPanelReady(); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.GetProviderKey())
	if !entity.IsAIProviderKey(key) {
		return nil, aiUnknownProvider()
	}
	enabled := req.GetEnabled()
	by := authsrv.GetAdminUsername(ctx)
	if err := s.repo.AI().UpdateProvider(ctx, key, entity.AIProviderPatch{Enabled: &enabled}, req.GetExpectedVersion(), by); err != nil {
		return nil, aiWriteError(ctx, "switch an ai provider", err)
	}
	slog.Default().InfoContext(ctx, "ai provider switched",
		slog.String("provider", key), slog.Bool("enabled", enabled), slog.String("by", by))
	cfg, err := s.aiAfterWrite(ctx)
	if err != nil {
		return nil, err
	}
	return &pb_admin.UpdateAiProviderResponse{Config: cfg}, nil
}

// SetAiProviderKey stores (sealed) or clears one key slot, then probes the just-saved key with the
// provider's free endpoint. See the file header: the value never leaves this function except sealed
// into the store and into the probe's request to the provider's own constant host.
func (s *Server) SetAiProviderKey(ctx context.Context, req *pb_admin.SetAiProviderKeyRequest) (*pb_admin.SetAiProviderKeyResponse, error) {
	if err := s.aiPanelReady(); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.GetProviderKey())
	if !entity.IsAIProviderKey(key) {
		return nil, aiUnknownProvider()
	}
	kind := entity.AIKeyKind(strings.TrimSpace(req.GetKind()))
	switch kind {
	case entity.AIKeyAPI:
	case entity.AIKeyAdmin:
		if !aiAdminKeySupported(key) {
			return nil, apierr.Invalid(entity.NewFieldViolation("kind", "admin_key_not_supported", "",
				"only openai, anthropic and fal take a reconciliation key"))
		}
	default:
		return nil, apierr.Invalid(entity.NewFieldViolation("kind", "unknown_ai_key_kind", "", "name api or admin"))
	}
	value := strings.TrimSpace(req.GetValue())
	by := authsrv.GetAdminUsername(ctx)

	// "" clears the slot: nothing is sealed (no master key needed) and nothing is probed.
	if value == "" {
		if err := s.repo.AI().SetProviderKey(ctx, key, kind, nil, "", by); err != nil {
			return nil, aiWriteError(ctx, "clear an ai provider key", err)
		}
		slog.Default().InfoContext(ctx, "ai provider key cleared",
			slog.String("provider", key), slog.String("kind", string(kind)), slog.String("by", by))
		s.aiKeyWritten(ctx, key, kind)
		cfg, err := s.aiProvidersConfig(ctx)
		if err != nil {
			return nil, err
		}
		return &pb_admin.SetAiProviderKeyResponse{Config: cfg}, nil
	}

	if len(value) > aiKeyMaxBytes {
		return nil, apierr.Invalid(entity.NewFieldViolation("value", "key_too_long", "",
			"a provider key is at most 1024 characters; paste the key alone"))
	}
	if !aiKeyPrintable(value) {
		return nil, apierr.Invalid(entity.NewFieldViolation("value", "key_not_printable", "",
			"paste the key alone: no spaces, line breaks or non-ASCII characters"))
	}
	if !s.aiKeyRing.Enabled() {
		return nil, status.Error(codes.FailedPrecondition, aiNoMasterKeyMessage)
	}
	enc, err := s.aiKeyRing.Seal(value, keyring.AAD(key, string(kind)))
	if err != nil {
		if errors.Is(err, keyring.ErrNoMasterKey) {
			return nil, status.Error(codes.FailedPrecondition, aiNoMasterKeyMessage)
		}
		// keyring errors never carry the plaintext (keyring package doc).
		slog.Default().ErrorContext(ctx, "can't seal an ai provider key",
			slog.String("provider", key), slog.String("kind", string(kind)), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't seal the key; try again")
	}
	last4 := keyring.Last4(value)
	if err := s.repo.AI().SetProviderKey(ctx, key, kind, enc, last4, by); err != nil {
		return nil, aiWriteError(ctx, "store an ai provider key", err)
	}
	s.aiKeyWritten(ctx, key, kind)

	// The probe gets the JUST-SAVED key itself, not the registry's answer: for an api key the
	// registry would answer "" while the provider is switched off, and an admin key is never served.
	res := probe.Probe(ctx, key, kind, value, s.aiProbeClient)
	slog.Default().InfoContext(ctx, "ai provider key saved",
		slog.String("provider", key), slog.String("kind", string(kind)), slog.String("last4", last4),
		slog.String("by", by), slog.Bool("probe_ok", res.OK), slog.String("probe_code", res.Code))
	reconcile := s.aiReconcile
	if kind == entity.AIKeyAdmin && res.OK && reconcile != nil {
		// The RPC may end the moment this goroutine starts. Keep its values for tracing, detach its
		// cancellation, and impose a worker-sized deadline so a provider cannot outlive shutdown.
		go func() {
			runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			reconcile(runCtx, key)
		}()
	}

	cfg, err := s.aiProvidersConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &pb_admin.SetAiProviderKeyResponse{Probe: dto.AIProbeResultToPb(res), Config: cfg}, nil
}

// aiKeyWritten makes this instance serve a key write now: Reload (a rotated api key reaches the next
// request; Reload itself also resets the breakers when the answering key changed), then — for an api
// key — ResetBreakers regardless: a key saved is a new chance even when its value happens to be the one
// already in force. An admin key serves no call and leaves the breakers alone.
func (s *Server) aiKeyWritten(ctx context.Context, provider string, kind entity.AIKeyKind) {
	if err := s.aiReg.Reload(ctx); err != nil {
		slog.Default().WarnContext(ctx, "ai registry: reload after a key write failed; the poller will pick it up",
			slog.String("provider", provider), slog.String("err", err.Error()))
	}
	if kind == entity.AIKeyAPI {
		s.aiReg.ResetBreakers(provider)
	}
}

// SetAiDefaults sets the default chat and/or image provider; "" leaves that one as it is.
func (s *Server) SetAiDefaults(ctx context.Context, req *pb_admin.SetAiDefaultsRequest) (*pb_admin.SetAiDefaultsResponse, error) {
	if err := s.aiPanelReady(); err != nil {
		return nil, err
	}
	var patch entity.AIDefaultsPatch
	if chat := strings.TrimSpace(req.GetChatProviderKey()); chat != "" {
		if err := aiCheckProvider("chat_provider_key", chat, entity.AICapabilityChat); err != nil {
			return nil, err
		}
		patch.ChatProviderKey = &chat
	}
	if image := strings.TrimSpace(req.GetImageProviderKey()); image != "" {
		if err := aiCheckProvider("image_provider_key", image, entity.AICapabilityImage); err != nil {
			return nil, err
		}
		patch.ImageProviderKey = &image
	}
	if patch.ChatProviderKey == nil && patch.ImageProviderKey == nil {
		return nil, apierr.Invalid(entity.NewFieldViolation("chat_provider_key", "no_settings_named", "",
			"name a default chat provider, a default image provider, or both"))
	}
	by := authsrv.GetAdminUsername(ctx)
	if err := s.repo.AI().SetDefaults(ctx, patch, req.GetExpectedVersion(), by); err != nil {
		return nil, aiWriteError(ctx, "set the ai default providers", err)
	}
	slog.Default().InfoContext(ctx, "ai default providers set",
		slog.String("chat", derefOr(patch.ChatProviderKey, "(unchanged)")),
		slog.String("image", derefOr(patch.ImageProviderKey, "(unchanged)")),
		slog.String("by", by))
	cfg, err := s.aiAfterWrite(ctx)
	if err != nil {
		return nil, err
	}
	return &pb_admin.SetAiDefaultsResponse{Config: cfg}, nil
}

// SetAiRoute sets one purpose's route: the primary and an optional fallback, which must not be the
// primary itself (aiSameCandidate). The store records every slug the route names in ai_model INSIDE
// the route's own checked transaction (Codex B #6): the route and its models commit together or not
// at all, a stale page records nothing, and a "" provider's slug is filed under the default that
// transaction read — the one the route follows.
func (s *Server) SetAiRoute(ctx context.Context, req *pb_admin.SetAiRouteRequest) (*pb_admin.SetAiRouteResponse, error) {
	if err := s.aiPanelReady(); err != nil {
		return nil, err
	}
	purpose, ok := aiprov.PurposeInfo(strings.TrimSpace(req.GetPurpose()))
	if !ok {
		return nil, status.Error(codes.NotFound, "unknown AI purpose; name one of: "+strings.Join(entity.AIPurposes(), ", "))
	}
	if req.GetPrimary() == nil {
		return nil, apierr.Invalid(entity.NewFieldViolation("primary", "route_primary_required", "",
			"name the primary; an empty provider means the default one (chat and image only)"))
	}
	primary, err := aiRouteCandidate("primary", req.GetPrimary(), purpose.Capability)
	if err != nil {
		return nil, err
	}
	primary.Position = 1
	cands := []entity.AIRouteCandidate{primary}
	if req.GetFallback() != nil {
		fallback, err := aiRouteCandidate("fallback", req.GetFallback(), purpose.Capability)
		if err != nil {
			return nil, err
		}
		same, err := s.aiSameCandidate(ctx, purpose, primary, fallback, req.GetExpectedVersion())
		if err != nil {
			return nil, err
		}
		if same {
			return nil, apierr.Invalid(entity.NewFieldViolation("fallback", "same_as_primary", "",
				"the fallback is the primary itself; choose another provider or model, or no fallback"))
		}
		fallback.Position = 2
		cands = append(cands, fallback)
	}
	by := authsrv.GetAdminUsername(ctx)
	if err := s.repo.AI().SetRoute(ctx, purpose.Key, cands, req.GetExpectedVersion(), by); err != nil {
		return nil, aiWriteError(ctx, "set an ai route", err)
	}
	slog.Default().InfoContext(ctx, "ai route set",
		slog.String("purpose", purpose.Key), slog.Any("candidates", cands), slog.String("by", by))
	cfg, err := s.aiAfterWrite(ctx)
	if err != nil {
		return nil, err
	}
	return &pb_admin.SetAiRouteResponse{Config: cfg}, nil
}

// aiRouteCandidate validates one candidate of a route for a purpose of this capability.
func aiRouteCandidate(field string, c *pb_admin.AiRouteCandidate, capability string) (entity.AIRouteCandidate, error) {
	provider := strings.TrimSpace(c.GetProviderKey())
	model := strings.TrimSpace(c.GetModel())
	if provider == "" {
		// "" = the capability's default provider, and only chat and image have one.
		if capability != entity.AICapabilityChat && capability != entity.AICapabilityImage {
			return entity.AIRouteCandidate{}, apierr.Invalid(entity.NewFieldViolation(field+".provider_key",
				"provider_required", "", capability+" has no default provider; name one"))
		}
	} else if err := aiCheckProvider(field+".provider_key", provider, capability); err != nil {
		return entity.AIRouteCandidate{}, err
	}
	if utf8.RuneCountInString(model) > aiModelMaxRunes {
		return entity.AIRouteCandidate{}, apierr.Invalid(entity.NewFieldViolation(field+".model",
			"model_too_long", "", "a model slug is at most 128 characters"))
	}
	return entity.AIRouteCandidate{ProviderKey: provider, Model: model}, nil
}

// aiSameCandidate reports whether a route's fallback is its primary IN EFFECT (Codex B #8, FIX-D P2):
// the same provider — "" resolved to the capability's default on BOTH sides — and the same slug — ""
// resolved the way the router resolves it (aiEffectiveSlug: the purpose's env default for an
// openrouter row). The router skips a repeat of a provider and slug in one chain, so such a fallback
// would be saved and shown while the runtime has no fallback at all; «openrouter / ""» against
// «openrouter / <the slug that "" becomes>» is exactly that.
//
// Only a comparison that hinges on the default PROVIDER reads the configuration, and its verdict
// counts only when that read is the version the page saved against: the route write is a
// compare-and-swap on expectedVersion, so at any other version the write is refused as stale anyway,
// and a default read from another version is not the one the route would follow.
func (s *Server) aiSameCandidate(ctx context.Context, purpose aiprov.Purpose, primary, fallback entity.AIRouteCandidate, expectedVersion uint64) (bool, error) {
	pp, fp := primary.ProviderKey, fallback.ProviderKey
	if pp == fp && primary.Model == fallback.Model {
		return true, nil // literally the same row, the default twice included
	}
	if (pp != "" && fp != "" && pp != fp) ||
		(primary.Model != "" && fallback.Model != "" && primary.Model != fallback.Model) {
		return false, nil // two named providers, or two named slugs, that differ: no default can join them
	}
	if pp == "" || fp == "" {
		cfg, err := s.aiReadConfig(ctx)
		if err != nil {
			return false, err
		}
		if cfg.Settings.ConfigVersion != expectedVersion {
			return false, nil
		}
		if pp == "" {
			pp = cfg.Settings.DefaultProviderFor(purpose.Capability)
		}
		if fp == "" {
			fp = cfg.Settings.DefaultProviderFor(purpose.Capability)
		}
		if pp != fp {
			return false, nil
		}
	}
	return s.aiEffectiveSlug(purpose.Key, pp, primary.Model) == s.aiEffectiveSlug(purpose.Key, fp, fallback.Model), nil
}

// aiEffectiveSlug is the slug a route row is CALLED with: its own model, else the router's default for
// the purpose on that provider (router.EffectiveModel — OPENROUTER_MODEL, _ANALYSIS, _IDEAS for an
// openrouter row), else "" (a provider whose client picks its own default).
func (s *Server) aiEffectiveSlug(purpose, providerKey, model string) string {
	if m := s.ai.EffectiveModel(purpose, registry.Candidate{ProviderKey: providerKey, Model: model}); m != "" {
		return m
	}
	return model
}

// ───────────────────────── helpers ─────────────────────────

// aiAfterWrite reloads the registry after a config write and answers with the config.
func (s *Server) aiAfterWrite(ctx context.Context) (*pb_admin.GetAiProvidersConfigResponse, error) {
	if err := s.aiReg.Reload(ctx); err != nil {
		slog.Default().WarnContext(ctx, "ai registry: reload after a write failed; the poller will pick it up",
			slog.String("err", err.Error()))
	}
	return s.aiProvidersConfig(ctx)
}

// aiCheckProvider refuses a provider that is unknown or does not serve the capability — before any
// write. The key is not echoed (see the file header).
func aiCheckProvider(field, provider, capability string) error {
	if !entity.IsAIProviderKey(provider) {
		return apierr.Invalid(entity.NewFieldViolation(field, "unknown_ai_provider", "",
			"name one of: "+strings.Join(entity.AIProviderKeys(), ", ")))
	}
	if !entity.AIProviderServes(provider, capability) {
		return apierr.Invalid(entity.NewFieldViolation(field, "provider_cannot_serve", "",
			provider+" does not serve "+capability+"; choose one that does"))
	}
	return nil
}

func aiUnknownProvider() error {
	return status.Error(codes.NotFound, "unknown AI provider; name one of: "+strings.Join(entity.AIProviderKeys(), ", "))
}

// aiWriteError maps a store error of a config write.
func aiWriteError(ctx context.Context, op string, err error) error {
	var ve *entity.ValidationError
	switch {
	case errors.Is(err, entity.ErrAIVersionConflict):
		return status.Error(codes.FailedPrecondition, aiStaleMessage)
	case errors.As(err, &ve):
		return apierr.Invalid(ve)
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, "this AI provider has no row in the configuration; reload")
	}
	slog.Default().ErrorContext(ctx, "can't "+op, slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't save the AI provider configuration; try again")
}

// aiKeyPrintable — printable ASCII with no spaces: the only shape a provider key has, and the only one
// the probe (and every client) can put in a header unchanged.
func aiKeyPrintable(v string) bool {
	for i := 0; i < len(v); i++ {
		if v[i] <= ' ' || v[i] >= 0x7f {
			return false
		}
	}
	return true
}

func derefOr(p *string, or string) string {
	if p == nil {
		return or
	}
	return *p
}
