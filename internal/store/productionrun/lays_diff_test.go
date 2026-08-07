package productionrun

import (
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// The section diff is the whole reason настилы are their own aggregate, so its rules are pinned
// here without a database — the same arrangement planRunLineDiff has, and for the same reason: the
// ordering argument is what makes the write safe, and an argument that can only be checked by
// running it against MySQL is an argument nobody checks.
//
// What every case below is really asserting is ONE property: a stored section's id SURVIVES any
// edit that is not a deletion. Ф5б hangs the consumption fact and the cutting receipt off that id,
// and a full replace would re-mint it on every save — including a save that only touched the lay's
// note. That is verbatim the failure 0230 was written to prevent for the run's plan lines.

func sec(key string, marker, plies, position int) entity.ProductionRunLaySectionInsert {
	return entity.ProductionRunLaySectionInsert{SectionKey: key, MarkerId: marker, Plies: plies, Position: position}
}

func keysOf(sections []entity.ProductionRunLaySectionInsert) []string {
	keys := make([]string, len(sections))
	for i := range sections {
		keys[i] = sections[i].SectionKey
	}
	return keys
}

const (
	keyA = "01LAYSECTIONAAAAAAAAAAAAAA"
	keyB = "01LAYSECTIONBBBBBBBBBBBBBB"
	keyC = "01LAYSECTIONCCCCCCCCCCCCCC"
)

// TestPlanLaySectionDiffKeepsIdOnPliesEdit is acceptance probe §13.6, first clause: the single most
// common edit there is — "make it 30 plies instead of 24" — must be an UPDATE IN PLACE.
func TestPlanLaySectionDiffKeepsIdOnPliesEdit(t *testing.T) {
	stored := []laySectionIdentity{
		{Id: 11, SectionKey: keyA, MarkerId: 7, Plies: 24, Position: 0},
	}
	payload := []entity.ProductionRunLaySectionInsert{sec(keyA, 7, 30, 0)}

	plan := planLaySectionDiff(stored, payload, keysOf(payload))

	if len(plan.deletes) != 0 || len(plan.inserts) != 0 {
		t.Fatalf("a ply edit must not delete or insert: %+v", plan)
	}
	if len(plan.updates) != 1 || plan.updates[0].id != 11 {
		t.Fatalf("the stored id must survive a ply edit, got %+v", plan.updates)
	}
	if !plan.updates[0].changed || !plan.Changed() {
		t.Error("a ply edit is a real change and must trigger the quantity snapshot refresh")
	}
}

// TestPlanLaySectionDiffKeepsIdsOnReorder is acceptance probe §13.6, second clause. Position is an
// EDITABLE ATTRIBUTE, not an identity: keying on it would make swapping two sections read as
// deleting both and creating two others, which is exactly the re-mint the ids must survive.
func TestPlanLaySectionDiffKeepsIdsOnReorder(t *testing.T) {
	stored := []laySectionIdentity{
		{Id: 11, SectionKey: keyA, MarkerId: 7, Plies: 24, Position: 0},
		{Id: 12, SectionKey: keyB, MarkerId: 8, Plies: 12, Position: 1},
	}
	// The same two sections, swapped.
	payload := []entity.ProductionRunLaySectionInsert{sec(keyB, 8, 12, 0), sec(keyA, 7, 24, 1)}

	plan := planLaySectionDiff(stored, payload, keysOf(payload))

	if len(plan.deletes) != 0 || len(plan.inserts) != 0 {
		t.Fatalf("a reorder must not delete or insert: %+v", plan)
	}
	got := map[string]int{}
	for _, u := range plan.updates {
		got[payload[u.index].SectionKey] = u.id
	}
	if got[keyA] != 11 || got[keyB] != 12 {
		t.Fatalf("both ids must survive a reorder, got %+v", got)
	}
	if !plan.Changed() {
		t.Error("a reorder changes the lay and must refresh the snapshot")
	}
}

// TestPlanLaySectionDiffDeletesOmitted is acceptance probe §13.6, third clause. INSIDE one lay the
// submitted list IS the lay: leaving an unmentioned section alone would leave no way to remove one.
// (Between lays the rule is the opposite — see TestSaveLaySurvivesRunSave in the integration suite.)
func TestPlanLaySectionDiffDeletesOmitted(t *testing.T) {
	stored := []laySectionIdentity{
		{Id: 11, SectionKey: keyA, MarkerId: 7, Plies: 24, Position: 0},
		{Id: 12, SectionKey: keyB, MarkerId: 8, Plies: 12, Position: 1},
	}
	payload := []entity.ProductionRunLaySectionInsert{sec(keyA, 7, 24, 0)}

	plan := planLaySectionDiff(stored, payload, keysOf(payload))

	if len(plan.deletes) != 1 || plan.deletes[0] != 12 {
		t.Fatalf("the omitted section must be deleted, got %+v", plan.deletes)
	}
	if len(plan.updates) != 1 || plan.updates[0].id != 11 {
		t.Fatalf("the surviving section keeps its id, got %+v", plan.updates)
	}
	if len(plan.inserts) != 0 {
		t.Fatalf("nothing new arrived: %+v", plan.inserts)
	}
	if !plan.Changed() {
		t.Error("a deletion changes the lay")
	}
}

// TestPlanLaySectionDiffInsertsNewKeys pins that an unknown key is a create, not an error — the
// client mints section identities before the first save, which is what makes a retry idempotent.
func TestPlanLaySectionDiffInsertsNewKeys(t *testing.T) {
	stored := []laySectionIdentity{{Id: 11, SectionKey: keyA, MarkerId: 7, Plies: 24, Position: 0}}
	payload := []entity.ProductionRunLaySectionInsert{sec(keyA, 7, 24, 0), sec(keyC, 9, 6, 1)}

	plan := planLaySectionDiff(stored, payload, keysOf(payload))

	if len(plan.inserts) != 1 || plan.inserts[0] != 1 {
		t.Fatalf("the new key must be inserted, got %+v", plan.inserts)
	}
	if len(plan.deletes) != 0 {
		t.Fatalf("nothing vanished: %+v", plan.deletes)
	}
	if !plan.updates[0].changed && !plan.Changed() {
		t.Error("an insert changes the lay even when the matched rows did not move")
	}
}

// TestPlanLaySectionDiffIdenticalPayloadIsNoChange is the note-only save, and it is the case the
// staleness badge depends on. A byte-identical section list must report NO change, so
// refreshLayQtySnapshot is not called and the "quantities changed" badge is not laundered by an
// edit that never touched a section.
func TestPlanLaySectionDiffIdenticalPayloadIsNoChange(t *testing.T) {
	stored := []laySectionIdentity{
		{Id: 11, SectionKey: keyA, MarkerId: 7, Plies: 24, Position: 0},
		{Id: 12, SectionKey: keyB, MarkerId: 8, Plies: 12, Position: 1},
	}
	payload := []entity.ProductionRunLaySectionInsert{sec(keyA, 7, 24, 0), sec(keyB, 8, 12, 1)}

	plan := planLaySectionDiff(stored, payload, keysOf(payload))

	if len(plan.deletes) != 0 || len(plan.inserts) != 0 {
		t.Fatalf("an identical payload writes nothing new: %+v", plan)
	}
	for _, u := range plan.updates {
		if u.changed {
			t.Errorf("section %d reported changed on an identical payload", u.id)
		}
	}
	if plan.Changed() {
		t.Error("an identical payload must NOT refresh the quantity snapshot")
	}
}

// TestPlanLaySectionDiffMarkerSwapIsAnUpdate: replacing the раскладка a section lays is an edit of
// that section, not a new section. The operator re-nested and wants the same block of the lay to
// use the new layout — and the consumption fact already hanging off this row belongs to that block.
func TestPlanLaySectionDiffMarkerSwapIsAnUpdate(t *testing.T) {
	stored := []laySectionIdentity{{Id: 11, SectionKey: keyA, MarkerId: 7, Plies: 24, Position: 0}}
	payload := []entity.ProductionRunLaySectionInsert{sec(keyA, 99, 24, 0)}

	plan := planLaySectionDiff(stored, payload, keysOf(payload))

	if len(plan.updates) != 1 || plan.updates[0].id != 11 || !plan.updates[0].changed {
		t.Fatalf("a marker swap is an in-place update of the same row, got %+v", plan.updates)
	}
	if len(plan.deletes) != 0 || len(plan.inserts) != 0 {
		t.Fatalf("a marker swap neither deletes nor inserts: %+v", plan)
	}
}

// TestPlanLaySectionDiffNeedsNoParking is the explicit statement of why this diff is SHORTER than
// upsertRunLines. There the four-step order (delete → update-with-parking → insert → un-park) exists
// because run lines carry a SECOND unique key, uniq_prl (run_id, product_id, size_id), so a row
// moving into a slot another row is vacating collides mid-diff. Sections have exactly one unique
// key — uniq_prlays_key (lay_id, section_key) — and the diff matches on precisely that key, so a
// matched row keeps its key by construction and can collide with nothing. Position is not unique
// and never was: two sections may legally share one, which this case also pins.
func TestPlanLaySectionDiffNeedsNoParking(t *testing.T) {
	stored := []laySectionIdentity{
		{Id: 11, SectionKey: keyA, MarkerId: 7, Plies: 24, Position: 0},
		{Id: 12, SectionKey: keyB, MarkerId: 8, Plies: 12, Position: 1},
	}
	// Both sections move onto position 0 — a collision, if position were an identity. It is not.
	payload := []entity.ProductionRunLaySectionInsert{sec(keyA, 7, 24, 0), sec(keyB, 8, 12, 0)}

	plan := planLaySectionDiff(stored, payload, keysOf(payload))

	if len(plan.updates) != 2 {
		t.Fatalf("both rows update in place, got %+v", plan.updates)
	}
	for _, u := range plan.updates {
		if u.id != 11 && u.id != 12 {
			t.Fatalf("unexpected id in the plan: %+v", u)
		}
	}
	if len(plan.deletes) != 0 || len(plan.inserts) != 0 {
		t.Fatalf("no row had to be parked, deleted or recreated: %+v", plan)
	}
}

// TestResolveLaySectionKeysRefusesDuplicates: two payload rows carrying one key would both match the
// same stored row and the diff would silently apply whichever came last. Refused by name.
func TestResolveLaySectionKeysRefusesDuplicates(t *testing.T) {
	_, err := resolveLaySectionKeys([]entity.ProductionRunLaySectionInsert{sec(keyA, 7, 2, 0), sec(keyA, 8, 4, 1)})
	if err == nil {
		t.Fatal("a duplicate section key must be refused")
	}
	var ve *entity.ValidationError
	if !errors.As(err, &ve) || ve.Field != "lay.sections[1].section_key" {
		t.Fatalf("the refusal must name the offending field, got %v", err)
	}
}

// TestResolveLaySectionKeysMintsMissing: an empty key is a create, and the server mints the identity
// so the row is addressable from the very next save.
func TestResolveLaySectionKeysMintsMissing(t *testing.T) {
	keys, err := resolveLaySectionKeys([]entity.ProductionRunLaySectionInsert{sec("", 7, 2, 0), sec(keyB, 8, 4, 1)})
	if err != nil {
		t.Fatalf("minting a missing key must succeed: %v", err)
	}
	if !entity.IsValidProductionLayKey(keys[0]) {
		t.Errorf("minted key %q is not a valid section key", keys[0])
	}
	if keys[1] != keyB {
		t.Errorf("a supplied key must be preserved verbatim, got %q", keys[1])
	}
}
