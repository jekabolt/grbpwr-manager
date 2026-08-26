package techcardarchive

import (
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ─────────────────────────────────────────────────────────────────────────────
// The approval sanitiser: an imported card cannot look signed or released.
//
// This is the second line, not the first. By construction the archive carries no
// sign-offs, no release stamp and no digest — the export builder cuts them and the
// format has no field for a signature at all (doc.go, FORMAT.md §4). The first line
// is therefore "our exporter is polite". This file exists for the archive that our
// exporter did not write: hand-made, produced by a future MINOR, or hostile. Such an
// archive can put a full APPROVED sign-off set and a release timestamp into card.json,
// and protojson will read them into the insert without complaint.
//
// What that would cost is not a data defect but FALSE EVIDENCE. A tech card is the
// document a factory cuts against, and «approved / released» on it is an assertion that
// a named person signed a named section against a named content digest. A card that
// arrives already wearing those marks asserts an approval nobody performed. Everything
// else the import gets wrong degrades into a report line an operator can read and fix;
// this one degrades into a floor worker believing a sheet was blessed.
//
// It also cannot be delegated downstream, because the create pipeline COERCES rather
// than refuses: prepareCreateTechCardSignoffs (internal/apisrv/admin/techcard.go) takes
// the sign-offs it is handed, stamps them with the acting username and the current time,
// and restampFreshSignoffDigests then fingerprints the payload being written — so an
// APPROVED section sent in on a create path comes back out as a FRESH, internally
// consistent, digest-matching approval attributed to whoever ran the import. There is no
// later gate that can undo that. The single defence is to never put them on the input.
// ─────────────────────────────────────────────────────────────────────────────

// ApprovalFieldNames is the approval family: every field of the tech-card write contract
// that says a human blessed this card, or when. It is a NAME list run over the whole
// insert tree by RedactFieldsDeep, not four assignments on the top-level struct, for the
// same reason MoneyFieldNamesArchive is a name list: the four names are cleared wherever
// they occur, including in a nested message that does not exist yet.
//
// The list is kept honest in both directions by TestSanitizeFieldGuard — a new
// approval-shaped field anywhere under TechCardInsert that is neither in this list nor in
// the guard's exclusions (with a written reason) turns that test red, and a name here that
// no longer resolves turns it red too, because a clearing that silently stopped happening
// is exactly the failure this file is about.
//
// `signoffs` is a whole message and is cleared whole: RedactFieldsDeep does not descend
// into a matched field, so signed_by / signed_at / signed_digest / state go with it and a
// field added to TechCardSignoff later cannot leak through this door.
//
// NOT in the list, deliberately:
//   - `status` — freeform workflow notes, soft and non-gating (techcard.proto). It is
//     prose the studio writes to itself, carries no authority, and blanking it would
//     destroy content the archive was asked to carry. A card whose status text reads
//     "released" while its approval_state is DRAFT is a note, not a claim.
//   - `stage` — where the style is in its life (IDEA/PROTO/…), not who approved it. An
//     imported production style is still a production style.
//   - `style_number` — collision strategy is Ф3.3's, not the sanitiser's.
var ApprovalFieldNames = map[string]bool{
	"approval_state": true, // TechCardApprovalState; the visible «APPROVED / RELEASED» badge
	"released_at":    true, // when the card was released to manufacture
	"approved_at":    true, // auto-set on approve/release; a stamp of an approval that did not happen
	"signoffs":       true, // repeated TechCardSignoff — the per-section signatures themselves
}

// SanitizedApprovalState is what an imported card is forced to, always. DRAFT rather than
// UNKNOWN even though UNKNOWN "defaults to DRAFT on write": the default is a promise made
// by a different layer, and a sanitiser whose result is correct only because somebody
// else's conversion happens to fill the blank is not a sanitiser. The value written here
// is the value that reaches the database.
const SanitizedApprovalState = pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_DRAFT

// SanitizeImportedCard forces an incoming card to draft: no approval state above DRAFT, no
// release stamp, no approval stamp, no sign-offs — anywhere in the tree.
//
// Unconditional by contract. It takes no flag and asks no question about the caller, the
// archive's provenance or its manifest, because there is no answer to either that would
// make importing somebody else's approval correct. Ф2.3 calls it on the way in, before any
// remapping, and nothing downstream may re-add what it removed.
//
// nil-safe: card.json can be absent or empty and the resolver reads TechCard.TechCard,
// which is nil for a card message that carries only its read-side projections.
func SanitizeImportedCard(card *pb_common.TechCardInsert) {
	if card == nil {
		return
	}
	// Clear by name over the whole tree, then stamp. The order matters only in that the
	// stamp must come second: RedactFieldsDeep would clear approval_state back to UNKNOWN.
	RedactFieldsDeep(card.ProtoReflect(), ApprovalFieldNames)
	card.ApprovalState = SanitizedApprovalState
}
