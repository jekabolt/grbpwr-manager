package admin

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/encoding/protojson"
)

// ─────────────────────────────────────────────────────────────────────────────
// card.json — the archive's central file (FORMAT.md §1, §4).
//
// This lives in package admin and not in internal/techcardarchive on purpose: the two things it
// needs — dto.ConvertEntityTechCardToPb's admin-side callers and stripTechCardCosting — are the
// API layer's, and the archive format package must not depend on a handler package (walk.go says
// the same about the direction of that arrow).
// ─────────────────────────────────────────────────────────────────────────────

// buildArchiveCardJSON renders a resolved tech card as the archive's card.json.
//
// It takes NO context and NO caller: money removal is a property of the FORMAT, not of who asked.
// There is nowhere in this signature to put a costing:read check, which is the point — an archive
// is a file that travels to a factory, and the question "was the exporter allowed to see prices"
// has no bearing on what may leave the building. The API's own RBAC redaction (costing_rbac.go)
// answers a different question for a different reader.
//
// Three passes, in this order:
//
//  1. stripTechCardCosting — the same by-name cut the read handlers apply, run unconditionally.
//  2. sanitizeCardForArchive — the exporting INSTANCE's own facts: signatures, tokenised URLs,
//     account assignments, price provenance.
//  3. techcardarchive.RedactFieldsDeep — the recursive net under both of the above.
//
// Layers 1 and 3 overlap by design, and MEASURED (2026-08-25) they overlap COMPLETELY on today's
// contract: comment out either one alone and card.json still comes out money-free — the mutation
// pair for this file had to remove BOTH before the test went red. That is the intended state, not
// an accident to tidy up. Layer 1 is the list the live RPCs are already held to, so the archive can
// never redact less than an API response does even if MoneyFieldNamesArchive is edited; layer 3
// catches money layer 1 never enumerated — a new field on a nested message, a message reached by a
// path nobody wrote down. Each is the other's floor, and TestBuildArchiveCard measures both
// separately, because a single-pipeline test cannot tell a working layer from a redundant one.
//
// The returned holes are what the export could not CARRY (FORMAT.md §2). Nothing the three passes
// above remove is a hole: those removals are the format itself, identical in every archive, and
// listing them per export would bury the real holes under the same three lines every time. The
// slice is returned anyway so the seam with the writer (which merges card + sidecar holes into the
// manifest) does not have to change the day card.json grows a case that IS one.
func buildArchiveCardJSON(card *entity.TechCard) ([]byte, []techcardarchive.ExportHole, error) {
	if card == nil {
		return nil, nil, fmt.Errorf("build card.json: no tech card")
	}

	// A ZERO CostingFx, deliberately, where the release snapshot passes s.costingFx(ctx).
	// The costing block this feeds is cut whole four lines below, so fetching the FX table would
	// buy a database read and a currency rollup for a number that is deleted before it is
	// serialised. The converter is documented to accept a zero value ("pass a zero CostingFx to
	// omit the *_base figures") and CostingFx.toBase returns early on an empty Base, so the only
	// difference is that costing arrives without its *_base rollup — into the bin either way.
	pb := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{})

	stripTechCardCosting(pb)
	sanitizeCardForArchive(pb)
	techcardarchive.RedactFieldsDeep(pb.ProtoReflect(), techcardarchive.MoneyFieldNamesArchive)

	blob, err := protojson.Marshal(pb)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal card.json: %w", err)
	}
	// The reader refuses a card.json over this ceiling (FORMAT.md §1.3), so writing one would
	// produce an archive that this very format cannot open. Fail here, where the operator still
	// gets an error naming the card, rather than at the far end where it reads as corruption.
	if len(blob) > techcardarchive.MaxCardJSONBytes {
		return nil, nil, fmt.Errorf("card.json is %d bytes, over the format ceiling of %d",
			len(blob), techcardarchive.MaxCardJSONBytes)
	}
	return blob, nil, nil
}

// sanitizeCardForArchive removes what belongs to the EXPORTING INSTANCE rather than to the card:
// its signatures, its accounts, its URLs, its price provenance and the digests it derived. Money
// is not this function's job — it runs between the two money layers and touches only what neither
// of them can see, since none of these names is money and a name list cannot express "blank the
// url INSIDE resolved media".
//
// What deliberately stays (owner decisions B-2 / B-3, FORMAT.md §4): created_by / updated_by and
// the revision journal. They are provenance a receiving constructor reads and cannot resolve
// anything through — unlike an account assignment, which names a row in the source's admins table.
func sanitizeCardForArchive(pb *pb_common.TechCard) {
	if pb == nil {
		return
	}

	// Role assignments name accounts (admin_id + username) in the SOURCE instance's admins table.
	pb.RoleAssignments = nil

	// Section digests are a READ-SIDE projection the converter recomputes on every read, and PLAN
	// §7 forbids storing what is derivable. Two reasons beyond the letter of that rule: the costing
	// section's digest was computed from the insert half BEFORE the money was cut, so it is a
	// fingerprint of exactly what we just removed; and on the receiving side every one of them is
	// false the moment ids are remapped, while looking to a partner like a statement about
	// integrity. The importing instance recomputes them for itself.
	pb.SectionDigests = nil

	// Colourways are products and do not travel as products (FORMAT.md §5.3): what a receiving
	// instance can use of them — the recipe and the piece materials — rides in colorways.json,
	// keyed by colour code, and this derived list would only offer the source's product ids
	// alongside a second, money-bearing copy of the same recipe.
	pb.Colorways = nil

	// Nil-checked like stripTechCardCosting rather than assumed, so the two are safe in either
	// order on the same value; a card without its insert half does not occur in practice.
	if ins := pb.TechCard; ins != nil {
		// An imported card must never LOOK signed (FORMAT.md §6.1). The import forces draft and
		// drops signoffs, and this is the other half of that: the archive does not carry them at
		// all, so nothing has to be trusted to strip them later.
		ins.Signoffs = nil

		for _, p := range ins.Patterns {
			if p == nil {
				continue
			}
			// Tokenised URLs on THIS backend's origin (/api/p/{token}). The DXF/PDF bytes travel
			// in patterns/, and the token resolves only against the source's object table.
			//
			// Empty already on this path — the converter does not fill them, the handler layer
			// does (patternaccess.Service decorates a response after conversion). Blanked anyway,
			// because "card.json carries no instance URL" has to hold for the file, not for one
			// call path: the day the builder is handed an already-decorated message, this is what
			// stands between that and a shipped token.
			p.ViewUrl = ""
			p.DownloadUrl = ""

			// The source instance's object key, worn as a URL (FORMAT.md §4: object keys are
			// blanked). Blanking it is stricter than tidiness:
			// ConvertPbTechCardInsertToEntity REQUIRES a non-empty url and checks it against
			// managedPatternHosts plus the tech-card-patterns segment. A foreign host fails the
			// WHOLE import loudly; a host that happens to match (beta and prod behind one CDN)
			// is worse — the row lands in the target base pointing at an object nobody moved.
			// Empty makes the failure loud AND addressed at the row. Ф3.1 substitutes the
			// re-uploaded url before conversion; anything it did not reach must not convert.
			p.Url = ""
		}

		for _, b := range ins.BomItems {
			if b == nil {
				continue
			}
			// The source and date of a price whose figure is gone. Provenance without the number
			// is still a leak — "priced from a production run on 12 March" says the article was
			// bought and when — and it is meaningless in the target, whose own price will have a
			// provenance of its own. (MoneyFieldNamesArchive carries both names too; this is the
			// by-name half of the same decision, next to unit_price/currency in layer 1.)
			b.PriceSource = ""
			b.PriceSnapshotAt = nil
		}
	}

	// Resolved media are the read-side projection of the media slots: the slot's media_id stays
	// (media/index.json is keyed by it and the import remaps it), the CDN links do not.
	blankResolvedMediaURLs(pb.ResolvedMoodboardMedia)
	blankResolvedMediaURLs(pb.ResolvedTechnicalMedia)
	blankResolvedMediaURLs(pb.ResolvedOperationMedia)
}

// blankResolvedMediaURLs empties the three MediaInfo urls of every resolved media item, keeping
// their width/height and the blurhash: those describe the picture, not where this instance keeps
// it, and a reader that has the bytes still wants the dimensions.
func blankResolvedMediaURLs(items []*pb_common.TechCardMediaFull) {
	for _, it := range items {
		mi := it.GetMedia().GetMedia()
		if mi == nil {
			continue
		}
		for _, info := range []*pb_common.MediaInfo{mi.GetFullSize(), mi.GetThumbnail(), mi.GetCompressed()} {
			if info != nil {
				info.MediaUrl = ""
			}
		}
	}
}
