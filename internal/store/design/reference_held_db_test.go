package design_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ «REMOVE FROM PROMPT» — label_state held (109 §4) ═════════════════════════════════════════════
//
// A held picture keeps its label and its place on the board and rides no run. The rules that can go
// red one by one: a hold needs a settled label; a model's detail goes with its last travelling photo
// (a person's slot never); putting back a model photo whose slot went re-reads it; a person's edit and
// a model relabel both lift a hold.

func heldRow(t *testing.T, raw *sql.DB, card, media int) (role, state, source string, slot sql.NullInt64, labelled sql.NullTime, attempts int) {
	t.Helper()
	require.NoError(t, raw.QueryRow(`
		SELECT role, label_state, label_source, detail_slot_id, labelled_at, label_attempts
		FROM design_reference WHERE tech_card_id = ? AND media_id = ?`, card, media).
		Scan(&role, &state, &source, &slot, &labelled, &attempts))
	return
}

func slotExists(t *testing.T, raw *sql.DB, id int) bool {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM design_bench_slot WHERE id = ?`, id).Scan(&n))
	return n > 0
}

// modelDetail — the labeller's path for a detail photo: a pending model row, then the model's answer
// naming the part (minting a made_by_model slot, or joining one by name).
func modelDetail(t *testing.T, rep dependency.Repository, card, media int, name string) *entity.DesignReference {
	t.Helper()
	ctx := context.Background()
	claimed, err := rep.Design().BeginBoardLabel(ctx, entity.DesignBoardLabelBegin{
		TechCardId: card, MediaId: media, StaleBefore: time.Now().Add(-time.Minute), Actor: "probe",
	})
	require.NoError(t, err)
	require.True(t, claimed)
	ref, err := rep.Design().FinishBoardLabel(ctx, entity.DesignBoardLabel{
		TechCardId: card, MediaId: media, Role: entity.DesignViewDetail, NewDetailName: name,
		Source: entity.DesignLabelSourceModelStrong, State: entity.DesignLabelStateOk,
		ProposedPurpose: "detail", LabelModel: "probe",
	})
	require.NoError(t, err)
	require.NotNil(t, ref)
	require.True(t, ref.DetailSlotId.Valid)
	return ref
}

func TestDesignDBReferenceHeldTakesOutAndPutsBack(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)

	_, err := rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: media, Role: entity.DesignViewFront, Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)

	ref, err := rep.Design().SetReferenceHeld(ctx, entity.DesignReferenceHold{TechCardId: card, MediaId: media, Held: true, Actor: "probe"})
	require.NoError(t, err)
	require.Equal(t, entity.DesignLabelStateHeld, ref.LabelState)
	require.Equal(t, entity.DesignViewFront, ref.Role, "the label stays")
	require.Equal(t, entity.DesignLabelSourceHuman, ref.LabelSource, "the source never changes")
	require.False(t, entity.DesignReferenceTravels(*ref), "a held picture rides no run")

	// idempotent
	ref, err = rep.Design().SetReferenceHeld(ctx, entity.DesignReferenceHold{TechCardId: card, MediaId: media, Held: true, Actor: "probe"})
	require.NoError(t, err)
	require.Equal(t, entity.DesignLabelStateHeld, ref.LabelState)

	ref, err = rep.Design().SetReferenceHeld(ctx, entity.DesignReferenceHold{TechCardId: card, MediaId: media, Held: false, Actor: "probe"})
	require.NoError(t, err)
	require.Equal(t, entity.DesignLabelStateOk, ref.LabelState)
	require.True(t, entity.DesignReferenceTravels(*ref))

	// put back of a row that is not held: unchanged
	ref, err = rep.Design().SetReferenceHeld(ctx, entity.DesignReferenceHold{TechCardId: card, MediaId: media, Held: false, Actor: "probe"})
	require.NoError(t, err)
	require.Equal(t, entity.DesignLabelStateOk, ref.LabelState)

	// A PERSON'S LABEL EDIT LIFTS A HOLD: touched it — wants it to ride.
	_, err = rep.Design().SetReferenceHeld(ctx, entity.DesignReferenceHold{TechCardId: card, MediaId: media, Held: true, Actor: "probe"})
	require.NoError(t, err)
	ref, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: media, Role: entity.DesignViewBack, Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignLabelStateOk, ref.LabelState)
}

func TestDesignDBReferenceHeldRefusesWhatIsNotInThePrompt(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	none, noView, pending := probeMedia(t, raw), probeMedia(t, raw), probeMedia(t, raw)

	hold := func(media int) error {
		_, err := rep.Design().SetReferenceHeld(ctx, entity.DesignReferenceHold{TechCardId: card, MediaId: media, Held: true, Actor: "probe"})
		return err
	}
	require.ErrorIs(t, hold(none), entity.ErrDesignNothingToHold, "no row")

	_, err := rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{TechCardId: card, MediaId: noView, Role: "", Actor: "probe"})
	require.NoError(t, err)
	require.ErrorIs(t, hold(noView), entity.ErrDesignNothingToHold, "a person's «no view»")

	claimed, err := rep.Design().BeginBoardLabel(ctx, entity.DesignBoardLabelBegin{
		TechCardId: card, MediaId: pending, StaleBefore: time.Now().Add(-time.Minute), Actor: "probe",
	})
	require.NoError(t, err)
	require.True(t, claimed)
	require.ErrorIs(t, hold(pending), entity.ErrDesignNothingToHold, "a label still being read")
	_, state, _, _, _, _ := heldRow(t, raw, card, pending)
	require.Equal(t, entity.DesignLabelStatePending, state, "a refusal writes nothing")

	// put back with no row: nothing, no error
	ref, err := rep.Design().SetReferenceHeld(ctx, entity.DesignReferenceHold{TechCardId: card, MediaId: none, Held: false, Actor: "probe"})
	require.NoError(t, err)
	require.Nil(t, ref)
}

// Q3: the model's detail goes with its last TRAVELLING photo; putting a photo back re-reads it.
func TestDesignDBHeldModelDetailGoesWithItsLastPhotoAndIsReadAgain(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	a, b := probeMedia(t, raw), probeMedia(t, raw)

	ra := modelDetail(t, rep, card, a, "cuff vent")
	rb := modelDetail(t, rep, card, b, "Cuff Vent")
	require.Equal(t, ra.DetailSlotId, rb.DetailSlotId, "same name joins, not «(2)»")
	slot := int(ra.DetailSlotId.Int32)

	hold := func(media int, held bool) *entity.DesignReference {
		ref, err := rep.Design().SetReferenceHeld(ctx, entity.DesignReferenceHold{TechCardId: card, MediaId: media, Held: held, Actor: "probe"})
		require.NoError(t, err)
		return ref
	}
	hold(a, true)
	require.True(t, slotExists(t, raw, slot), "b still asks for the detail")
	hold(b, true)
	require.False(t, slotExists(t, raw, slot), "every photo held: the model's detail goes")
	_, state, _, s, _, _ := heldRow(t, raw, card, a)
	require.Equal(t, entity.DesignLabelStateHeld, state)
	require.False(t, s.Valid, "FK SET NULL")

	ref := hold(a, false)
	require.Equal(t, entity.DesignLabelStatePending, ref.LabelState, "the slot is gone: read again")
	require.Equal(t, "", ref.Role)
	require.True(t, entity.IsDesignLabelByModel(ref.LabelSource))
	_, _, _, _, labelled, attempts := heldRow(t, raw, card, a)
	require.False(t, labelled.Valid, "due at once")
	require.Equal(t, 0, attempts)
	claimed, err := rep.Design().BeginBoardLabel(ctx, entity.DesignBoardLabelBegin{
		TechCardId: card, MediaId: a, StaleBefore: time.Now().Add(-time.Minute), Actor: "probe",
	})
	require.NoError(t, err)
	require.True(t, claimed, "the sync claims the put-back photo")
	again, err := rep.Design().FinishBoardLabel(ctx, entity.DesignBoardLabel{
		TechCardId: card, MediaId: a, Role: entity.DesignViewDetail, NewDetailName: "cuff vent",
		Source: entity.DesignLabelSourceModelStrong, State: entity.DesignLabelStateOk, LabelModel: "probe",
	})
	require.NoError(t, err)
	require.True(t, again.DetailSlotId.Valid, "minted anew")
	_, state, _, _, _, _ = heldRow(t, raw, card, b)
	require.Equal(t, entity.DesignLabelStateHeld, state, "b stays held")
}

// A slot a person named never goes; a person's photo put back without a slot stays the person's.
func TestDesignDBHeldNeverDropsAPersonsSlotOrHandsAPersonsRowToAModel(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	mine, theirs, joined := probeMedia(t, raw), probeMedia(t, raw), probeMedia(t, raw)

	human, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewDetail},
		NewDetailName: "pocket", Actor: "probe",
	})
	require.NoError(t, err)
	_, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: mine, Role: entity.DesignViewDetail, DetailSlotId: human.Id, Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)
	_, err = rep.Design().SetReferenceHeld(ctx, entity.DesignReferenceHold{TechCardId: card, MediaId: mine, Held: true, Actor: "probe"})
	require.NoError(t, err)
	require.True(t, slotExists(t, raw, human.Id), "a person's slot stays")

	// a person's photo on a MODEL's slot: hold everything, the slot goes, put the person's back
	rm := modelDetail(t, rep, card, theirs, "strap")
	modelSlot := int(rm.DetailSlotId.Int32)
	_, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: joined, Role: entity.DesignViewDetail, DetailSlotId: modelSlot, Ordinal: 2, Actor: "probe",
	})
	require.NoError(t, err)
	for _, m := range []int{theirs, joined} {
		_, err = rep.Design().SetReferenceHeld(ctx, entity.DesignReferenceHold{TechCardId: card, MediaId: m, Held: true, Actor: "probe"})
		require.NoError(t, err)
	}
	require.False(t, slotExists(t, raw, modelSlot))
	ref, err := rep.Design().SetReferenceHeld(ctx, entity.DesignReferenceHold{TechCardId: card, MediaId: joined, Held: false, Actor: "probe"})
	require.NoError(t, err)
	require.Equal(t, entity.DesignLabelStateOk, ref.LabelState, "a person's row is never handed to a model")
	require.Equal(t, entity.DesignLabelSourceHuman, ref.LabelSource)
	require.Equal(t, entity.DesignViewDetail, ref.Role)
	require.False(t, ref.DetailSlotId.Valid, "the client asks «detail ?»")
}

// A model relabel (the purpose changed under the model's label) is the stronger gesture: it lifts the hold.
func TestDesignDBModelRelabelLiftsAHold(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	m := probeMedia(t, raw)
	modelDetail(t, rep, card, m, "collar")
	_, err := rep.Design().SetReferenceHeld(ctx, entity.DesignReferenceHold{TechCardId: card, MediaId: m, Held: true, Actor: "probe"})
	require.NoError(t, err)
	claimed, err := rep.Design().BeginBoardLabel(ctx, entity.DesignBoardLabelBegin{
		TechCardId: card, MediaId: m, Relabel: true, StaleBefore: time.Now().Add(-time.Minute), Actor: "probe",
	})
	require.NoError(t, err)
	require.True(t, claimed)
	_, state, _, _, _, _ := heldRow(t, raw, card, m)
	require.Equal(t, entity.DesignLabelStatePending, state)
}
