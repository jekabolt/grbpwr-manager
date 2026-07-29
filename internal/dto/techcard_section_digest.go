package dto

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

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
//	present→ "I am not re-approving, I am just saving." Carried through verbatim, so an approval
//	         made three edits ago keeps pointing at the content it actually covered.
//
// A section that is not approved carries no digest at all: pending and rejected have nothing to be
// stale against.
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
			continue // an unrelated save: the approval still covers what it covered
		}
		tc.Signoffs[i].SignedDigest = nullStringFromPb(digests[tc.Signoffs[i].Section])
	}
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
		construction = []any{
			c.MainStitchType.String, c.StitchDensity.String, c.OverlockThreads.String,
			c.SeamAllowances.String, c.HemFinish.String, c.Pressing.String,
			c.MachineClass.String, c.Notes.String,
		}
	}
	ops := make([]any, 0, len(tc.Operations))
	for _, o := range tc.Operations {
		ops = append(ops, []any{
			o.Node, o.Description.String, o.SeamType.String, digestDecimal(o.StitchesPerCm),
			o.TopstitchWidth.String, o.Thread.String, o.Note.String, o.OperationNumber.Int32,
			o.Machine.String, o.SeamAllowance.String, o.Needle.String, digestDecimal(o.TimeNorm),
			o.Attachment.String, string(o.OperationType), o.CalloutNumber.Int32,
			string(o.Zone), o.Placement.String, o.BomLineKeys, o.PieceLineKeys,
		})
	}
	pieces := make([]any, 0, len(tc.Pieces))
	for _, p := range tc.Pieces {
		pieces = append(pieces, []any{
			p.LineKey, p.Name, p.PiecesPerGarment, p.Mirrored, p.Grainline,
			p.Fused, p.CalloutNumber.Int32, p.Note.String,
		})
	}
	return []any{construction, ops, pieces}
}

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

func costingProjection(tc *entity.TechCardInsert) any {
	var costing any
	if c := tc.Costing; c != nil {
		costing = []any{
			digestDecimal(c.CmtCost), digestDecimal(c.HardwareCost), digestDecimal(c.PackagingCost), digestDecimal(c.LogisticsCost),
			digestDecimal(c.OverheadCost), digestDecimal(c.DefectPercent), c.Currency.String, c.Notes.String,
			digestDecimal(c.TargetMarginPct),
		}
	}
	qty := make([]any, 0, len(tc.SizeQuantities))
	for _, q := range tc.SizeQuantities {
		qty = append(qty, []any{q.SizeId, q.OrderQty})
	}
	return []any{costing, qty}
}

// dec renders a nullable decimal as a canonical string, so 1.50 and 1.5 — which the DB may return
// differently from what the client sent — cannot produce two different fingerprints.
func digestDecimal(d decimal.NullDecimal) string {
	if !d.Valid {
		return ""
	}
	return d.Decimal.String()
}
