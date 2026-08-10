package dto

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

// TechCardSectionDigests fingerprints each sign-off section's content, so an approval can be told
// apart from a stale approval (see TechCardSignoff.signed_digest).
//
// THE INVARIANT THAT MAKES THIS WORK: the same function runs on the WRITE payload (to stamp a
// section as it is approved) and on the READ model (to report the current fingerprint). Both are an
// entity.TechCardInsert — entity.TechCard embeds it — so the two agree as long as the projection
// below reads only fields that survive the store round-trip unchanged.
//
// That is why every value goes through the normalising helpers: a NULL string and an empty string
// must hash the same, or a section would read as "changed" purely because the store wrote NULL where
// the client sent "".
//
// DELIBERATELY NOT COVERED: TechCardInsert.Colorways. It is on the struct but it is populated only on
// READ (a colourway is a product; the style write path never carries them), so folding it in would
// make every COLOUR digest differ between write and read and mark every colour sign-off permanently
// stale. What the COLOUR section does cover is the card-owned colour decision: which BOM fabric each
// cut-piece is made from, per colourway.
//
// THE ONE PLACE THE TWO SIDES DO NOT AGREE BY THEMSELVES — LINKED BOM LINES: the BOM read query
// resolves a linked line's identity (name / supplier / supplier_ref / composition / spec / unit) from
// the catalog material it links, while the write payload legitimately carries an empty string for those
// fields (the admin client sends no name for a linked line that never had one — see
// parseTechCardBomItems and bomNaturalKey). materialsProjection hashes exactly those fields, so a
// MATERIALS approval stamped from the raw payload could never match the fingerprint the next read
// reports — a permanent, un-clearable "changed since sign-off". The write side therefore stamps through
// TechCardSectionDigestsAsRead, which resolves the link the same way the query does; the read side
// needs no help, the store enriched it already.
func TechCardSectionDigests(tc *entity.TechCardInsert) map[entity.TechCardSignoffSection]string {
	if tc == nil {
		return nil
	}
	return map[entity.TechCardSignoffSection]string{
		entity.SignoffDesign:       digestOf(designProjection(tc)),
		entity.SignoffConstruction: digestOf(constructionProjection(tc)),
		entity.SignoffMaterials:    digestOf(materialsProjection(tc)),
		entity.SignoffColour:       digestOf(colourProjection(tc)),
		entity.SignoffLabels:       digestOf(labelsProjection(tc)),
		entity.SignoffPackaging:    digestOf(packagingProjection(tc)),
		entity.SignoffCosting:      digestOf(costingProjection(tc)),
	}
}

// TechCardSectionDigestsToPb emits the current fingerprints for the wire, in a stable section order
// so the list does not reshuffle between reads.
func TechCardSectionDigestsToPb(tc *entity.TechCardInsert) []*pb_common.TechCardSectionDigest {
	digests := TechCardSectionDigests(tc)
	if len(digests) == 0 {
		return nil
	}
	order := []entity.TechCardSignoffSection{
		entity.SignoffDesign, entity.SignoffConstruction, entity.SignoffMaterials,
		entity.SignoffColour, entity.SignoffLabels, entity.SignoffPackaging, entity.SignoffCosting,
	}
	out := make([]*pb_common.TechCardSectionDigest, 0, len(order))
	for _, sec := range order {
		d, ok := digests[sec]
		if !ok {
			continue
		}
		out = append(out, &pb_common.TechCardSectionDigest{
			Section: techCardSignoffSectionEntityToPb[sec],
			Digest:  d,
		})
	}
	return out
}

// StampTechCardSignoffDigests records, for each APPROVED section, the fingerprint of the content that
// was approved — and, crucially, does NOT move it afterwards.
//
// The subtle part is when to stamp. Re-stamping on every save would be worse than useless: a save
// that edits the BOM would hand the materials sign-off a fresh digest matching the NEW content, so
// the section would silently re-bless itself and the staleness check could never fire. That is the
// exact failure this whole mechanism exists to prevent.
//
// So the stamp is driven by the caller's intent, expressed by what it sends back:
//
//	empty  → "I am approving this now" (a new sign-off, or a re-approval after review). The server
//	         fingerprints the payload it is about to write — the only moment it can be certain the
//	         digest describes precisely the content the approver was looking at.
//	present→ "I am not re-approving, I am just saving." The admin layer treats this only as a carry
//	         request and replaces the digest and audit fields from the stored approved row. Create has
//	         no stored row and coerces every approval to fresh.
//
// A section that is not approved carries no digest at all: pending and rejected have nothing to be
// stale against.
//
// This runs at parse time, with no DB in reach, so what it stamps is the payload AS SENT. The RPC layer
// re-stamps exactly the sections stamped here from the read-model form of the same payload
// (TechCardSectionDigestsAsRead) before the write — see restampFreshSignoffDigests. Keep this function
// the one that decides WHICH sections get stamped; the re-stamp only corrects the VALUE.
func StampTechCardSignoffDigests(tc *entity.TechCardInsert) {
	if tc == nil || len(tc.Signoffs) == 0 {
		return
	}
	digests := TechCardSectionDigests(tc)
	for i := range tc.Signoffs {
		if tc.Signoffs[i].State != entity.SignoffStateApproved {
			tc.Signoffs[i].SignedDigest = sql.NullString{}
			continue
		}
		if tc.Signoffs[i].SignedDigest.Valid && tc.Signoffs[i].SignedDigest.String != "" {
			continue // carry request; the admin layer verifies it against storage before persistence
		}
		tc.Signoffs[i].SignedDigest = nullStringFromPb(digests[tc.Signoffs[i].Section])
	}
}

// BomMaterialIdentity is the identity a LINKED BOM line inherits from the catalog material it links:
// exactly the fields the BOM read query resolves THROUGH the link (internal/store/techcard/materials.go,
// enrichMaterials). unit_price / currency are deliberately absent — the read path does not resolve them
// (an agreed cost stays frozen on the line), and colour is the line's own.
type BomMaterialIdentity struct {
	Name        string
	Supplier    string
	SupplierRef string
	Composition string
	Spec        string
	Unit        string
}

// TechCardSectionDigestsAsRead fingerprints a WRITE payload as the card will READ BACK: linked BOM
// lines take their identity from the catalog, the way the read query resolves it. catalog maps
// material_id → identity and only needs the materials the payload's BOM actually links; a missing id
// (or a nil map) leaves the line exactly as sent, which is also what the read query's LEFT JOIN does
// for a broken link.
//
// This is what the write side must stamp an approval with. TechCardSectionDigests on the raw payload
// answers a different question — "what did the client send" — and for a linked line that is not what
// any later read reports.
func TechCardSectionDigestsAsRead(tc *entity.TechCardInsert, catalog map[int64]BomMaterialIdentity) map[entity.TechCardSignoffSection]string {
	return TechCardSectionDigests(withResolvedBomIdentity(tc, catalog))
}

// withResolvedBomIdentity returns a copy of the insert whose linked BOM lines carry the identity the
// read query resolves for them. The copy is shallow apart from BomItems — nothing else is touched, and
// the caller's payload is never mutated: the enriched form is for hashing only, the stored columns keep
// the client's values (the read path re-resolves them on every read, which is the point of a link).
func withResolvedBomIdentity(tc *entity.TechCardInsert, catalog map[int64]BomMaterialIdentity) *entity.TechCardInsert {
	if tc == nil || len(catalog) == 0 || len(tc.BomItems) == 0 {
		return tc
	}
	out := *tc
	out.BomItems = make([]entity.TechCardBomItem, len(tc.BomItems))
	copy(out.BomItems, tc.BomItems)
	for i := range out.BomItems {
		b := &out.BomItems[i]
		if !b.MaterialId.Valid || b.MaterialId.Int64 <= 0 {
			continue // a free-text line has nothing to resolve from and keeps its own values
		}
		m, ok := catalog[b.MaterialId.Int64]
		if !ok {
			continue // link to a material we could not load: the stored value stands, as in the LEFT JOIN
		}
		b.Name = resolvedThroughLink(m.Name, b.Name)
		b.Supplier = resolvedNullThroughLink(m.Supplier, b.Supplier)
		b.SupplierRef = resolvedNullThroughLink(m.SupplierRef, b.SupplierRef)
		b.Composition = resolvedNullThroughLink(m.Composition, b.Composition)
		b.Spec = resolvedNullThroughLink(m.Spec, b.Spec)
		b.Unit = resolvedNullThroughLink(m.Unit, b.Unit)
	}
	return &out
}

// resolvedThroughLink mirrors the read query's COALESCE(NULLIF(m.x, empty), bi.x) rule: the catalog
// value wins whenever it is non-empty, otherwise the line's own value stands.
func resolvedThroughLink(catalog, stored string) string {
	if catalog != "" {
		return catalog
	}
	return stored
}

func resolvedNullThroughLink(catalog string, stored sql.NullString) sql.NullString {
	if catalog != "" {
		return sql.NullString{String: catalog, Valid: true}
	}
	return stored
}

func digestOf(v any) string {
	// json.Marshal walks struct fields in declaration order and map keys in sorted order, so the
	// encoding is stable for a given Go value — which is the only property this needs.
	b, err := json.Marshal(v)
	if err != nil {
		// A projection is plain data; a marshal failure means a programming error, not a data one.
		// Returning an empty digest would read as "no digest recorded", so make it loud instead.
		return fmt.Sprintf("unmarshalable:%v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- section projections -------------------------------------------------------------------
//
// Each returns a plain, fully-normalised value. Field names are short because they are hashed, never
// read; what matters is that the SET of fields is exactly the content a signer is signing off.

type digestMedia struct {
	MediaID int    `json:"m"`
	Kind    string `json:"k"`
	Caption string `json:"c"`
	Cat     string `json:"g"`
}

func designProjection(tc *entity.TechCardInsert) any {
	media := make([]digestMedia, 0, len(tc.Media))
	for _, m := range tc.Media {
		media = append(media, digestMedia{
			MediaID: m.MediaId, Kind: string(m.Kind),
			Caption: m.Caption.String, Cat: string(m.Category),
		})
	}
	callouts := make([]any, 0, len(tc.Callouts))
	for _, c := range tc.Callouts {
		callouts = append(callouts, []any{
			c.Number, c.Part.String, c.Description.String, c.Dimensions.String,
			c.MediaId.Int32, digestDecimal(c.PosX), digestDecimal(c.PosY),
		})
	}
	details := make([]any, 0, len(tc.Details))
	for _, d := range tc.Details {
		details = append(details, []any{d.Key.String, d.Text.String, d.MediaIds})
	}
	return []any{tc.Concept.String, media, callouts, details}
}

func constructionProjection(tc *entity.TechCardInsert) any {
	var construction any
	if tc.Construction != nil {
		c := tc.Construction
		// FIXED ORDER, FOREVER. Reordering these rewrites every stored CONSTRUCTION digest and marks
		// every approved section as edited-since-signing. This tuple CHANGED SHAPE in the operations
		// break (0289) — the free-text defaults became typed ones — so approvals on cards that
		// carried construction defaults are stale exactly once, by design.
		construction = []any{
			c.DefaultSeamClass.String, digestDecimal(c.DefaultStitchesPerCm),
			c.OverlockThreadCount.Int32, c.HemFinish.String, c.Pressing.String, c.Notes.String,
		}
	}
	// The operation tuple, also FIXED FOREVER. Unlike construction above, changing this one was
	// free: an EMPTY operations list marshals identically whatever shape the tuple has, and no card
	// on prod had a single operation when the break landed.
	ops := make([]any, 0, len(tc.Operations))
	for _, o := range tc.Operations {
		ops = append(ops, []any{
			o.OperationNumber.Int32, string(o.OperationType), string(o.Zone),
			o.PieceLineKeys, o.BomLineKeys, digestDecimal(o.SMV), o.CalloutNumber.Int32,
			digestDecimal(o.StitchesPerCm), o.SeamClass.String, digestDecimal(o.SeamAllowanceMm),
			o.TopstitchMode.String, digestDecimal(o.TopstitchWidthMm), o.TopstitchRows.Int32,
			o.AttachmentKind.String, digestDecimal(o.AttachmentSizeMm), o.Note.String,
		})
	}
	pieces := make([]any, 0, len(tc.Pieces))
	for _, p := range tc.Pieces {
		row := []any{
			p.LineKey, p.Name, p.PiecesPerGarment, p.Mirrored, p.Grainline,
			p.Fused, p.CalloutNumber.Int32, p.Note.String,
		}
		// A TAIL, NOT A SLOT — and this is not stylistics. json.Marshal encodes []any POSITIONALLY, so
		// a ninth element set UNCONDITIONALLY (be it "" or null) would shift the fingerprint of every
		// card in the database and declare EVERY approved CONSTRUCTION sign-off stale at the moment of
		// deploy — before anybody had marked anything. Appending only when the field is filled gives
		// exactly what is wanted: a card nobody has marked hashes byte for byte as it did before 0275,
		// and the only cards that go stale are the ones where a human actually ANSWERED the question —
		// including the answer «identical», because that too is an instruction to the floor, not the
		// absence of one. The re-approval wave is therefore the size of the marking campaign, not of
		// the rollout.
		//
		// Does the field belong in the signed content at all? Yes, without reservation. CONSTRUCTION is
		// the signature under WHAT is cut and sewn and HOW; "these two panels are a mirrored pair"
		// changes the physical part that comes out, not metadata about it (unlike purpose/is_sample,
		// which are excluded deliberately — see materialsProjection). Marking pairing on an approved
		// card without moving the signature would mean signing one thing and shipping another.
		//
		// p.Mirrored stays in slot 4 as a frozen false (0266 cleared every 1). REMOVING it is the same
		// unconditional shift as adding one: dropping an element breaks the fingerprint exactly as
		// appending one does. The column costs one BOOLEAN and one `false` in JSON; the rebase costs a
		// re-approval wave across every card at once. Do not tidy it away.
		if p.CutSymmetry.Valid {
			row = append(row, p.CutSymmetry.String)
		}
		pieces = append(pieces, row)
	}
	return []any{construction, ops, pieces}
}

// materialsProjection fingerprints what the card BUYS: which article, at what price, in what
// quantity terms.
//
// DELIBERATELY ABSENT — purpose / purpose_note / is_sample (0265), and kind / kind_note (0278) on
// exactly the same grounds. They classify a line that already exists; they do not change the
// article, the price or the consumption, so on the same reasoning as price_source they are metadata
// about a value and must not stale a sign-off whose value did not change. The concrete cost of
// folding them in would be paid immediately and by everyone: every pre-0265 line is deliberately
// unsorted and every pre-0278 line deliberately unclassified, so the operator's first sorting pass
// over an approved card would mark its MATERIALS approval stale on every single card at once — a
// wall of "changed since sign-off" that means nothing and trains people to ignore the signal that
// does. Adding them later is a digest rebase (see costingProjection's placeholder note) and needs
// the same care.
//
// THE DAY THAT CHANGES — and it is a specific day, not a vibe: the moment a kind value becomes an
// INPUT TO A DERIVATION rather than a grouping. Today `kind` only buckets lines for a screen. If it
// ever picks a costing rule, a consumption unit, an assembly step or anything the card's OUTPUT
// depends on, then "these buttons are now snaps" changes what is made, the MATERIALS signature must
// move with it, and the field must be folded in — as a POSITIONAL TAIL, appended only when filled
// (see constructionProjection's cut_symmetry note for why an unconditional element restamps every
// card in the database at deploy time instead of only the ones somebody actually answered).
func materialsProjection(tc *entity.TechCardInsert) any {
	items := make([]any, 0, len(tc.BomItems))
	for _, b := range tc.BomItems {
		items = append(items, []any{
			b.LineKey, string(b.Section), b.Name, b.Supplier.String, b.SupplierRef.String,
			b.Color.String, b.Composition.String, b.Spec.String, b.Unit.String,
			digestDecimal(b.UnitPrice), b.Currency.String, b.Comment.String,
			digestDecimal(b.FabricWidth), digestDecimal(b.FabricWeightGsm), b.FabricDirection.String,
			digestDecimal(b.WastagePercent), b.MaterialId.Int64,
		})
	}
	return items
}

// colourProjection covers the colour decision the STYLE owns: which fabric (and fusing) each cut
// piece takes in each colourway. The colourways themselves, and their lab dips, belong to the
// colourway write path and are versioned there — see the note on TechCardSectionDigests.
func colourProjection(tc *entity.TechCardInsert) any {
	out := make([]any, 0, len(tc.Pieces))
	for _, p := range tc.Pieces {
		mats := make([]any, 0, len(p.Materials))
		for _, m := range p.Materials {
			mats = append(mats, []any{
				m.ColorwayID, m.BomLineKey, m.FusingBomLineKey, m.Note.String,
			})
		}
		out = append(out, []any{p.LineKey, mats})
	}
	return out
}

func labelsProjection(tc *entity.TechCardInsert) any {
	labels := make([]any, 0, len(tc.Labels))
	for _, l := range tc.Labels {
		labels = append(labels, []any{
			string(l.LabelType), l.Content.String, l.Placement.String,
			l.Attachment.String, l.Size.String, l.Note.String, l.BomItemId.Int32,
		})
	}
	return labels
}

func packagingProjection(tc *entity.TechCardInsert) any {
	if tc.Packaging == nil {
		return nil
	}
	p := tc.Packaging
	return []any{
		p.FoldingMethod.String, p.Polybag.String, p.BagSticker.String, p.Inserts.String,
		p.UnitsPerBox.Int32, p.BoxMarking.String, p.BoxDimensions.String,
		p.WeightNetGrams.Int32, p.WeightGrossGrams.Int32, p.Notes.String,
	}
}

// costingProjection fingerprints what the card COSTS: the manual articles, the declared typical
// run, and — since the range-average basis (T6) — the DECLARED SIZE RANGE the standard cost
// averages over.
//
// WHY size_ids are in here even though the range lives on the card header. The range is not
// metadata about the costing, it is an INPUT TO THE DERIVATION: a size-graded material norm
// enters the style cost as Σ norm / |range| over exactly that set
// (entity.TechCardColorwayUsage.RangeAverageTotal), so adding or removing a size reprices every
// colourway of the style. Leaving it out would give exactly the failure the whole mechanism
// exists to prevent — a costing sign-off staying green over a number that changed. That is the
// same test materialsProjection sets for `kind`: the day a field becomes an input to a
// derivation rather than a grouping, it must join the signature.
//
// SORTED, because only MEMBERSHIP is the input: Σ/n is order-blind, so reordering the declared
// range must not restamp a signature, while adding or removing a size must. Hashing the stored
// order would let a cosmetic reshuffle read as «changed since sign-off».
//
// base_sample_size_id LEFT the projection in the same change, unconditionally: it is a reference
// «размер образца» now, not an input — re-pointing it moves no figure, so keeping it hashed
// would restamp signatures over nothing. Swapping the basis element restamps every stored
// COSTING digest at deploy — every approved costing sign-off goes stale at once. That is honest,
// not collateral damage (owner: «переподпишем»): the basis of the number under those signatures
// changed in the same commit, from «the base size's own norm» to «the simple average over the
// declared range», so no existing approval describes the current figure. The positional-tail
// trick (see constructionProjection) would have bought nothing here — there is no card for which
// the two bases agree by construction.
func costingProjection(tc *entity.TechCardInsert) any {
	var costing any
	if c := tc.Costing; c != nil {
		// Positions 2 and 3 held hardware_cost / packaging_cost until Phase 2 removed them. They stay
		// as the canonical empty placeholder ("" = digestDecimal of an unset decimal) so the digest of
		// a card that never had those scalars is BYTE-IDENTICAL to what its approver signed — the
		// removal must not mass-stale every approved costing sign-off (review S1, digest-rebase). A
		// card whose scalar WAS migrated changes digest legitimately: its costing content moved.
		costing = []any{
			digestDecimal(c.CmtCost), "", "", digestDecimal(c.LogisticsCost),
			digestDecimal(c.OverheadCost), digestDecimal(c.DefectPercent), c.Currency.String, c.Notes.String,
			digestDecimal(c.TargetMarginPct),
		}
	}
	qty := make([]any, 0, len(tc.SizeQuantities))
	for _, q := range tc.SizeQuantities {
		qty = append(qty, []any{q.SizeId, q.OrderQty})
	}
	rangeIds := append([]int(nil), tc.SizeIds...)
	sort.Ints(rangeIds)
	rangeVals := make([]any, 0, len(rangeIds))
	for _, id := range rangeIds {
		rangeVals = append(rangeVals, id)
	}
	return []any{costing, qty, rangeVals}
}

// dec renders a nullable decimal as a canonical string, so 1.50 and 1.5 — which the DB may return
// differently from what the client sent — cannot produce two different fingerprints.
func digestDecimal(d decimal.NullDecimal) string {
	if !d.Valid {
		return ""
	}
	return d.Decimal.String()
}
