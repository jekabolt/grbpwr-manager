package admin

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
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
// listing them per export would bury the real holes under the same three lines every time.
//
// There is exactly ONE hole this builder raises, and it is not about a field at all: the re-import
// probe below, which says «no import will accept this card» while the person who could still fix it
// is holding the file. See reimportProbe for the measurement it rests on.
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

	// Asked and answered BEFORE the three passes below, for the reason in the probe's own header:
	// two of the things they remove are things the import puts back, and probing after them would
	// report the format as a defect of the card.
	holes := reimportProbe(pb.GetTechCard(), card)

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
	return blob, holes, nil
}

// reimportProbe answers, ON THE EXPORT, the question the import used to answer weeks later in
// another base: will anything accept this card back?
//
// THE MEASUREMENT BEHIND IT. The store is SOFTER than the converter. Every write that arrives over
// the API — create, update, the season clone — passes dto.ConvertPbTechCardInsertToEntity; a write
// that goes straight to the store passes nothing, and the store says so in its own comments rather
// than re-checking (store/techcard/production.go: «СТОРОЖА "СЧЁТНОЙ СЕКЦИИ" ЗДЕСЬ НЕТ … его
// проверил конвертер»). Measured on this tree, at least three rules live only in the converter: a
// per-step material count on a MEASURED BOM section (parseOperationBomQuantities), a profile key
// that is not 26 alphanumerics (parseProfileKey → validatePatternLineKey), and the assembly graph
// rules 1-3/5-7 (canonicalizeAssembly → entity.AssemblySweep). AddTechCard writes all three
// happily. Such a card EXPORTS perfectly and is refused WHOLE on the far side — the import runs
// exactly this converter (tcciPayload), and a refusal there is not a hole but a corrupt-archive
// error, which is the right answer to the wrong person: the operator holding the file did not
// write the card and is told about a field violation in a payload they never composed.
//
// A HOLE, NOT AN EXPORT FAILURE. The archive still opens, still carries every file and sidecar, and
// is still the right thing to hand somebody who wants to READ the card or lift parts of it. What
// was missing was never the file. It was the sentence, said in the one place where the person can
// still act on it — the export response carries the manifest for exactly this purpose.
//
// WHAT IT IS RUN ON, AND WHY THAT IS NOT THE ARCHIVE'S OWN BYTES. Two of the things the archive
// deliberately does NOT carry are things the import puts back before it converts: the pattern URLs
// (blanked by the sanitiser here, overwritten with the target's own re-uploaded objects by
// tcflApplyPatternObjects there) and every foreign id (remapped through id_maps). Probing the
// sanitised payload would therefore report the FORMAT as a defect of the card — every card with a
// single pattern sheet would come out «not importable». So the probe runs on the writable half as
// it stands in THIS base, where the ids are its own.
//
// THE PATTERN URL IS TAKEN OUT OF SCOPE OUTRIGHT, and that is the one place this probe deliberately
// asks less than the import does. The import never converts the url the archive travelled with —
// it overwrites every surviving row's url first, and drops the rows it could not re-upload — so a
// refusal about that field would be the one thing worse than no line at all: a true-sounding
// sentence («no import will take this card») whose cause the import never even looks at. Each row
// gets a stand-in of the same shape instead, and with no bucket host configured — the state in
// which the live save path cannot store a pattern row either — there is no stand-in to give and the
// rows leave the probe's scope rather than being blamed for the configuration.
//
// What that costs is honesty about scope, stated here: this probe cannot answer questions about the
// TARGET (a size name that base lacks, an article it never heard of) — those are the resolver's
// holes and are already reported by their own codes.
//
// THE SIX AWARE FLAGS ARE SET, and that is not cosmetic. They are write-only transport flags, so
// the read converter never emits them, and the import's resolver sets all six to true
// (techcard_archive_resolve.go). They change what is checked: with AssemblyAware false the graph is
// classified from the LEGACY piece-only projection, so a card whose unit graph breaks rule 3 would
// probe clean and still be refused on arrival. A probe that runs a different converter than the
// import is worse than no probe.
//
// COST: one proto.Clone of the writable half and one pure-Go conversion — no context, no database,
// no bucket. The clone is insurance rather than a known need (the converter reads its input and
// mutates only the entity it builds), because a probe that damaged the payload it was inspecting
// would be a defect introduced by the guard against defects.
func reimportProbe(insert *pb_common.TechCardInsert, card *entity.TechCard) []techcardarchive.ExportHole {
	if insert == nil {
		return nil
	}
	probe := proto.Clone(insert).(*pb_common.TechCardInsert)
	if standIn := dto.ManagedPatternURLStandIn(); standIn != "" {
		for i, p := range probe.Patterns {
			if p == nil {
				continue
			}
			// ONE STAND-IN PER ROW, not one for all of them, because the parser reads the url a
			// second time: two keyed rows sharing a (size, url) pair are refused as one sheet hung
			// twice. A shared stand-in would invent that collision on any card with two sheets in
			// one size; a distinct one models what the import actually produces, where the upload
			// loop runs once per line_key and every surviving row ends up on an object of its own.
			p.Url = fmt.Sprintf("%s?row=%d", standIn, i)
		}
	} else {
		probe.Patterns = nil
	}
	probe.AssemblyAware = true
	probe.MachineFieldsAware = true
	probe.MediaAware = true
	probe.OperationKindsAware = true
	probe.OperationWorkAware = true
	probe.BomQtyAware = true

	if _, err := dto.ConvertPbTechCardInsertToEntity(probe); err != nil {
		return []techcardarchive.ExportHole{{
			Entity: techcardarchive.EntityCard,
			Ref:    archiveCardRef(card),
			Reason: techcardarchive.ReasonCardNotImportable,
			Detail: "an import runs the same check and would refuse the whole archive: " + err.Error(),
		}}
	}
	return nil
}

// archiveCardRef names the card the way every other ref in this format names its row: by the thing
// a person reads it by. The id is the fallback and not the first choice — a style number is the
// same string in the report, on the screen and on the archive's file name, while an id means
// nothing outside this base (§2: nothing is resolved through source.*).
func archiveCardRef(card *entity.TechCard) string {
	if card == nil {
		return "tech_card_id=0"
	}
	if n := card.StyleNumber; n.Valid && n.String != "" {
		return "style_number=" + n.String
	}
	return fmt.Sprintf("tech_card_id=%d", card.Id)
}

// sanitizeCardForArchive removes what belongs to the EXPORTING INSTANCE rather than to the card:
// its signatures, its accounts, its fit model, its URLs, its warehouse balances, its price
// provenance and the digests it derived. Money is not this function's job — it runs between the two
// money layers and touches only what neither of them can see, since none of these names is money
// and a name list cannot express "blank the url INSIDE resolved media".
//
// What deliberately stays (owner decisions B-2 / B-3, FORMAT.md §4): created_by / updated_by and
// the revision journal. They are provenance a receiving constructor reads and cannot resolve
// anything through — unlike an account assignment, which names a row in the source's admins table.
// They stay IN THE FILE and are not written on the far side: the imported row is stamped with the
// operator who ran the import, and the archive's journal never becomes the target's (FORMAT.md
// §4.2 — free-text change notes are the last channel by which somebody else's prices could enter
// our permanent records as prose, and neither money layer can read prose).
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

	// The measured scopes' staleness verdict is the SAME CLASS as the digests above, and is cut in
	// the same gesture rather than by a rule of its own: the server recomputes it on every read by
	// comparing today's sheet fingerprint with the one the areas were measured under (see
	// entity.PieceAreaScope — «Staleness is COMPUTED, not stored»), so storing it in a file is
	// storing what is derivable.
	//
	// The areas themselves stay — they are the measurement, and they travel and are written
	// (FORMAT.md §4.1 №27). Only the verdict OVER them goes, and it is meaningless to the receiver
	// twice over: it speaks about the SOURCE instance's pattern files, which the receiver does not
	// have and cannot check; and it is false on arrival either way, because the import mints a
	// domain-separated fingerprint so an imported scope always reads «measured elsewhere, the files
	// have changed since» (insertImportedPieceAreas). A carried `false` would look, on a partner's
	// screen, like a statement that the measurement is current — one line above the reader that is
	// about to compute the opposite.
	for _, sc := range pb.PieceAreaScopes {
		if sc != nil {
			sc.Stale = false
		}
	}

	// Colourways are products and do not travel as products (FORMAT.md §5.3): what a receiving
	// instance can use of them — the recipe and the piece materials — rides in colorways.json,
	// keyed by colour code, and this derived list would only offer the source's product ids
	// alongside a second, money-bearing copy of the same recipe.
	pb.Colorways = nil

	// Output variants are WAREHOUSE BUCKETS, and buckets do not travel — the same decision as the
	// colourways above, taken by the same rule rather than by a second mechanism invented for it.
	//
	// The loudest of the three reasons is not an id at all: `on_hand` is the SOURCE'S CURRENT STOCK
	// BALANCE of that bucket. It is not money, so neither money layer can see it; it is not an id,
	// so §6.2 says nothing about it. It rode out of the building purely because the message it sits
	// on is OUTPUT-ONLY and nobody cut it — and a partner opening this archive on the same admin
	// panel reads how much of that article we hold today.
	//
	// The other two are the ordinary ones. `id` / `tech_card_id` are the source rows' own identity.
	// `material_id` names a row of the source's material catalogue with NO passport travelling
	// beside it: collectArchiveMaterials builds passports from BOM lines, colourway usages and
	// `output_material_id` — never from a variant's bucket — so §6.2 ("every id is either remapped
	// or dropped") leaves exactly one option for it.
	//
	// WHY NOT REFERENCE-ONLY — colour codes kept, ids and balance cut? Because nothing on the far
	// side would read it. A variant owns warehouse stock, so a full-replace card save may not mint
	// one: they exist only through Upsert/DeleteTechCardOutputVariant, and entity.TechCardArchiveImport
	// accordingly has no member for them. A pruned copy would be reference nobody consumes, kept as
	// RAW protojson of an output-only message — which is the very property that let `on_hand` out.
	// The next field added to TechCardOutputVariant would ship the same way, without anyone deciding
	// it should. (Reference payloads that a human DOES read have a sidecar of their own: §5.3.)
	//
	// WHAT THE CARD PRODUCES IS NOT LOST. The output ARTICLE travels by the R2 mechanism —
	// `output_material_id` stays on the insert half and its passport rides in materials/index.json
	// under ref=output_material, where resolveOutputMaterial matches it against the target
	// catalogue. The COLOUR DIMENSION over that article is what the receiving instance declares for
	// its own warehouse, in its own buckets, through the RPC that owns them.
	pb.OutputVariants = nil

	// Nil-checked like stripTechCardCosting rather than assumed, so the two are safe in either
	// order on the same value; a card without its insert half does not occur in practice.
	if ins := pb.TechCard; ins != nil {
		// An imported card must never LOOK signed (FORMAT.md §6.1). The import forces draft and
		// drops signoffs, and this is the other half of that: the archive does not carry them at
		// all, so nothing has to be trusted to strip them later.
		ins.Signoffs = nil

		// The fit model is a row in the SOURCE's model table — the same class as role_assignments
		// (FORMAT.md §4: "a row in a table the target does not share"), and no model dictionary
		// travels beside it. Blanked HERE and not only on the import: an id left in card.json is a
		// number a foreign reader of this format has no way to know is nobody's, and §6.2's "every
		// id is either remapped or dropped" is a promise the FILE has to keep, not just our
		// resolver. The import clears it too — that is defence against a hand-made archive, and a
		// log is enough there because our own exports no longer produce one.
		ins.BaseModelId = 0

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
