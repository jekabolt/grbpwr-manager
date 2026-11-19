// Package feature defines the feature catalog and the feature requirements for
// admin RPCs and non-gRPC admin routes.
package feature

import (
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/rbac"
)

// Key is a stable feature identifier stored in the control-plane database.
type Key string

const (
	Core           Key = "core"
	Tasks          Key = "tasks"
	Files          Key = "files"
	Media          Key = "media"
	Models         Key = "models"
	Fittings       Key = "fittings"
	Materials      Key = "materials"
	Production     Key = "production"
	TechCards      Key = "tech_cards"
	AI             Key = "ai"
	Products       Key = "products"
	Orders         Key = "orders"
	Fulfillment    Key = "fulfillment"
	Promo          Key = "promo"
	Hero           Key = "hero"
	Archive        Key = "archive"
	EmailCampaigns Key = "email_campaigns"
	Membership     Key = "membership"
	Support        Key = "support"
	Analytics      Key = "analytics"
	Accounting     Key = "accounting"
	Settings       Key = "settings"
	AIProviders    Key = "ai_providers"
	LocalAccounts  Key = "local_accounts"
)

// Info describes one switchable team feature.
type Info struct {
	Key         Key
	Title       string
	Group       string
	DefaultOn   bool
	TeamOneOnly bool
}

var catalog = []Info{
	// operations
	{Key: Analytics, Title: "analytics", Group: "operations"},
	{Key: Accounting, Title: "accounting", Group: "operations"},
	{Key: Orders, Title: "orders", Group: "operations"},
	{Key: Fulfillment, Title: "fulfillment", Group: "operations"},
	{Key: Tasks, Title: "tasks", Group: "operations", DefaultOn: true},
	{Key: Files, Title: "files", Group: "operations", DefaultOn: true},

	// catalog
	{Key: Products, Title: "products", Group: "catalog"},
	{Key: Media, Title: "media", Group: "catalog", DefaultOn: true},
	{Key: Hero, Title: "hero", Group: "catalog"},
	{Key: Promo, Title: "promo", Group: "catalog"},
	{Key: EmailCampaigns, Title: "email", Group: "catalog"},
	{Key: Archive, Title: "timeline", Group: "catalog"},

	// production
	{Key: Models, Title: "models", Group: "production", DefaultOn: true},
	{Key: Fittings, Title: "fittings", Group: "production", DefaultOn: true},
	{Key: TechCards, Title: "tech cards", Group: "production", DefaultOn: true},
	{Key: Materials, Title: "materials", Group: "production", DefaultOn: true},
	{Key: Production, Title: "production", Group: "production", DefaultOn: true},

	// members
	{Key: Membership, Title: "members", Group: "members"},
	{Key: Support, Title: "support", Group: "members"},

	// admin
	// Dictionaries (colours, collections, sizes) are not a switch: every team has
	// them (owner decision 18.11), so the section maps to Core below.
	{Key: Settings, Title: "settings", Group: "admin"},
	{Key: AI, Title: "ai", Group: "admin", DefaultOn: true},
	{Key: AIProviders, Title: "ai providers", Group: "admin", TeamOneOnly: true},
	{Key: LocalAccounts, Title: "local accounts", Group: "admin", TeamOneOnly: true},
}

var defaultOn = []Key{
	Tasks,
	Files,
	Media,
	Models,
	Fittings,
	Materials,
	Production,
	TechCards,
	AI,
}

// Catalog returns the ordered catalog of switchable team features. Core is not
// switchable and is therefore not included.
func Catalog() []Info {
	return append([]Info(nil), catalog...)
}

// DefaultOn returns the standard-profile defaults in their canonical order.
func DefaultOn() []Key {
	return append([]Key(nil), defaultOn...)
}

// Valid reports whether k is a switchable feature key.
func Valid(k Key) bool {
	for _, info := range catalog {
		if info.Key == k {
			return true
		}
	}
	return false
}

// Allowed reports whether all required keys are enabled. Core is always on;
// an empty or unknown requirement fails closed.
func Allowed(eff map[Key]bool, keys []Key) bool {
	if len(keys) == 0 {
		return false
	}
	for _, key := range keys {
		if key == Core {
			continue
		}
		if !Valid(key) || !eff[key] {
			return false
		}
	}
	return true
}

// Effective computes the effective feature switches for a team.
func Effective(teamID int64, profile string, overrides map[string]bool) map[Key]bool {
	effective := make(map[Key]bool, len(catalog))
	for _, info := range catalog {
		if info.TeamOneOnly && teamID != 1 {
			effective[info.Key] = false
			continue
		}
		if enabled, ok := overrides[string(info.Key)]; ok {
			effective[info.Key] = enabled
			continue
		}
		if profile == "all" {
			effective[info.Key] = true
			continue
		}
		effective[info.Key] = info.DefaultOn
	}
	return effective
}

// SectionFeature is the feature key an rbac section belongs to (Core = always on).
func SectionFeature(section string) (Key, bool) {
	k, ok := sectionFeatures[section]
	return k, ok
}

var sectionFeatures = map[string]Key{
	rbac.SectionProducts:     Products,
	rbac.SectionPromo:        Promo,
	rbac.SectionOrders:       Orders,
	rbac.SectionFulfillment:  Fulfillment,
	rbac.SectionAnalytics:    Analytics,
	rbac.SectionContent:      Media,
	rbac.SectionHero:         Hero,
	rbac.SectionArchive:      Archive,
	rbac.SectionCampaigns:    EmailCampaigns,
	rbac.SectionModels:       Models,
	rbac.SectionFittings:     Fittings,
	rbac.SectionDictionaries: Core,
	rbac.SectionTechCards:    TechCards,
	rbac.SectionProduction:   Production,
	rbac.SectionInventory:    Materials,
	rbac.SectionCosting:      TechCards,
	rbac.SectionTasks:        Tasks,
	rbac.SectionFiles:        Files,
	rbac.SectionSettings:     Settings,
	rbac.SectionSupport:      Support,
	rbac.SectionMembership:   Membership,
	rbac.SectionAccounts:     Core,
	rbac.SectionAccounting:   Accounting,
}

// methodOverrides is intentionally explicit. The test suite checks every name
// against AdminService_ServiceDesc so stale entries cannot hide a renamed RPC.
var methodOverrides = map[string][]Key{
	// Materials live independently of the tech-card and accounting sections that
	// currently protect their RPCs.
	"CreateMaterial":       {Materials},
	"UpdateMaterial":       {Materials},
	"ArchiveMaterial":      {Materials},
	"GetMaterial":          {Materials},
	"ListMaterials":        {Materials},
	"AddMaterialPrice":     {Materials},
	"ListMaterialPrices":   {Materials},
	"GetCostingFxRates":    {Materials},
	"UpsertCostingFxRates": {Materials},
	"CreateSupplier":       {Materials},
	"ListSuppliers":        {Materials},

	// Process-wide provider configuration exists only in team one.
	"GetAiProvidersConfig": {AIProviders},
	"UpdateAiProvider":     {AIProviders},
	"SetAiProviderKey":     {AIProviders},
	"SetAiDefaults":        {AIProviders},
	"SetAiRoute":           {AIProviders},
	"GetAiSpendReport":     {AIProviders},

	// These mutate credentials in the legacy per-team admins table.
	"CreateAccount":        {LocalAccounts},
	"DeleteAccount":        {LocalAccounts},
	"ResetAccountPassword": {LocalAccounts},

	// P0 adds these RPCs. Mapping them now keeps the feature gate fail-closed
	// across the merge order of the two lanes.
	"ListTeamMembers":   {Core},
	"InviteTeamMember":  {Core},
	"RevokeTeamInvite":  {Core},
	"ResendTeamInvite":  {Core},
	"RemoveTeamMember":  {Core},
	"SetTeamMemberRole": {Core},

	// Paid AI calls: the domain feature and the ai capability must both be on.
	"AutoTranslateEmailCampaign":  {EmailCampaigns, AI},
	"DraftFittingEdits":           {Fittings, AI},
	"FormatLibraryNoteMarkdown":   {Files, AI},
	"EnhanceText":                 {TechCards, AI},
	"SuggestPrompts":              {TechCards, AI},
	"SuggestCallouts":             {TechCards, AI},
	"AnalyzeTechCardConstruction": {TechCards, AI},
	"SuggestPatternPieces":        {TechCards, AI},
	"ClassifyPatternPages":        {TechCards, AI},
	"SuggestAssemblySkeleton":     {TechCards, AI},
	"SuggestTechCardSeamHints":    {TechCards, AI},
	"StartDesignRun":              {TechCards, AI},
	"DraftDesignIdea":             {TechCards, AI},
	"GenerateDesignQuiz":          {TechCards, AI},
	"SuggestDesignParts":          {TechCards, AI},
	"SuggestDesignPartsCard":      {TechCards, AI},
	"GenerateDesignJoins":         {TechCards, AI},
}

// ForMethod returns the features required by an AdminService full method.
func ForMethod(fullMethod string) ([]Key, bool) {
	if name, ok := strings.CutPrefix(fullMethod, rbac.MethodPrefix); ok {
		if keys, overridden := methodOverrides[name]; overridden {
			return append([]Key(nil), keys...), true
		}
	}

	requirement, allowlisted, known := rbac.Lookup(fullMethod)
	if allowlisted {
		return []Key{Core}, true
	}
	if !known {
		return nil, false
	}
	key, ok := sectionFeatures[requirement.Section]
	if !ok {
		return nil, false
	}
	return []Key{key}, true
}

type route struct {
	method  string
	pattern string
}

var routeFeatures = map[route][]Key{
	{method: "POST", pattern: "/api/files/upload"}:            {Files},
	{method: "POST", pattern: "/api/files/{id}/preview"}:      {Files},
	{method: "POST", pattern: "/api/techcard-archive/upload"}: {TechCards},
	// The file proxy serves media, patterns, crops and 3D models of the caller's own team: every
	// screen that shows a file needs it, so it is Core like the token reads below.
	{method: "GET", pattern: "/api/files/proxy"}: {Core},

	{method: "GET", pattern: "/api/p/{token}"}:   {Core},
	{method: "HEAD", pattern: "/api/p/{token}"}:  {Core},
	{method: "GET", pattern: "/api/pv/{token}"}:  {Core},
	{method: "HEAD", pattern: "/api/pv/{token}"}: {Core},
	{method: "GET", pattern: "/api/rp/{token}"}:  {Core},
	{method: "HEAD", pattern: "/api/rp/{token}"}: {Core},
	{method: "GET", pattern: "/api/f/{token}"}:   {Core},
	{method: "HEAD", pattern: "/api/f/{token}"}:  {Core},
}

// ForRoute returns the features required by a non-gRPC admin-side route.
func ForRoute(method, pattern string) ([]Key, bool) {
	keys, ok := routeFeatures[route{method: method, pattern: pattern}]
	if !ok {
		return nil, false
	}
	return append([]Key(nil), keys...), true
}
