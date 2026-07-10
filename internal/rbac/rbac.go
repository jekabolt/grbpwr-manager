// Package rbac defines the admin-panel section catalog and the mapping from
// admin gRPC methods to the section + access level they require. It is the
// single source of truth shared by the auth interceptor (which enforces access)
// and the admin service (which lets super-admins grant per-section access).
//
// Enforcement is stateless: an account's permissions are embedded in its JWT at
// login, and the interceptor authorizes each call from those claims alone. This
// package only maps methods to requirements; it holds no per-account state.
package rbac

import (
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// MethodPrefix is the gRPC full-method prefix for the admin service. A full
// method looks like "/admin.AdminService/UpsertProduct".
const MethodPrefix = "/admin.AdminService/"

// Section keys. These strings are stored verbatim in the admin_permission table
// and embedded in JWT claims, so they must stay stable.
const (
	SectionProducts   = "products"
	SectionPromo      = "promo"
	SectionOrders     = "orders"
	SectionAnalytics  = "analytics"
	SectionContent    = "content"
	SectionHero       = "hero"
	SectionArchive    = "archive"
	SectionModels     = "models"
	SectionFittings   = "fittings"
	SectionTechCards  = "tech_cards"
	SectionSettings   = "settings"
	SectionSupport    = "support"
	SectionMembership = "membership"
	// SectionAccounts governs the account-management RPCs themselves. Only a
	// super-admin or an account with accounts:write may create/edit accounts.
	SectionAccounts = "accounts"
)

// SectionInfo describes a section for the admin UI's permission picker.
type SectionInfo struct {
	Key         string
	Title       string
	Description string
}

// catalog is the ordered list of grantable sections shown in the UI. Order is
// intentional (mirrors the admin panel navigation).
var catalog = []SectionInfo{
	{SectionProducts, "Products", "Catalog: products, stock and stock history."},
	{SectionPromo, "Promo codes", "Promotional codes."},
	{SectionOrders, "Orders", "Orders, refunds, tracking, cancellations, custom orders."},
	{SectionAnalytics, "Analytics", "Business metrics, inventory targets, channel spend."},
	{SectionContent, "Content / media", "Media library: images, videos, patterns."},
	{SectionHero, "Hero", "Homepage hero and background."},
	{SectionArchive, "Archive", "Archive entries."},
	{SectionModels, "Models", "Fit models."},
	{SectionFittings, "Fittings", "Fitting sessions."},
	{SectionTechCards, "Tech cards", "Tech cards / tech packs."},
	{SectionSettings, "Settings", "Store settings and shipment carriers."},
	{SectionSupport, "Support", "Support tickets and reviews."},
	{SectionMembership, "Membership", "Members, loyalty tiers, hacker invites."},
	{SectionAccounts, "Accounts", "Admin accounts and their permissions."},
}

// sectionSet is the set of valid section keys, derived from the catalog.
var sectionSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(catalog))
	for _, s := range catalog {
		m[s.Key] = struct{}{}
	}
	return m
}()

// Sections returns the ordered catalog of grantable sections (for the UI).
func Sections() []SectionInfo {
	out := make([]SectionInfo, len(catalog))
	copy(out, catalog)
	return out
}

// ValidSection reports whether key is a known section.
func ValidSection(key string) bool {
	_, ok := sectionSet[key]
	return ok
}

// Requirement is the section + minimum access level a method needs.
type Requirement struct {
	Section string
	Access  entity.AccessLevel
}

func rd(section string) Requirement { return Requirement{section, entity.AccessRead} }
func wr(section string) Requirement { return Requirement{section, entity.AccessWrite} }

// methodRequirements maps each mutating/reading admin method (bare method name,
// without the service prefix) to the section + access it requires. Every method
// of AdminService must appear here or in allowlist; a completeness test enforces
// that so a newly added RPC can never ship unprotected.
var methodRequirements = map[string]Requirement{
	// products
	"UpsertProduct":          wr(SectionProducts),
	"GetProductsPaged":       rd(SectionProducts),
	"GetProductByID":         rd(SectionProducts),
	"DeleteProductByID":      wr(SectionProducts),
	"UpdateProductSizeStock": wr(SectionProducts),
	"ListStockChangeHistory": rd(SectionProducts),
	"ListStockChanges":       rd(SectionProducts),
	// promo
	"AddPromo":         wr(SectionPromo),
	"ListPromos":       rd(SectionPromo),
	"DeletePromoCode":  wr(SectionPromo),
	"DisablePromoCode": wr(SectionPromo),
	// orders
	"GetOrderByUUID":    rd(SectionOrders),
	"ListOrders":        rd(SectionOrders),
	"SetTrackingNumber": wr(SectionOrders),
	"RefundOrder":       wr(SectionOrders),
	"DeliveredOrder":    wr(SectionOrders),
	"CancelOrder":       wr(SectionOrders),
	"AddOrderComment":   wr(SectionOrders),
	"CreateCustomOrder": wr(SectionOrders),
	// analytics
	"GetMetrics":             rd(SectionAnalytics),
	"GetDashboard":           rd(SectionAnalytics),
	"UpsertInventoryTargets": wr(SectionAnalytics),
	"UpsertChannelSpend":     wr(SectionAnalytics),
	"GetAlertSettings":       rd(SectionAnalytics),
	"UpsertAlertSettings":    wr(SectionAnalytics),
	// content / media
	"UploadContentImage": wr(SectionContent),
	"UploadContentVideo": wr(SectionContent),
	"UploadPattern":      wr(SectionContent),
	"DeleteFromBucket":   wr(SectionContent),
	"ListObjectsPaged":   rd(SectionContent),
	// hero
	"AddHero":                wr(SectionHero),
	"GetBackgroundHeroColor": rd(SectionHero),
	"SetBackgroundHeroColor": wr(SectionHero),
	// archive
	"AddArchive":        wr(SectionArchive),
	"UpdateArchive":     wr(SectionArchive),
	"DeleteArchiveById": wr(SectionArchive),
	"GetArchiveByID":    rd(SectionArchive),
	// models
	"AddModel":    wr(SectionModels),
	"GetModel":    rd(SectionModels),
	"UpdateModel": wr(SectionModels),
	"DeleteModel": wr(SectionModels),
	"ListModels":  rd(SectionModels),
	// fittings
	"AddFitting":    wr(SectionFittings),
	"GetFitting":    rd(SectionFittings),
	"UpdateFitting": wr(SectionFittings),
	"DeleteFitting": wr(SectionFittings),
	"ListFittings":  rd(SectionFittings),
	// tech cards
	"CreateTechCard": wr(SectionTechCards),
	"GetTechCard":    rd(SectionTechCards),
	"UpdateTechCard": wr(SectionTechCards),
	"DeleteTechCard": wr(SectionTechCards),
	"ListTechCards":  rd(SectionTechCards),
	// settings
	"UpdateSettings":        wr(SectionSettings),
	"AddShipmentCarrier":    wr(SectionSettings),
	"UpdateShipmentCarrier": wr(SectionSettings),
	"DeleteShipmentCarrier": wr(SectionSettings),
	// support
	"GetSupportTicketById":         rd(SectionSupport),
	"GetSupportTicketByCaseNumber": rd(SectionSupport),
	"GetSupportTicketsPaged":       rd(SectionSupport),
	"UpdateSupportTicketStatus":    wr(SectionSupport),
	"UpdateSupportTicket":          wr(SectionSupport),
	"GetOrderReviewsPaged":         rd(SectionSupport),
	"DeleteOrderReview":            wr(SectionSupport),
	"GetProductReviewsPaged":       rd(SectionSupport),
	// membership
	"ListMembers":          rd(SectionMembership),
	"GetMember":            rd(SectionMembership),
	"OverrideTier":         wr(SectionMembership),
	"SetMemberStatus":      wr(SectionMembership),
	"SoftDeleteMember":     wr(SectionMembership),
	"HardEraseMember":      wr(SectionMembership),
	"GetTierHistory":       rd(SectionMembership),
	"SendMemberEmail":      wr(SectionMembership),
	"GetTierConfig":        rd(SectionMembership),
	"UpdateTierConfig":     wr(SectionMembership),
	"GenerateHackerInvite": wr(SectionMembership),
	"ListHackerInvites":    rd(SectionMembership),
	"RevokeHackerInvite":   wr(SectionMembership),
	"ListHackerAccounts":   rd(SectionMembership),
	"RevokeHackerStatus":   wr(SectionMembership),
	"GetTierAuditLog":      rd(SectionMembership),
	"RunTierBackfill":      wr(SectionMembership),
	// accounts (management RPCs)
	"ListAccounts":             rd(SectionAccounts),
	"CreateAccount":            wr(SectionAccounts),
	"UpdateAccountPermissions": wr(SectionAccounts),
	"SetAccountDisabled":       wr(SectionAccounts),
	"DeleteAccount":            wr(SectionAccounts),
	"ResetAccountPassword":     wr(SectionAccounts),
}

// allowlist is the set of admin methods any authenticated account may call
// regardless of its section grants. These are panel-wide reads that every screen
// needs (the dictionary) or an account's view of its own identity/permissions.
var allowlist = map[string]struct{}{
	"GetDictionary":       {},
	"GetCurrentAccount":   {},
	"ListAccountSections": {},
}

// EncodePermissions formats a permission set as the "section:access" strings
// embedded in a JWT's perms claim (e.g. "orders:write"). Unknown-section or
// invalid-access entries are skipped so a malformed grant can't widen access.
func EncodePermissions(perms []entity.AdminPermission) []string {
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		if !ValidSection(p.Section) || !p.Access.Valid() {
			continue
		}
		out = append(out, p.Section+":"+string(p.Access))
	}
	return out
}

// ParsePermissions decodes "section:access" claim strings into a section→access
// map. Malformed, unknown-section, or invalid-access entries are dropped (fail
// closed: a grant we can't understand confers nothing).
func ParsePermissions(perms []string) map[string]entity.AccessLevel {
	m := make(map[string]entity.AccessLevel, len(perms))
	for _, p := range perms {
		section, access, ok := strings.Cut(p, ":")
		if !ok || !ValidSection(section) {
			continue
		}
		lvl := entity.AccessLevel(access)
		if !lvl.Valid() {
			continue
		}
		// If a section appears twice, keep the stronger grant.
		if existing, ok := m[section]; ok && existing.Covers(lvl) {
			continue
		}
		m[section] = lvl
	}
	return m
}

// Authorize reports whether an account with the given super flag and parsed
// permission map may call fullMethod. legacy tokens (pre-RBAC) and super accounts
// are allowed everything; allowlisted methods are allowed for anyone
// authenticated; unmapped methods are denied (fail closed).
func Authorize(fullMethod string, legacy, super bool, perms map[string]entity.AccessLevel) bool {
	req, allowlisted, known := Lookup(fullMethod)
	if allowlisted || legacy || super {
		return true
	}
	if !known {
		return false
	}
	have, ok := perms[req.Section]
	return ok && have.Covers(req.Access)
}

// Lookup resolves a gRPC full method to its access requirement.
//
//   - allowlisted=true  → any authenticated account may call it.
//   - known=true        → req holds the required section + access.
//   - known=false and allowlisted=false → the method is not mapped; callers must
//     fail closed (deny) rather than allow an unmapped admin method.
func Lookup(fullMethod string) (req Requirement, allowlisted, known bool) {
	if len(fullMethod) <= len(MethodPrefix) || fullMethod[:len(MethodPrefix)] != MethodPrefix {
		return Requirement{}, false, false
	}
	name := fullMethod[len(MethodPrefix):]
	if _, ok := allowlist[name]; ok {
		return Requirement{}, true, false
	}
	req, ok := methodRequirements[name]
	return req, false, ok
}
