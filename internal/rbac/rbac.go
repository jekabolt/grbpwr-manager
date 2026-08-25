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
	SectionCampaigns   = "email_campaigns"
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
	SectionCosting = "costing"
	SectionTasks   = "tasks"
	// SectionFiles governs the files library: shared internal documents (mockups,
	// design guidelines, icons, 3D parts, spreadsheets), their topic labels, and
	// attaching them to tasks.
	//
	// Note there is exactly ONE section for the whole library, so everything in it
	// is visible to everyone holding files:read. That is why the seed topics in
	// migration 0312 deliberately omit `legal` and `finance` — a library that
	// cannot keep a secret should not advertise places to put one.
	//
	// The multipart upload endpoint (POST /api/files/upload) is NOT an RPC and so
	// is absent from methodRequirements; it enforces files:write inside the
	// handler instead. See internal/apisrv/admin/files_upload.go.
	SectionFiles      = "files"
	SectionSettings   = "settings"
	SectionSupport    = "support"
	SectionMembership = "membership"
	// SectionAccounts governs the account-management RPCs themselves. Only a
	// super-admin or an account with accounts:write may create/edit accounts.
	SectionAccounts = "accounts"
	// SectionAccounting governs the double-entry ledger: chart of accounts, journal
	// (incl. manual entries), period close and accounting reports. Reports expose the
	// same confidential figures as SectionCosting, so grant together in practice.
	SectionAccounting = "accounting"
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
	{SectionCampaigns, "Email campaigns", "Email campaign and audience-segment definitions."},
	{SectionModels, "Models", "Fit models."},
	{SectionFittings, "Fittings", "Fitting sessions."},
	{SectionDictionaries, "Dictionaries", "Controlled merch dictionaries: collections, colours, tags, countries. Managing them is separate from editing the catalog that uses them."},
	{SectionTechCards, "Tech cards", "Tech cards / tech packs."},
	{SectionProduction, "Production", "Production runs: plan, receive, plan/fact costs."},
	{SectionInventory, "Inventory", "Material warehouse: on-hand stock, receipts, issues, adjustments and valuation."},
	{SectionCosting, "Costing", "Confidential cost of goods: tech-card costing & BOM prices, product cost, margin/COGS analytics. Redacts fields, does not hide screens."},
	{SectionTasks, "Tasks", "Internal team kanban board."},
	{SectionFiles, "Files", "Files library: shared documents, mockups and guidelines; topics and task attachments."},
	{SectionSettings, "Settings", "Store settings and shipment carriers."},
	{SectionSupport, "Support", "Support tickets and reviews."},
	{SectionMembership, "Membership", "Members, loyalty tiers, hacker invites."},
	{SectionAccounts, "Accounts", "Admin accounts and their permissions."},
	{SectionAccounting, "Accounting", "Double-entry ledger: chart of accounts, journal, period close, financial reports."},
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
	"CreateColorway":       wr(SectionProducts), // R2/R4 write decomposition (was UpsertColorway)
	"UpdateColorway":       wr(SectionProducts), // R2/R4 write decomposition (was UpsertColorway)
	"UpdateColorwayRecipe": wr(SectionProducts), // colourway-owned material recipe (S2/S3 write-path)
	"UpdateStyle":          wr(SectionProducts), // R4: sole writer of catalogue-style facts
	"GetColorwaysPaged":    rd(SectionProducts),
	"GetColorwayByID":      rd(SectionProducts),
	"ArchiveColorwayByID":  wr(SectionProducts), // was DeleteColorwayByID (archive-not-delete, R6/R9)
	// Физическое удаление колорвея живёт в ТОМ ЖЕ скоупе, что и архивирование, и это выбор: обе
	// операции снимают колорвей с карточки, различаются только обратимостью, и разводить их по
	// правам значило бы завести роль, которая может архивировать, но не может стереть опечатку —
	// то есть ровно ту, для которой фича и написана. Необратимость охраняет не право и не сухой
	// прогон (dry_run никем не навязан — клиент вправе позвать сразу dry_run = false), а сам
	// предикат: удалить можно только то, что никогда не продавалось, не стояло в партии и в
	// настиле и не имеет остатка, и он пере-проверяется внутри транзакции удаления.
	"DeleteColorwayByID":       wr(SectionProducts),
	"PublishColorway":          wr(SectionProducts), // R6 lifecycle transition
	"TransitionColorwayStatus": wr(SectionProducts), // R6 lifecycle transition (hide/unhide/archive)
	"UpdateVariantStock":       wr(SectionProducts),
	"CreateVariant":            wr(SectionProducts), // R2 variant CRUD
	"UpdateVariant":            wr(SectionProducts), // R2 variant CRUD (status patch)
	"ArchiveVariant":           wr(SectionProducts), // R2 archive-not-delete
	"ListVariantSeconds":       rd(SectionProducts), // 0251 B-grade seconds read surface
	"SetVariantPrice":          wr(SectionProducts), // 0251 manual B-grade price (catalogue price, not costing)
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
	// the waitlist is a demand signal on products (Phase 9) — same read section as the catalog.
	"ListProductWaitlist": rd(SectionProducts),
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
	"CreateFiber":       wr(SectionDictionaries),
	"ArchiveFiber":      wr(SectionDictionaries),
	"ListCountries":     rd(SectionDictionaries),
	"SetCountryActive":  wr(SectionDictionaries),
	// promo
	"AddPromo":         wr(SectionPromo),
	"ListPromos":       rd(SectionPromo),
	"DeletePromoCode":  wr(SectionPromo),
	"DisablePromoCode": wr(SectionPromo),
	"UpdatePromoCode":  wr(SectionPromo),
	// orders
	"GetOrderByUUID":        rd(SectionOrders),
	"ListOrders":            rd(SectionOrders),
	"GetOrdersOverview":     rd(SectionOrders),
	"ListOrderComments":     rd(SectionOrders),
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
	// VAT rates feed the tax engine (declarations/JPK), not business metrics — governed by
	// accounting for segregation of duties (D-5), so a metrics-only operator can't move tax numbers.
	"GetVatRates":    rd(SectionAccounting),
	"UpsertVatRates": wr(SectionAccounting),
	// content / media
	"UploadContentImage": wr(SectionContent),
	"UploadContentVideo": wr(SectionContent),
	"UploadPattern":      wr(SectionContent),
	"DeleteFromBucket":   wr(SectionContent),
	"ListObjectsPaged":   rd(SectionContent),
	"GetMediaUsage":      rd(SectionContent),
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
	// email campaigns and saved audience predicates
	"UpsertEmailSegment": wr(SectionCampaigns),
	"GetEmailSegment":    rd(SectionCampaigns),
	"ListEmailSegments":  rd(SectionCampaigns),
	"DeleteEmailSegment": wr(SectionCampaigns),
	// wr, not rd: PreviewEmailSegment persists the cached audience count for a saved segment.
	"PreviewEmailSegment":        wr(SectionCampaigns),
	"UpsertEmailCampaign":        wr(SectionCampaigns),
	"GetEmailCampaign":           rd(SectionCampaigns),
	"ListEmailCampaignsPaged":    rd(SectionCampaigns),
	"DeleteEmailCampaign":        wr(SectionCampaigns),
	"RenderCampaignPreview":      rd(SectionCampaigns),
	"SendTestEmail":              wr(SectionCampaigns),
	"SendCampaignNow":            wr(SectionCampaigns),
	"AutoTranslateEmailCampaign": wr(SectionCampaigns),
	"ScheduleCampaign":           wr(SectionCampaigns),
	"PauseCampaign":              wr(SectionCampaigns),
	"ResumeCampaign":             wr(SectionCampaigns),
	"CancelCampaign":             wr(SectionCampaigns),
	"GetCampaignDispatchStatus":  rd(SectionCampaigns),
	"GetCampaignMetrics":         rd(SectionCampaigns),
	"GetCampaignRecipients":      rd(SectionCampaigns),
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
	"CreateTechCard":             wr(SectionTechCards),
	"GenerateTechCardOperations": wr(SectionTechCards), // AI-assisted authoring (POST); tech-card write
	"SuggestStyleNumber":         rd(SectionTechCards), // Q1: propose the next style number for a season
	// Q5 role assignments. The lightweight admin picker they were assigned from (ListAdmins) used to
	// live here on the argument "a role-assigner needs tech_cards, not accounts"; it has since grown
	// three more callers in sections that do not contain tech cards, so it moved to the allowlist —
	// see there for the full reasoning.
	"AssignTechCardRole":           wr(SectionTechCards),
	"RemoveTechCardRoleAssignment": wr(SectionTechCards),
	"ListTechCardRoleAssignments":  rd(SectionTechCards),
	"GetTechCard":                  rd(SectionTechCards),
	"UpdateTechCard":               wr(SectionTechCards),
	"DeleteTechCard":               wr(SectionTechCards),
	"ListTechCards":                rd(SectionTechCards),
	"GetStylePipeline":             rd(SectionTechCards),
	"GetTechCardReadiness":         rd(SectionTechCards),
	// CONSTRUCTION-аудит (машинный слой): rd, не wr, и это НЕ описка рядом с
	// GenerateTechCardOperations, который стоит на записи. Тот тратит деньги ключа и пишет черновик
	// операций — авторинг. Этот только пересчитывает уже сохранённую карточку и ничего не выводит,
	// чего внимательный читатель той же карточки не вывел бы сам. Повесить его на запись значило бы
	// отнять аудит у роли, которая карточки читает, но не правит, — то есть ровно у того, кому
	// «что здесь не так» и нужно. Его LLM-брат (волна 2) классифицируется wr отдельно.
	"GetTechCardConstructionAudit": rd(SectionTechCards),
	// Его LLM-брат и узкий файлинг находки — ЗАПИСЬ, по прецеденту GenerateTechCardOperations.
	// AnalyzeTechCardConstruction ничего не сохраняет, и «не пишет — значит чтение» здесь неверно:
	// нажатие ТРАТИТ ДЕНЬГИ ключа, а грант на трату — это грант авторинга, а не чтения. Раздать его
	// всем, кто карточки читает, значило бы раздать кнопку с ценой.
	"AnalyzeTechCardConstruction": wr(SectionTechCards),
	// AddTechCardIssue пишет строку — тут спорить не о чем. Он работает и на ЗАМОРОЖЕННОЙ карточке
	// (issues вне CONSTRUCTION-дайджеста), но замороженность карточки — не права: право одно и то
	// же на всех состояниях, иначе роль зависела бы от стадии.
	"AddTechCardIssue": wr(SectionTechCards),
	// Каталог работ (виды операций, 0329/0330). Чтение — обычное чтение тех-карт: каталог не несёт
	// ни денег, ни персональных данных, а без него пикер работ пуст. Запись дефолта — ГЛОБАЛЬНАЯ
	// (пара «работа + поле» на все карточки), поэтому tech-card write, а не read: жест меняет то,
	// чем заполнится форма у всех остальных.
	"GetOperationWorkCatalog":      rd(SectionTechCards),
	"RememberOperationWorkDefault": wr(SectionTechCards),
	"GetCostingFxRates":            rd(SectionTechCards),
	"UpsertCostingFxRates":         wr(SectionTechCards),
	"CreateMaterial":               wr(SectionTechCards),
	"UpdateMaterial":               wr(SectionTechCards),
	"ArchiveMaterial":              wr(SectionTechCards),
	"GetMaterial":                  rd(SectionTechCards),
	"ListMaterials":                rd(SectionTechCards),
	"AddMaterialPrice":             wr(SectionTechCards),
	"ListMaterialPrices":           rd(SectionTechCards),
	// Phase 3: reprice rewrites BOM money → tech-card write PLUS costing:write (checked in-handler).
	"RepriceTechCardBom": wr(SectionTechCards),
	// Phase 2 migration exception report: read surface; amounts additionally need costing:read
	// (stripped in-handler otherwise, same rule as every cost figure).
	"ListCostingMigrationExceptions": rd(SectionTechCards),
	"ListTechCardReleases":           rd(SectionTechCards),
	"GetTechCardRelease":             rd(SectionTechCards),
	"AddTechCardDevExpense":          wr(SectionTechCards),
	"DeleteTechCardDevExpense":       wr(SectionTechCards),
	"ListTechCardDevExpenses":        rd(SectionTechCards),
	// style assembly bill: on-garment auxiliary components (labels/tags) — a PLM/style concern (WS7, §2.8)
	"UpsertStyleAssembly":  wr(SectionTechCards),
	"ListStyleAssembly":    rd(SectionTechCards),
	"GetStyleCostEstimate": rd(SectionTechCards),
	// colour variants of an auxiliary card's warehouse output (0252): tech-card content, edited by
	// whoever edits the card. Reads travel on GetTechCard/ListTechCards, so there is no read RPC.
	"UpsertTechCardOutputVariant": wr(SectionTechCards),
	"DeleteTechCardOutputVariant": wr(SectionTechCards),
	"SaveTechCardMarker":          wr(SectionTechCards),
	"GetTechCardMarker":           rd(SectionTechCards),
	"DeleteTechCardMarker":        wr(SectionTechCards),
	// Designating the НОРМИРОВОЧНАЯ раскладка (Ф3.4) — a decision about what the CARD says, under the
	// same right as the rest of the card's content. Deliberately NOT production:write, unlike the
	// workshop settings beside it: a припуск по умолчанию is a fact about the ЦЕХ and belongs to
	// whoever runs the floor, while a norm belongs to one card and is chosen by whoever keeps it. Not
	// a section of its own either — it writes a column of tech_card_marker, which this same right
	// already writes through SaveTechCardMarker.
	"SetTechCardMarkerNorm": wr(SectionTechCards),
	// ИНДЕКС РАЗМЕРОВ ВЫКРОЕК (Ф6.3) — tech-cards WRITE, and the asymmetry with
	// CheckProductionRunReadiness above is the point. This WRITES a derived fact about the card's
	// patterns, from the patterns tab, in the same session as the upload it describes. A production
	// planner must not be able to write it: the index feeds the readiness gate, so the right to
	// rewrite it is the right to clear one's own blocker.
	"PutTechCardPatternSizeIndex": wr(SectionTechCards),
	// Площади деталей кроя (Ф0): тот же уровень, что индекс размеров — производный факт о
	// выкройках карточки, пишется со вкладки выкроек, и им кормится костинг и гейт релиза.
	// Плановик производства не должен уметь его переписать: право переписать площади — это право
	// снять себе блокер.
	"SaveTechCardPieceAreas": wr(SectionTechCards),
	// НАПРАВЛЕНИЕ ТКАНИ gap report (Ф1.8) — tech-cards READ, and specifically not production nor a
	// section of its own. Every field it returns is BOM-tab content the same account already reads
	// card by card through GetTechCard (line name, section, назначение, семпловая, approval state);
	// the report only reads it ACROSS cards. It carries no money, so the field-shaping SectionCosting
	// does not apply. The work it produces — filling направление in on the BOM tab — needs
	// tech_cards:write anyway, so a reader who can see the list and not act on it is the intended
	// asymmetry rather than a gap. Never allowlisted: allowlisted means ANY authenticated account may
	// call it, and this one enumerates the whole style portfolio by article and name.
	"ListTechCardFabricDirectionGaps": rd(SectionTechCards),
	// production runs (партии)
	"CreateProductionRun":  wr(SectionProduction),
	"UpdateProductionRun":  wr(SectionProduction),
	"DeleteProductionRun":  wr(SectionProduction),
	"GetProductionRun":     rd(SectionProduction),
	"ListProductionRuns":   rd(SectionProduction),
	"ReceiveProductionRun": wr(SectionProduction),
	// the atomic receipt command additionally requires products:write, enforced in-handler
	// (it moves sellable stock); the interceptor gate stays the production section.
	"PostProductionRunReceipt": wr(SectionProduction),
	// the reversal additionally requires products:write AND costing:write, enforced in-handler
	// (it moves sellable stock and rolls back cost_price).
	"ReverseProductionRunReceipt":  wr(SectionProduction),
	"GetProductionRunMaterialPlan": rd(SectionProduction),
	// КАТ-ЛИСТ ПАРТИИ — та же секция и то же READ, что у материального плана строкой выше: обе
	// проекции читают одни и те же факты карточки (BOM, колорвеи, рецепты) поверх одного и того же
	// прогона, и классифицировать сестёр по-разному значило бы завести аккаунт, который видит,
	// сколько ткани заказать, но не видит, что из неё выкроить.
	//
	// НЕ SectionCosting, и это проверяется содержимым, а не привычкой: в ответе нет ни одного
	// денежного поля, и по построению не может быть — он уходит на бумагу в цех и в публичный
	// манифест наряда, где стрипать костинг некому.
	"GetProductionRunCutPlan": rd(SectionProduction),
	// НАСТИЛЫ ПРОГОНА (Ф4). Секция production, и это не выбор по умолчанию: настил — план ЦЕХА
	// (сколько слоёв, каким маркером, с какой ткани), а не содержимое карточки. Тот же довод, что у
	// UpdateWorkshopSettings ниже — «менять это может тот, кто ведёт цех, а не всякий, кто правит
	// карточку». Чтение НЕ в allowlist: в ответе едут количества, артикулы и покрытие, то есть
	// содержимое производственного плана, а не справочник.
	//
	// SaveTechCardMarker выше остаётся wr(SectionTechCards): раскройный маркер сохраняется ТЕМ ЖЕ
	// RPC с production_run_id != 0, и дополнительное требование production:write проверяется в
	// обработчике — прецедент PostProductionRunReceipt / ReverseProductionRunReceipt, где карта
	// держит одну секцию, а вторая проверяется кодом. Второй RPC ради секции дал бы две реализации
	// одной валидации, которые разойдутся молча.
	"SaveProductionRunLay":   wr(SectionProduction),
	"DeleteProductionRunLay": wr(SectionProduction),
	"ListProductionRunLays":  rd(SectionProduction),
	// ПРИЁМКА КРОЯ (Ф5б.5) — та же секция и по тому же доводу: выкроенное и принятое в пошив считает
	// тот, кто ведёт цех. Чтение НЕ в allowlist — в ответе едут количества прогона.
	//
	// Заметим, что секция здесь НЕ следует из статуса прогона: приёмку кроя принимают и на закрытом
	// прогоне (это отчёт о прошлом, а не план), поэтому единственный, кто её ограничивает, — вот эта
	// карта.
	"SaveProductionRunCutReceipt":   wr(SectionProduction),
	"DeleteProductionRunCutReceipt": wr(SectionProduction),
	"ListProductionRunCutReceipts":  rd(SectionProduction),
	// КАЛИБРОВКА КОЭФФИЦИЕНТА РАСКРОЯ (Ф5б.3) — production READ, ХОТЯ ЖИВЁТ НА ЭКРАНЕ АРТИКУЛА.
	//
	// Секция следует за тем, ЧТО В ОТВЕТЕ, а не за тем, где кнопка. А в ответе — разбор по настилам:
	// id прогонов, id и названия карточек, замеренный расход цеха. Классифицируй мы это как
	// tech_cards (где живут остальные чтения материала), владелец каталога без доступа к производству
	// читал бы, сколько ткани ушло на каждую партию. Довод тот же, по которому ListProductionRunLays
	// выше не в allowlist. Следствие названо: клиент на экране артикула обязан пережить
	// PermissionDenied, показав отсутствие предложения, а не ошибку.
	//
	// READ, и это не формальность: сервер здесь НИЧЕГО НЕ ПИШЕТ — коэффициент принимает рука
	// владельца артикула через UpdateMaterial, у которого своя (tech_cards:write) проверка.
	"GetMaterialCuttingCoefficientSuggestion": rd(SectionProduction),
	// ПРЕДЛОЖЕНИЕ ПРОЦЕНТА РАСКРОЯ (T7 волна 2) — та же классификация и ДОСЛОВНО тот же довод, что у
	// соседа строкой выше: ответ несёт разбор по настилам (id прогонов, карточки, замеренный расход
	// цеха), поэтому production:read, а не tech_cards — и READ, потому что сервер ничего не пишет:
	// процент принимает рука через UpdateTechCard со своей (tech_cards:write) проверкой. Забыть RPC
	// в этой карте — не «открыть», а ЗАКРЫТЬ его всем, кроме суперпользователя: неизвестный метод
	// запрещается fail-closed, и на бете под суперпользователем это незаметно.
	"GetBomWastageSuggestion": rd(SectionProduction),
	// ГЕЙТ ГОТОВНОСТИ ПРОГОНА (Ф6) — production READ, and specifically NOT tech_cards.
	//
	// The argument, not the taste: this RPC has ONE consumer (the run-creation modal) and ONE
	// meaning («why can I not create this run»). Whoever is entitled to create a run —
	// CreateProductionRun is wr(production) — must be entitled to read the reason it was refused;
	// otherwise a planner without tech-card access gets a refusal and NO WAY to learn its cause,
	// which is a dead control stating an unfixable requirement. The precedent is immediate:
	// GetProductionRunMaterialPlan above reads the very same card facts (BOM, colourways, recipes)
	// and is classified the same way. The answer carries no money, so SectionCosting does not apply.
	"CheckProductionRunReadiness": rd(SectionProduction),
	// «Дом настроек цеха» (Ф2.5) — the physical shop floor's constants (cutting table length today;
	// припуск, высота стопки, минимальный зазор next). Classified under production, NOT under
	// SectionSettings: that section is the STOREFRONT's configuration ("Store settings and shipment
	// carriers"), and the precedent for domain config is GetAlertSettings/UpsertAlertSettings, which
	// sit in the section they serve rather than in settings.
	//
	// The write gate is deliberately production:write and not tech_cards:write. Changing the table
	// length moves the verdict for every раскладка in the shop, so it belongs to whoever runs the
	// floor, not to everyone who may edit a card.
	//
	// The READ is not here — it is allowlisted, see the allowlist below for why a single section
	// gate could not serve both of its readers.
	"UpdateWorkshopSettings": wr(SectionProduction),
	// material warehouse (new-flow NF-01)
	"ReceiveMaterialStock":    wr(SectionInventory),
	"IssueMaterialStock":      wr(SectionInventory),
	"AdjustMaterialStock":     wr(SectionInventory),
	"BatchIssueMaterialStock": wr(SectionInventory),
	"GetMaterialStock":        rd(SectionInventory),
	"ListMaterialStock":       rd(SectionInventory),
	"ListMaterialMovements":   rd(SectionInventory),
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

	// files library
	"GetLibraryFile":    rd(SectionFiles),
	"ListLibraryFiles":  rd(SectionFiles),
	"UpdateLibraryFile": wr(SectionFiles),
	"DeleteLibraryFile": wr(SectionFiles),
	"ListFileTopics":    rd(SectionFiles),
	"CreateFileTopic":   wr(SectionFiles),
	"RenameFileTopic":   wr(SectionFiles),
	"DeleteFileTopic":   wr(SectionFiles),
	// Merge edits the topic vocabulary, assign labels files: same section as
	// rename/delete, and both write.
	"MergeFileTopics":         wr(SectionFiles),
	"AssignLibraryFileTopics": wr(SectionFiles),
	// ГРУППИРОВКА: ТИП ТЕМЫ И СЛОВАРЬ РОЛЕЙ. Та же секция files и никакой новой: это та же
	// библиотека, разложенная иначе, а не второй раздел. Чтение словаря — files:read (роли не
	// секрет от того, кто видит сетку; счётчики при этом персональны, как у тем), правки словаря
	// и простановка роли — files:write.
	//
	// SetLibraryFileRoles — ЗАПИСЬ ПОД ПРЕДИКАТОМ: files:write здесь необходим, но проверку
	// видимости каждого файла делает стор, и один невидимый id отказывает всей пачке. Второго
	// гейта в хендлере (как у владельцев и доступа) тут нет намеренно — роль не расширяет ничей
	// доступ, она только раскладывает уже видимое.
	"UpdateFileTopicMeta": wr(SectionFiles),
	"ListFileRoles":       rd(SectionFiles),
	"UpsertFileRole":      wr(SectionFiles),
	"MergeFileRoles":      wr(SectionFiles),
	"SetLibraryFileRoles": wr(SectionFiles),
	// ПРОЕКТ ↔ СТИЛЬ (0321): «каким файлом сделана эта вещь». Та же секция files и никакой новой
	// — библиотека, посмотренная с другой стороны.
	//
	// ListStyleFileProjects ЧИТАЕТСЯ С КАРТОЧКИ ВЕЩИ, НО ТРЕБУЕТ files:read, А НЕ techcards:read,
	// и это осознанно: ответ несёт ИМЕНА проектов и ЧИСЛА файлов, то есть содержимое библиотеки.
	// Повесить его на секцию тех-карт значило бы сделать карточку вещи боковым каналом, через
	// который человек без прав на файлы читает, как называются проекты и сколько в них лежит.
	// Человек без files:read просто не получает этот блок — клиент прячет его на PermissionDenied.
	//
	// Обратное плечо симметрично и его здесь НЕТ намеренно: список стилей проекта отдаёт номера и
	// имена тех-карт обладателю files:read. Это принято — артикул и имя стиля в этой системе не
	// секрет (их печатает половина экранов), а требовать ОБЕ секции значило бы закрыть страницу
	// проекта от того самого человека, который её и наполняет.
	"LinkFileTopicStyle":    wr(SectionFiles),
	"UnlinkFileTopicStyle":  wr(SectionFiles),
	"ListFileTopicStyles":   rd(SectionFiles),
	"ListStyleFileProjects": rd(SectionFiles),
	// files:write is NECESSARY here but not SUFFICIENT: the handler additionally requires the caller
	// to be the uploader, a current owner, or a super-admin. Without that second gate any files:write
	// account could appoint itself owner of anybody's file — and once the access levels land, appoint
	// itself INTO a file it was not allowed to see. Precedent for "the map holds one section, the code
	// checks the rest": PostProductionRunReceipt / ReverseProductionRunReceipt.
	"SetLibraryFileOwners": wr(SectionFiles),
	// ОБСУЖДЕНИЕ ФАЙЛА. Чтение ленты — files:read, письмо в ленту — files:write, и асимметрия названа:
	// обсуждение классифицировано как СОДЕРЖИМОЕ библиотеки, а не как её просмотр, поэтому files:read
	// ленту читает, но не пополняет. Второй гейт — править и удалять можно ТОЛЬКО свою реплику, супер
	// любую — стоит в хендлере: карта держит одну секцию, тот же приём, что у SetLibraryFileOwners.
	"ListLibraryFileComments":  rd(SectionFiles),
	"AddLibraryFileComment":    wr(SectionFiles),
	"UpdateLibraryFileComment": wr(SectionFiles),
	"DeleteLibraryFileComment": wr(SectionFiles),
	// ДОСТУП К ФАЙЛУ: уровень (team|people|link), поимённый список, публичная ссылка и журнал.
	//
	// Чтения — files:read: уровень и список не секрет от того, кто сам файл уже видит, а невидимый файл
	// до этих RPC не доходит — предикат видимости отвечает NotFound раньше. Витрина «что у нас открыто»
	// ходит под тем же предикатом, поэтому её выдача у разных людей разная; это принято планом и
	// написано на самом экране.
	//
	// Записи — files:write И круг «загрузивший|владелец|супер» в хендлере, ровно как у владельцев: без
	// второго гейта любой files:write выкладывает чужой файл в интернет одним запросом. Публичный
	// маршрут GET|HEAD /api/f/{token} в этой карте отсутствует по построению — он не RPC и ходит без
	// JWT, как /api/p|pv|rp.
	"GetLibraryFileAccess":   rd(SectionFiles),
	"ListSharedLibraryFiles": rd(SectionFiles),
	"SetLibraryFileAccess":   wr(SectionFiles),
	"RotateLibraryFileLink":  wr(SectionFiles),
	// MARKDOWN-ЗАМЕТКИ. Заметка — обычный файл библиотеки, поэтому и права у неё файловые: чтение текста
	// files:read, создание и сохранение files:write. Своей секции нет сознательно — она дала бы аккаунт,
	// который читает заметку, но не файл, в котором она лежит.
	"CreateLibraryNote":      wr(SectionFiles),
	"GetLibraryNoteContent":  rd(SectionFiles),
	"SaveLibraryNoteContent": wr(SectionFiles),
	// AI-ФОРМАТИРОВАНИЕ ТЕКСТА ЗАМЕТКИ — files:WRITE, хотя сервер не сохраняет ни байта: прецедент
	// GenerateTechCardOperations, где AI-авторинг классифицирован как запись. Довод не формальный: метод
	// существует, чтобы породить содержимое, которое человек примет в буфер и сохранит, и читатель
	// библиотеки не должен уметь запустить авторинг. Заодно это единственный тормоз расхода на модель —
	// платный вызов не должен быть доступен всякому, у кого есть files:read.
	"FormatLibraryNoteMarkdown": wr(SectionFiles),
	// task archive + checklist
	"ArchiveTask":              wr(SectionTasks),
	"UnarchiveTask":            wr(SectionTasks),
	"AddTaskChecklistItem":     wr(SectionTasks),
	"SetTaskChecklistItemDone": wr(SectionTasks),
	"DeleteTaskChecklistItem":  wr(SectionTasks),
	// ЗАДАЧИ ФАЙЛА — секция TASKS, а не files, хотя все три RPC живут по адресу /api/admin/files/… и
	// зовутся с карточки файла. Секция следует за тем, ЧТО В ОТВЕТЕ, а не за тем, где кнопка (прецедент
	// GetMaterialCuttingCoefficientSuggestion выше в production): в ответе едут заголовки, колонки,
	// исполнители и сроки задач, а мутация пишет строку принадлежности ЗАДАЧИ — task_file каскадит от
	// задачи, не от файла. Классифицируй мы это как files, аккаунт с одной библиотекой читал бы доску
	// боком, поимённо.
	//
	// Следствие названо, чтобы его не приняли за баг: на карточке файла человек без tasks:read получает
	// PermissionDenied, и блок задач обязан пережить его надписью «нет доступа к задачам», а не
	// сломанной карточкой.
	"ListLibraryFileTasks":      rd(SectionTasks),
	"AttachLibraryFileToTask":   wr(SectionTasks),
	"DetachLibraryFileFromTask": wr(SectionTasks),
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
	// УДАЛЕНИЕ ПОЗИЦИИ СЛОВАРЯ СПЕЦИАЛЬНОСТЕЙ — ЗДЕСЬ, А НЕ В allowlist РЯДОМ С ЗАПИСЬЮ.
	// SetAccountSpecialties внизу разрешён всем аутентифицированным, потому что человек правит СВОЁ
	// самоописание, и новое имя только добавляется: ни у кого на экране ничего не пропадает.
	// Удаление — ровно наоборот. Словарь общий, он едет в каждом ответе пикера людей, и снятая
	// позиция исчезает у ВСЕХ, кто мог бы её выбрать. Это действие над чужими аккаунтами, а не над
	// своей подписью, поэтому послабление Р1 сюда не переносится: нужен accounts:write, то же право,
	// которым уже закрыта правка чужих специальностей.
	"DeleteAccountSpecialty": wr(SectionAccounts),
	// accounting (double-entry ledger, docs/plan-accounting/05-admin-api.md): chart of accounts,
	// journal (incl. manual entries + reversal), period close/reopen and the financial reports.
	"ListAcctAccounts":    rd(SectionAccounting),
	"CreateAcctAccount":   wr(SectionAccounting),
	"UpdateAcctAccount":   wr(SectionAccounting),
	"ArchiveAcctAccount":  wr(SectionAccounting),
	"CreateJournalEntry":  wr(SectionAccounting),
	"ReverseJournalEntry": wr(SectionAccounting),
	"ListJournalEntries":  rd(SectionAccounting),
	"GetJournalEntry":     rd(SectionAccounting),
	"ListAcctPeriods":     rd(SectionAccounting),
	"CloseAcctPeriod":     wr(SectionAccounting),
	"ReopenAcctPeriod":    wr(SectionAccounting),
	// posting-worker event review queue (H-1/H-2/B-5): listing is read, reprocess/resolve mutate state.
	"ListAcctEventsNeedingReview": rd(SectionAccounting),
	"ReprocessAcctEvent":          wr(SectionAccounting),
	"ResolveAcctEvent":            wr(SectionAccounting),
	"GetTrialBalance":             rd(SectionAccounting),
	"GetProfitLossStatement":      rd(SectionAccounting),
	"GetBalanceSheet":             rd(SectionAccounting),
	"GetAccountLedger":            rd(SectionAccounting),
	"GetAcctReconciliation":       rd(SectionAccounting),
	"GetVatReturnPL":              rd(SectionAccounting),
	"GetOssReturn":                rd(SectionAccounting),
	"ExportJpkV7M":                rd(SectionAccounting),
	"ExportOssReturn":             rd(SectionAccounting),
	"GetUkVatReturn":              rd(SectionAccounting),
	"GetFrs105Accounts":           rd(SectionAccounting),
	"GetCashFlowStatement":        rd(SectionAccounting),
	"GetFinancialHealth":          rd(SectionAccounting),

	// Wave 4 — money side: Revolut bank inbox (4.1) + AP/AR subledgers (4.4).
	"ImportBankCsv":  wr(SectionAccounting),
	"ListBankTxns":   rd(SectionAccounting),
	"PostBankTxn":    wr(SectionAccounting),
	"IgnoreBankTxn":  wr(SectionAccounting),
	"ListBankRules":  rd(SectionAccounting),
	"CreateBankRule": wr(SectionAccounting),
	"DeleteBankRule": wr(SectionAccounting),
	"CreateSupplier": wr(SectionAccounting),
	"ListSuppliers":  rd(SectionAccounting),
	"GetPayables":    rd(SectionAccounting),
	"GetReceivables": rd(SectionAccounting),
	"GetAcctAlerts":  rd(SectionAccounting),
	"GetVatUe":       rd(SectionAccounting),
	// Fixed-asset depreciation + corporation-tax accrual (#71).
	"CreateFixedAsset":     wr(SectionAccounting),
	"ListFixedAssets":      rd(SectionAccounting),
	"PostDepreciation":     wr(SectionAccounting),
	"AccrueCorporationTax": wr(SectionAccounting),
}

// allowlist is the set of admin methods any authenticated account may call
// regardless of its section grants. These are panel-wide reads that every screen
// needs (the dictionary) or an account's view of its own identity/permissions.
var allowlist = map[string]struct{}{
	"GetDictionary":       {},
	"GetCurrentAccount":   {},
	"ListAccountSections": {},
	// Настройки цеха are shop-wide reference data — how long the cutting table is, and later the
	// default seam allowance, the stack-height limit and the minimum gap. They are read from TWO
	// sections that do not contain one another: the tech-card nesting modal (tech_cards) and the
	// настилы editor on the production run (production). methodRequirements allows exactly one
	// section per method, so ANY single read gate silently breaks one of the two callers — and it
	// breaks it as "the length is not configured", which is indistinguishable from the truthful
	// unset state and would make the screen quietly stop offering a default.
	//
	// Allowlisting the READ is the honest resolution: this is panel-wide configuration in the same
	// sense as the dictionary, and a table's length is not confidential. The WRITE stays gated on
	// production:write above — reading the shop's настройка and changing it for everyone are
	// different rights.
	"GetWorkshopSettings": {},
	// ПИКЕР ЛЮДЕЙ. Moved here from rd(SectionTechCards), with the GetWorkshopSettings argument above
	// applied word for word: a picker of people is needed from at least three sections that do not
	// contain one another (files: owners and, later, access; tasks: the assignee; tech cards: the Q5
	// roles), methodRequirements allows exactly ONE section per method, and any single section
	// silently breaks the picker on the other screens — as an EMPTY list of people, which reads like
	// "there is nobody to pick" rather than like a refusal.
	//
	// Allowlisting is safe because of WHAT IS IN THE ANSWER, checked rather than assumed: id,
	// username, self-declared specialties and the is_super flag. That is the panel's staff directory,
	// not confidential material — and specifically not the shape that got ListTechCardFabricDirectionGaps
	// REFUSED an allowlist, which enumerates the whole style portfolio by article and name. Nothing
	// here says what anybody may DO: permissions travel on ListAccounts, which stays rd(accounts).
	// is_super is in the answer on purpose: the "no access to this section" screen names who grants it.
	"ListAdmins": {},
	// СВОЮ СПЕЦИАЛЬНОСТЬ ЧЕЛОВЕК ПРАВИТ САМ, поэтому запись здесь, а не в wr(SectionAccounts) — и это
	// не послабление, а единственное место, где проверка вообще может стоять: интерсептор отрезал бы
	// self-edit ДО хендлера, и «одна строка на свой аккаунт» не выполнилась бы никогда. Чужой
	// username хендлер требует закрыть правом accounts:write (accountsWriteAccess).
	//
	// Довод: специальность не несёт ни грамма прав — это самоописание, которым ищут человека в
	// пикере. Поле, которое нельзя заполнить без администратора аккаунтов, остаётся пустым, а пустой
	// словарь специальностей обесценивает и пикер владельцев, и поиск людей, ради которых он заведён.
	// Цена ошибки — человек напишет себе неверную специальность и его хуже найдут; данных это не
	// трогает, прав не даёт, правится чужой рукой с accounts:write.
	//
	// Послабление кончается на записи: УДАЛЕНИЕ позиции общего словаря
	// (DeleteAccountSpecialty) стоит в methodRequirements как wr(SectionAccounts) — см. довод там.
	"SetAccountSpecialties": {},
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
