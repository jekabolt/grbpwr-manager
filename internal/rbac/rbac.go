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
	SectionProducts = "products"
	SectionPromo    = "promo"
	SectionOrders   = "orders"
	// SectionFulfillment governs the orders-fulfillment board: assign/notes/
	// checklist annotations and the ship/deliver transitions. It is separate from
	// SectionOrders so a warehouse role can fulfill orders without the broader
	// orders:write (which also grants refunds and cancellations).
	SectionFulfillment = "fulfillment"
	SectionAnalytics   = "analytics"
	SectionContent     = "content"
	SectionHero        = "hero"
	SectionArchive     = "archive"
	SectionModels      = "models"
	SectionFittings    = "fittings"
	// SectionDictionaries governs the controlled merch dictionaries (R9): collection, colour, tag
	// and the closed ISO country list — their dedicated management screens (List/Create/Update/
	// Archive/SetActive). Curating a dictionary is a SEPARATE right from editing the catalog that
	// consumes it (Q5: "создание Collection — отдельное право словарей"), so a catalog editor can no
	// longer silently pollute the collection/colour vocabulary. Catalog pickers are unaffected: the
	// flat read used by the product/tech-card UI is the allowlisted GetDictionary, not these RPCs.
	SectionDictionaries = "dictionaries"
	SectionTechCards    = "tech_cards"
	SectionProduction   = "production"
	// SectionInventory governs the material warehouse (new-flow NF-01): on-hand stock, receipts,
	// issues, adjustments and the movement ledger. Quantities are gated by this section; the money
	// on those responses (unit costs, valuation) is additionally gated by SectionCosting — a
	// warehouse role can hold inventory:read for balances without seeing their value.
	SectionInventory = "inventory"
	// SectionCosting is a FIELD-SHAPING section, not a method gate: no RPC is mapped
	// to it in methodRequirements. Instead the admin service strips confidential cost
	// fields (tech-card costing block + BOM purchase prices, product cost_price, margin/
	// COGS on metrics, release unit cost) from responses when the account lacks
	// costing:read, and rejects writes that set cost data without costing:write. A
	// content manager can hold tech_cards:read for sketches/sizes without seeing money.
	// This is the first "a permission redacts fields, not methods" precedent — future
	// financial fields (materials, production runs, dev costs) should classify here too.
	SectionCosting    = "costing"
	SectionTasks      = "tasks"
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
	{SectionFulfillment, "Fulfillment", "Orders-fulfillment board: assignee, packing checklist, ship and deliver."},
	{SectionAnalytics, "Analytics", "Business metrics, inventory targets, channel spend."},
	{SectionContent, "Content / media", "Media library: images, videos, patterns."},
	{SectionHero, "Hero", "Homepage hero and background."},
	{SectionArchive, "Archive", "Archive entries."},
	{SectionModels, "Models", "Fit models."},
	{SectionFittings, "Fittings", "Fitting sessions."},
	{SectionDictionaries, "Dictionaries", "Controlled merch dictionaries: collections, colours, tags, countries. Managing them is separate from editing the catalog that uses them."},
	{SectionTechCards, "Tech cards", "Tech cards / tech packs."},
	{SectionProduction, "Production", "Production runs (партии): plan, receive, plan/fact costs."},
	{SectionInventory, "Inventory", "Material warehouse: on-hand stock, receipts, issues, adjustments and valuation."},
	{SectionCosting, "Costing", "Confidential cost of goods: tech-card costing & BOM prices, product cost, margin/COGS analytics. Redacts fields, does not hide screens."},
	{SectionTasks, "Tasks", "Internal team kanban board."},
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
	// catalog colorways / variants
	"CreateColorway":           wr(SectionProducts), // R2/R4 write decomposition (was UpsertColorway)
	"UpdateColorway":           wr(SectionProducts), // R2/R4 write decomposition (was UpsertColorway)
	"UpdateColorwayRecipe":     wr(SectionProducts), // colourway-owned material recipe (S2/S3 write-path)
	"UpdateStyle":              wr(SectionProducts), // R4: sole writer of catalogue-style facts
	"GetColorwaysPaged":        rd(SectionProducts),
	"GetColorwayByID":          rd(SectionProducts),
	"ArchiveColorwayByID":      wr(SectionProducts), // was DeleteColorwayByID (archive-not-delete, R6/R9)
	"PublishColorway":          wr(SectionProducts), // R6 lifecycle transition
	"TransitionColorwayStatus": wr(SectionProducts), // R6 lifecycle transition (hide/unhide/archive)
	"UpdateVariantStock":       wr(SectionProducts),
	"CreateVariant":            wr(SectionProducts), // R2 variant CRUD
	"UpdateVariant":            wr(SectionProducts), // R2 variant CRUD (status patch)
	"ArchiveVariant":           wr(SectionProducts), // R2 archive-not-delete
	// Style size chart (R5). Preserves the pre-R5 authorization: the chart used to be edited through
	// the catalog product save (UpsertColorway = SectionProducts), so the same catalog role keeps it.
	"GetStyleSizeChart":               rd(SectionProducts),
	"UpdateStyleSizeChart":            wr(SectionProducts),
	"GetStyleCutList":                 rd(SectionProducts), // Q6: read-only production cut-list projection (mirror consumer)
	"RelinkDraftColorway":             wr(SectionProducts), // R4: move a draft colourway to another style
	"CloneStyleForSeason":             wr(SectionProducts), // R4: deep-clone a style under a new season
	"SyncColorwayCostFromOwningStyle": wr(SectionProducts),
	"GetColorwayCustoms":              rd(SectionProducts),
	"SetColorwayCustoms":              wr(SectionProducts),
	"ListStockChangeHistory":          rd(SectionProducts),
	"ListStockChanges":                rd(SectionProducts),
	// controlled merch dictionaries (R9): colour / collection / tag + closed ISO country. Q5: curating
	// a dictionary is a right separate from editing the catalog that consumes it, so their dedicated
	// management RPCs live in SectionDictionaries (reads + writes), not products. Catalog pickers read
	// the flat dictionary via the allowlisted GetDictionary, so this does not touch catalog editing.
	"ListColors":        rd(SectionDictionaries),
	"CreateColor":       wr(SectionDictionaries),
	"UpdateColor":       wr(SectionDictionaries),
	"ArchiveColor":      wr(SectionDictionaries),
	"ListCollections":   rd(SectionDictionaries),
	"CreateCollection":  wr(SectionDictionaries),
	"UpdateCollection":  wr(SectionDictionaries),
	"ArchiveCollection": wr(SectionDictionaries),
	"ListTags":          rd(SectionDictionaries),
	"CreateTag":         wr(SectionDictionaries),
	"UpdateTag":         wr(SectionDictionaries),
	"ArchiveTag":        wr(SectionDictionaries),
	"ListCountries":     rd(SectionDictionaries),
	"SetCountryActive":  wr(SectionDictionaries),
	// promo
	"AddPromo":         wr(SectionPromo),
	"ListPromos":       rd(SectionPromo),
	"DeletePromoCode":  wr(SectionPromo),
	"DisablePromoCode": wr(SectionPromo),
	// orders
	"GetOrderByUUID":        rd(SectionOrders),
	"ListOrders":            rd(SectionOrders),
	"SetTrackingNumber":     wr(SectionOrders),
	"SetShipmentActualCost": wr(SectionOrders),
	"RefundOrder":           wr(SectionOrders),
	"DeliveredOrder":        wr(SectionOrders),
	"CancelOrder":           wr(SectionOrders),
	"AddOrderComment":       wr(SectionOrders),
	"CreateCustomOrder":     wr(SectionOrders),
	// analytics
	"GetMetrics":             rd(SectionAnalytics),
	"GetDashboard":           rd(SectionAnalytics),
	"GetStyleEconomics":      rd(SectionAnalytics),
	"GetChannelRoasSettled":  rd(SectionAnalytics),
	"UpsertInventoryTargets": wr(SectionAnalytics),
	"UpsertChannelSpend":     wr(SectionAnalytics),
	"UpsertOpexEntries":      wr(SectionAnalytics),
	// OPEX v2 detailed line/recurring APIs (NF-08). Classified under analytics like the legacy
	// aggregate; the handlers additionally gate on costing:* (writes → PermissionDenied, reads →
	// empty) because the figures are confidential cost data. SectionCosting itself is field-shaping
	// only and is never a method requirement, so it can't appear here.
	"UpsertOpexLines":      wr(SectionAnalytics),
	"DeleteOpexLine":       wr(SectionAnalytics),
	"ListOpexLines":        rd(SectionAnalytics),
	"UpsertOpexRecurring":  wr(SectionAnalytics),
	"ArchiveOpexRecurring": wr(SectionAnalytics),
	"ListOpexRecurring":    rd(SectionAnalytics),
	// Employee registry (gap-07 v2 A) — salary journal's people. Same analytics + costing:* gating
	// as recurring OPEX (the registry carries a default_monthly_cost, confidential cost data).
	"UpsertEmployee":      wr(SectionAnalytics),
	"ArchiveEmployee":     wr(SectionAnalytics),
	"ListEmployees":       rd(SectionAnalytics),
	"GetAlertSettings":    rd(SectionAnalytics),
	"UpsertAlertSettings": wr(SectionAnalytics),
	"GetVatRates":         rd(SectionAnalytics),
	"UpsertVatRates":      wr(SectionAnalytics),
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
	"GetArchivesPaged":  rd(SectionArchive),
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
	// fitting change requests (S26): structured remark items with dedicated CRUD + carry-over
	"AddFittingChangeRequest":       wr(SectionFittings),
	"UpdateFittingChangeRequest":    wr(SectionFittings),
	"DeleteFittingChangeRequest":    wr(SectionFittings),
	"ListOpenFittingChangeRequests": rd(SectionFittings),
	// samples (new-flow NF-04) — part of the fitting/try-on cycle
	"AddSample":    wr(SectionFittings),
	"UpdateSample": wr(SectionFittings),
	"DeleteSample": wr(SectionFittings),
	"GetSample":    rd(SectionFittings),
	"ListSamples":  rd(SectionFittings),
	// sample substitutions (§2.7): dev-time material deviations on a sample
	"AddSampleSubstitution":    wr(SectionFittings),
	"DeleteSampleSubstitution": wr(SectionFittings),
	"ListSampleSubstitutions":  rd(SectionFittings),
	// tech cards
	"CreateTechCard":     wr(SectionTechCards),
	"SuggestStyleNumber": rd(SectionTechCards), // Q1: propose the next style number for a season
	// Q5 role assignments + the lightweight admin picker (so a role-assigner needs tech_cards, not accounts).
	"AssignTechCardRole":           wr(SectionTechCards),
	"RemoveTechCardRoleAssignment": wr(SectionTechCards),
	"ListTechCardRoleAssignments":  rd(SectionTechCards),
	"ListAdmins":                   rd(SectionTechCards),
	"GetTechCard":                  rd(SectionTechCards),
	"UpdateTechCard":               wr(SectionTechCards),
	"DeleteTechCard":               wr(SectionTechCards),
	"ListTechCards":                rd(SectionTechCards),
	"GetStylePipeline":             rd(SectionTechCards),
	"GetCostingFxRates":            rd(SectionTechCards),
	"UpsertCostingFxRates":         wr(SectionTechCards),
	"CreateMaterial":               wr(SectionTechCards),
	"UpdateMaterial":               wr(SectionTechCards),
	"ArchiveMaterial":              wr(SectionTechCards),
	"GetMaterial":                  rd(SectionTechCards),
	"ListMaterials":                rd(SectionTechCards),
	"AddMaterialPrice":             wr(SectionTechCards),
	"ListMaterialPrices":           rd(SectionTechCards),
	"ListTechCardReleases":         rd(SectionTechCards),
	"GetTechCardRelease":           rd(SectionTechCards),
	"AddTechCardDevExpense":        wr(SectionTechCards),
	"DeleteTechCardDevExpense":     wr(SectionTechCards),
	"ListTechCardDevExpenses":      rd(SectionTechCards),
	// style assembly bill: on-garment auxiliary components (labels/tags) — a PLM/style concern (WS7, §2.8)
	"UpsertStyleAssembly": wr(SectionTechCards),
	"ListStyleAssembly":   rd(SectionTechCards),
	"GetStyleCostEstimate": rd(SectionTechCards),
	// production runs (партии)
	"CreateProductionRun":          wr(SectionProduction),
	"UpdateProductionRun":          wr(SectionProduction),
	"DeleteProductionRun":          wr(SectionProduction),
	"GetProductionRun":             rd(SectionProduction),
	"ListProductionRuns":           rd(SectionProduction),
	"ReceiveProductionRun":         wr(SectionProduction),
	"GetProductionRunMaterialPlan": rd(SectionProduction),
	// material warehouse (new-flow NF-01)
	"ReceiveMaterialStock":  wr(SectionInventory),
	"IssueMaterialStock":    wr(SectionInventory),
	"AdjustMaterialStock":   wr(SectionInventory),
	"GetMaterialStock":      rd(SectionInventory),
	"ListMaterialStock":     rd(SectionInventory),
	"ListMaterialMovements": rd(SectionInventory),
	// packaging BOM consumed on ship (gap-07 v2 B) — warehouse config
	"UpsertPackagingBom": wr(SectionInventory),
	"ListPackagingBom":   rd(SectionInventory),
	// packaging recipe per product/style + global fallback (PLM rework §2.8, Q3)
	"UpsertPackagingRecipe": wr(SectionInventory),
	"ListPackagingRecipe":   rd(SectionInventory),
	// structured lots / rolls (gap-07 v2 D)
	"ListMaterialLots": rd(SectionInventory),
	// tasks (internal team kanban)
	"AddTask":          wr(SectionTasks),
	"GetTask":          rd(SectionTasks),
	"UpdateTask":       wr(SectionTasks),
	"MoveTask":         wr(SectionTasks),
	"DeleteTask":       wr(SectionTasks),
	"AddTaskComment":   wr(SectionTasks),
	"ListTaskComments": rd(SectionTasks),
	"ListTasks":        rd(SectionTasks),
	// task archive + checklist
	"ArchiveTask":              wr(SectionTasks),
	"UnarchiveTask":            wr(SectionTasks),
	"AddTaskChecklistItem":     wr(SectionTasks),
	"SetTaskChecklistItemDone": wr(SectionTasks),
	"DeleteTaskChecklistItem":  wr(SectionTasks),
	// fulfillment board (orders projection: annotations + ship/deliver)
	"GetFulfillmentBoard":             rd(SectionFulfillment),
	"GetFulfillmentCard":              rd(SectionFulfillment),
	"SetFulfillmentAssignee":          wr(SectionFulfillment),
	"SetFulfillmentNotes":             wr(SectionFulfillment),
	"AddFulfillmentChecklistItem":     wr(SectionFulfillment),
	"SetFulfillmentChecklistItemDone": wr(SectionFulfillment),
	"DeleteFulfillmentChecklistItem":  wr(SectionFulfillment),
	"ShipFulfillmentOrder":            wr(SectionFulfillment),
	"MarkFulfillmentDelivered":        wr(SectionFulfillment),
	"PrepareShippingLabel":            rd(SectionFulfillment),
	"GenerateShippingLabel":           wr(SectionFulfillment),
	"GetShippingOptions":              rd(SectionFulfillment),
	"VoidShippingLabel":               wr(SectionFulfillment),
	"SchedulePickup":                  wr(SectionFulfillment),
	// packer/QC packing spec: order → items + assembly + packaging (read-only projection, WS7 scope 3)
	"GetOrderPackingSpec": rd(SectionFulfillment),
	// settings
	"UpdateSettings":          wr(SectionSettings),
	"UpsertPaymentMethodFees": wr(SectionSettings),
	"AddShipmentCarrier":      wr(SectionSettings),
	"UpdateShipmentCarrier":   wr(SectionSettings),
	"DeleteShipmentCarrier":   wr(SectionSettings),
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
