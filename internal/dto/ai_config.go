package dto

import (
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov/probe"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ───────────────────────── admin → AI providers: the config wire (B-15) ─────────────────────────
//
// The panel's config is joined from four places — the store's rows, the registry's live state, the
// ledger's fault badge and code tables (purposes, pricing) — and the join lives with the handlers
// (apisrv/admin/ai_providers.go): this package cannot import the registry (registry → dependency →
// dto). What arrives here is the joined view, field for field the wire; these converters only say
// how each field travels. NO FIELD CARRIES A KEY — only where it comes from and its last four.

// AIModelView is one model of a provider: curated (the pricing catalogue) or custom (an ai_model row).
type AIModelView struct {
	Slug, Label, Kind string
	Custom, Priced    bool
}

// AIProviderView is one provider as the panel shows it.
type AIProviderView struct {
	Key, Label        string
	Enabled           bool
	Capabilities      []string
	KeySource         string     // none | env | db | unreadable
	KeyLast4          string     // of the key that answers
	KeyUpdatedBy      string     // "" when no key is stored
	KeyUpdatedAt      *time.Time // nil when no key is stored
	AdminKeySupported bool
	AdminKeySource    string // none | db | unreadable
	AdminKeyLast4     string
	Breaker           string // closed | open | half-open
	FaultCode         string // key_rejected | out_of_credits | model_unknown | ""
	Note              string
	Models            []AIModelView
}

// AIPurposeView is one purpose with its route; Fallback nil = no fallback, Primary nil = no route row.
type AIPurposeView struct {
	Key, Label, Hint, Group, Capability string
	Primary, Fallback                   *entity.AIRouteCandidate
}

// AIConfigView is the whole panel.
type AIConfigView struct {
	Providers                                       []AIProviderView
	Purposes                                        []AIPurposeView
	DefaultChatProviderKey, DefaultImageProviderKey string
	ConfigVersion                                   uint64
	MasterKeyPresent                                bool
	Timezone, PriceVersion                          string
	DesignGenerationEnabled                         bool
}

// AIConfigToPb converts the joined panel view.
func AIConfigToPb(v AIConfigView) *pb_admin.GetAiProvidersConfigResponse {
	out := &pb_admin.GetAiProvidersConfigResponse{
		Providers:               make([]*pb_admin.AiProviderInfo, 0, len(v.Providers)),
		Purposes:                make([]*pb_admin.AiPurposeInfo, 0, len(v.Purposes)),
		DefaultChatProviderKey:  v.DefaultChatProviderKey,
		DefaultImageProviderKey: v.DefaultImageProviderKey,
		ConfigVersion:           v.ConfigVersion,
		MasterKeyPresent:        v.MasterKeyPresent,
		Timezone:                v.Timezone,
		PriceVersion:            v.PriceVersion,
		DesignGenerationEnabled: v.DesignGenerationEnabled,
	}
	for _, p := range v.Providers {
		out.Providers = append(out.Providers, aiProviderToPb(p))
	}
	for _, p := range v.Purposes {
		out.Purposes = append(out.Purposes, &pb_admin.AiPurposeInfo{
			Key:        p.Key,
			Label:      p.Label,
			Hint:       p.Hint,
			Group:      p.Group,
			Capability: p.Capability,
			Primary:    AIRouteCandidateToPb(p.Primary),
			Fallback:   AIRouteCandidateToPb(p.Fallback),
		})
	}
	return out
}

func aiProviderToPb(p AIProviderView) *pb_admin.AiProviderInfo {
	info := &pb_admin.AiProviderInfo{
		Key:               p.Key,
		Label:             p.Label,
		Enabled:           p.Enabled,
		Capabilities:      append([]string(nil), p.Capabilities...),
		KeySource:         p.KeySource,
		KeyLast4:          p.KeyLast4,
		KeyUpdatedBy:      p.KeyUpdatedBy,
		AdminKeySupported: p.AdminKeySupported,
		AdminKeySource:    p.AdminKeySource,
		AdminKeyLast4:     p.AdminKeyLast4,
		Breaker:           p.Breaker,
		FaultCode:         p.FaultCode,
		Note:              p.Note,
		Models:            make([]*pb_admin.AiModelInfo, 0, len(p.Models)),
	}
	// Unset, not the zero instant, when no key is stored: absence is the wire's "none".
	if p.KeyUpdatedAt != nil && !p.KeyUpdatedAt.IsZero() {
		info.KeyUpdatedAt = timestamppb.New(*p.KeyUpdatedAt)
	}
	for _, m := range p.Models {
		info.Models = append(info.Models, &pb_admin.AiModelInfo{
			Slug: m.Slug, Label: m.Label, Kind: m.Kind, Custom: m.Custom, Priced: m.Priced,
		})
	}
	return info
}

// AIRouteCandidateToPb converts one route candidate; nil stays nil (an absent fallback).
func AIRouteCandidateToPb(c *entity.AIRouteCandidate) *pb_admin.AiRouteCandidate {
	if c == nil {
		return nil
	}
	return &pb_admin.AiRouteCandidate{ProviderKey: c.ProviderKey, Model: c.Model}
}

// AIProbeResultToPb converts what a key's free probe said. probe.Result carries no key material by
// construction (see the probe package), so nothing here can echo one.
func AIProbeResultToPb(r probe.Result) *pb_admin.AiProbeResult {
	return &pb_admin.AiProbeResult{Ok: r.OK, Code: r.Code, Message: r.Message, Balance: r.Balance}
}
