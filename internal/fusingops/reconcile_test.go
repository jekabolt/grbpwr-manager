package fusingops

import (
	"database/sql"
	"reflect"
	"slices"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// storeRoundTrip is the plan as the store leaves it: created steps get ids, CopyOf is consumed.
func storeRoundTrip(plan Plan) []Operation {
	ops := make([]Operation, len(plan.Operations))
	next := 1000
	for i, op := range plan.Operations {
		if op.ID == 0 {
			next++
			op.ID = next
		}
		op.CopyOf = 0
		ops[i] = op
	}
	return ops
}

func TestReconcileFusingOps(t *testing.T) {
	tests := []struct {
		name       string
		pieces     []Piece
		operations []Operation
		want       Plan
	}{
		{
			name:   "one step per fused piece just before its first use",
			pieces: []Piece{{ID: 11, Fused: true}, {ID: 12, Fused: true}, {ID: 13}},
			operations: []Operation{
				{ID: 1, Kind: KindOther, PieceIDs: []int{12, 13}},
				{ID: 2, Kind: KindOther, PieceIDs: []int{11}},
			},
			want: Plan{Operations: []Operation{
				{Kind: KindAuto, Owner: 12, Draft: true, PieceIDs: []int{12}},
				{ID: 1, Kind: KindOther, PieceIDs: []int{12, 13}},
				{Kind: KindAuto, Owner: 11, Draft: true, PieceIDs: []int{11}},
				{ID: 2, Kind: KindOther, PieceIDs: []int{11}},
			}},
		},
		{
			name:       "pieces no step uses yet go first in piece order",
			pieces:     []Piece{{ID: 12, Fused: true}, {ID: 11, Fused: true}},
			operations: []Operation{{ID: 1, Kind: KindOther}},
			want: Plan{Operations: []Operation{
				{Kind: KindAuto, Owner: 12, Draft: true, PieceIDs: []int{12}},
				{Kind: KindAuto, Owner: 11, Draft: true, PieceIDs: []int{11}},
				{ID: 1, Kind: KindOther},
			}},
		},
		{
			name:   "grouped legacy step: first piece keeps the row, others copy it, all moved before first use",
			pieces: []Piece{{ID: 11, Fused: true}, {ID: 12, Fused: true}, {ID: 13}},
			operations: []Operation{
				{ID: 5, Kind: KindLegacy, PieceIDs: []int{11, 12, 13}},
				{ID: 6, Kind: KindOther, PieceIDs: []int{12}},
				{ID: 7, Kind: KindOther, PieceIDs: []int{11}},
			},
			want: Plan{Operations: []Operation{
				{Kind: KindAuto, Owner: 12, Draft: true, PieceIDs: []int{12}, CopyOf: 5},
				{ID: 6, Kind: KindOther, PieceIDs: []int{12}},
				{ID: 5, Kind: KindAuto, Owner: 11, PieceIDs: []int{11}},
				{ID: 7, Kind: KindOther, PieceIDs: []int{11}},
			}},
		},
		{
			name:   "grouped legacy step with unit inputs stays where it stands",
			pieces: []Piece{{ID: 11, Fused: true}},
			operations: []Operation{
				{ID: 4, Kind: KindOther, PieceIDs: []int{11}},
				{ID: 5, Kind: KindLegacy, PieceIDs: []int{11}, UnitInputs: true},
			},
			want: Plan{Operations: []Operation{
				{ID: 4, Kind: KindOther, PieceIDs: []int{11}},
				{ID: 5, Kind: KindAuto, Owner: 11, PieceIDs: []int{11}, UnitInputs: true},
			}},
		},
		{
			name:   "grouped legacy step left without pieces: draft deleted, edited orphaned",
			pieces: []Piece{{ID: 11}},
			operations: []Operation{
				{ID: 5, Kind: KindLegacy, Draft: true, PieceIDs: []int{11}},
				{ID: 6, Kind: KindLegacy, PieceIDs: []int{11}},
			},
			want: Plan{
				Operations: []Operation{{ID: 6, Kind: KindAuto, Review: ReviewOrphan}},
				DeletedIDs: []int{5},
			},
		},
		{
			name:   "skeleton step is adopted in place, its duplicate draft deleted, an edited duplicate kept",
			pieces: []Piece{{ID: 11, Fused: true}, {ID: 12, Fused: true}},
			operations: []Operation{
				{ID: 1, Kind: KindOther},
				{ID: 2, Kind: KindAdoptable, Draft: true, PieceIDs: []int{11}},
				{ID: 3, Kind: KindAdoptable, Draft: true, PieceIDs: []int{11}},
				{ID: 4, Kind: KindAdoptable, PieceIDs: []int{11}},
				{ID: 5, Kind: KindOther, PieceIDs: []int{11, 12}},
			},
			want: Plan{
				Operations: []Operation{
					{ID: 1, Kind: KindOther},
					{ID: 2, Kind: KindAuto, Owner: 11, Draft: true, PieceIDs: []int{11}},
					{ID: 4, Kind: KindAdoptable, PieceIDs: []int{11}},
					{Kind: KindAuto, Owner: 12, Draft: true, PieceIDs: []int{12}},
					{ID: 5, Kind: KindOther, PieceIDs: []int{11, 12}},
				},
				DeletedIDs: []int{3},
			},
		},
		{
			name:   "an adoptable step holding several pieces is split like a grouped one, in place",
			pieces: []Piece{{ID: 11, Fused: true}, {ID: 12, Fused: true}},
			operations: []Operation{
				{ID: 1, Kind: KindOther},
				{ID: 2, Kind: KindAdoptable, PieceIDs: []int{11, 12}},
				{ID: 3, Kind: KindOther, PieceIDs: []int{11, 12}},
			},
			want: Plan{Operations: []Operation{
				{ID: 1, Kind: KindOther},
				{ID: 2, Kind: KindAuto, Owner: 11, PieceIDs: []int{11}},
				{Kind: KindAuto, Owner: 12, Draft: true, PieceIDs: []int{12}, CopyOf: 2},
				{ID: 3, Kind: KindOther, PieceIDs: []int{11, 12}},
			}},
		},
		{
			name:   "a per-piece step without its piece: draft deleted, edited orphaned, a second claimant orphaned",
			pieces: []Piece{{ID: 11, Fused: true}, {ID: 12}},
			operations: []Operation{
				{ID: 1, Kind: KindAuto, Owner: 11, Draft: true, PieceIDs: []int{11}},
				{ID: 2, Kind: KindAuto, Owner: 11, PieceIDs: []int{11}},
				{ID: 3, Kind: KindAuto, Owner: 12, Draft: true, PieceIDs: []int{12}},
				{ID: 4, Kind: KindAuto, Owner: 0, PieceIDs: []int{}},
			},
			want: Plan{
				Operations: []Operation{
					{ID: 1, Kind: KindAuto, Owner: 11, Draft: true, PieceIDs: []int{11}},
					{ID: 2, Kind: KindAuto, Owner: 11, Review: ReviewOrphan},
					{ID: 4, Kind: KindAuto, Review: ReviewOrphan},
				},
				DeletedIDs: []int{3},
			},
		},
		{
			name:       "an orphan whose piece is fused again reclaims it",
			pieces:     []Piece{{ID: 11, Fused: true}},
			operations: []Operation{{ID: 2, Kind: KindAuto, Owner: 11, Review: ReviewOrphan}},
			want:       Plan{Operations: []Operation{{ID: 2, Kind: KindAuto, Owner: 11, PieceIDs: []int{11}}}},
		},
		{
			name:   "manual steps are never touched",
			pieces: []Piece{{ID: 11, Fused: true}},
			operations: []Operation{
				{ID: 1, Kind: KindManual, PieceIDs: []int{11}, UnitInputs: true},
				{ID: 2, Kind: KindAuto, Owner: 11, Draft: true, PieceIDs: []int{11}},
			},
			want: Plan{Operations: []Operation{
				{ID: 1, Kind: KindManual, PieceIDs: []int{11}, UnitInputs: true},
				{ID: 2, Kind: KindAuto, Owner: 11, Draft: true, PieceIDs: []int{11}},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReconcileFusingOps(tt.pieces, tt.operations)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ReconcileFusingOps() =\n%#v\nwant\n%#v", got, tt.want)
			}
			stored := storeRoundTrip(got)
			again := ReconcileFusingOps(tt.pieces, stored)
			if !reflect.DeepEqual(again.Operations, stored) || len(again.DeletedIDs) != 0 {
				t.Fatalf("second reconcile changed state:\nfirst=%#v\nsecond=%#v", stored, again)
			}
		})
	}
}

func fusingOp(number int32, pieces ...string) entity.TechCardOperation {
	op := entity.TechCardOperation{
		OperationNumber: opNumber(number),
		OperationType:   entity.OpTypeFusing,
		Zone:            entity.ZoneInterlining,
		PressEquipment:  sql.NullString{String: "fusing_press", Valid: true},
	}
	for _, piece := range pieces {
		op.AssemblyInputs = append(op.AssemblyInputs, entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: piece})
		op.InputKeys = append(op.InputKeys, piece)
		op.PieceLineKeys = append(op.PieceLineKeys, piece)
	}
	return op
}

func joinOp(number int32, output string, units []string, pieces ...string) entity.TechCardOperation {
	op := machineOp(number, "join")
	op.InputKeys = append(append([]string(nil), units...), pieces...)
	op.PieceLineKeys = pieces
	op.OutputUnitKey = sql.NullString{String: output, Valid: true}
	return op
}

// assertConverged: the reconciled card needs no further reconcile and a repeat changes nothing.
func assertConverged(t *testing.T, card *entity.TechCardInsert) {
	t.Helper()
	if fusingOpsNeedReconcile(card) {
		t.Fatalf("reconciled card still needs a reconcile: %#v", card.Operations)
	}
	before := append([]entity.TechCardOperation(nil), card.Operations...)
	issues := append([]entity.TechCardIssue(nil), card.Issues...)
	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("repeat ReconcileTechCard() error = %v", err)
	}
	if !reflect.DeepEqual(card.Operations, before) || !reflect.DeepEqual(card.Issues, issues) {
		t.Fatalf("repeat reconcile changed the card:\nbefore=%#v\nafter=%#v", before, card.Operations)
	}
}

func assertPerPieceStep(t *testing.T, op entity.TechCardOperation, piece, line string, bom ...string) {
	t.Helper()
	if op.OperationType != entity.OpTypeFusing || op.AutoFusingPieceKey != autoKey(piece) ||
		op.AutoFusingLineKey.Valid || !reflect.DeepEqual(op.PieceLineKeys, []string{piece}) ||
		!reflect.DeepEqual(pieceInputKeys(&op, nil), []string{piece}) || op.Review.Valid ||
		!slices.Equal(op.BomLineKeys, bom) {
		t.Fatalf("step = %#v, want the automatic step of %s on %q with BOM %v", op, piece, line, bom)
	}
}

func TestWriteModelCreatesOneStepPerFusedPiece(t *testing.T) {
	card := &entity.TechCardInsert{
		AssemblyAware: true,
		BomItems: []entity.TechCardBomItem{
			{LineKey: "INTERLINING-A", Section: entity.BomSectionInterlining},
			{LineKey: "INTERLINING-B", Section: entity.BomSectionInterlining},
		},
		Pieces: []entity.TechCardPiece{
			{LineKey: "FRONT", Name: "front", Fused: true, FusingBomLineKey: "INTERLINING-A"},
			{LineKey: "BACK", Name: "back"},
			{LineKey: "COLLAR", Name: "collar", Fused: true, FusingBomLineKey: "INTERLINING-B"},
			{LineKey: "CUFF", Name: "cuff", Fused: true}, // two INTERLINING lines, no choice: unresolved
		},
		Operations: []entity.TechCardOperation{
			joinOp(10, "SHELL", nil, "FRONT", "BACK"),
			joinOp(20, "COLLARED", []string{"SHELL"}, "COLLAR"),
		},
		Issues: []entity.TechCardIssue{{OperationNumber: opNumber(20), Description: "collar"}},
	}
	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 5 {
		t.Fatalf("operations = %#v, want 3 fusing + 2 joins", card.Operations)
	}
	// FRONT goes before the join that takes it, CUFF (used by no step) up front by piece order,
	// COLLAR before its own join.
	assertPerPieceStep(t, card.Operations[0], "FRONT", "INTERLINING-A", "INTERLINING-A")
	assertPerPieceStep(t, card.Operations[1], "CUFF", "")
	assertPerPieceStep(t, card.Operations[3], "COLLAR", "INTERLINING-B", "INTERLINING-B")
	for i, op := range card.Operations {
		if op.OperationNumber != opNumber(int32((i+1)*10)) {
			t.Fatalf("operation %d number = %#v, want %d", i, op.OperationNumber, (i+1)*10)
		}
		if op.OperationType == entity.OpTypeFusing && (!op.Draft || op.PressEquipment.String != "fusing_press") {
			t.Fatalf("new fusing step = %#v, want a draft with defaults", op)
		}
	}
	if card.Operations[2].OutputUnitKey.String != "SHELL" || card.Operations[4].OutputUnitKey.String != "COLLARED" {
		t.Fatalf("joins moved: %#v", card.Operations)
	}
	if got := card.Issues[0].OperationNumber; got != opNumber(50) {
		t.Fatalf("issue number = %#v, want 50 (follows its join)", got)
	}
	if verr := dto.RevalidateTechCardAssembly(card); verr != nil {
		t.Fatalf("reconciled card failed assembly revalidation: %v", verr)
	}
	assertConverged(t, card)
}

func TestPerPieceIdentityDropsLegacyLineKeyForRollbackSafety(t *testing.T) {
	step := storedAutoFusing(10, "INTERLINING-A", "FRONT")
	// This is the mixed-version shape emitted by 04a8a17 before the rollback guard: both private
	// keys were set. The next reconcile must retain the BOM link but clear the key an old binary owns.
	step.AutoFusingLineKey = autoKey("INTERLINING-A")
	card := fusedCard([]entity.TechCardOperation{step})

	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 1 {
		t.Fatalf("operations = %#v, want one per-piece step", card.Operations)
	}
	assertPerPieceStep(t, card.Operations[0], "FRONT", "INTERLINING-A", "INTERLINING-A")
}

func TestMultiPieceManualFusingIsNeverAdoptedOrSplit(t *testing.T) {
	manual := manualFusing(10, []string{"INTERLINING-A"}, "FRONT", "BACK")
	card := fusedCard([]entity.TechCardOperation{manual})
	card.Pieces[1].Fused = true
	card.Pieces[1].FusingBomLineKey = "INTERLINING-A"

	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 3 {
		t.Fatalf("operations = %#v, want two automatic steps plus the untouched manual step", card.Operations)
	}
	assertPerPieceStep(t, card.Operations[0], "FRONT", "INTERLINING-A", "INTERLINING-A")
	assertPerPieceStep(t, card.Operations[1], "BACK", "INTERLINING-A", "INTERLINING-A")
	got := card.Operations[2]
	if got.AutoFusingPieceKey.Valid || got.AutoFusingLineKey.Valid || got.Draft ||
		!reflect.DeepEqual(got.PieceLineKeys, []string{"FRONT", "BACK"}) {
		t.Fatalf("multi-piece manual step was adopted or split: %#v", got)
	}
}

func TestPostReconcileStepPrecedesTheStepConsumingAProducedUnit(t *testing.T) {
	// FUSEPATCH is only used by a hand-made FUSING step that also takes SHELL, produced by step 10.
	// The old model put its automatic step first; per piece it goes just before step 20.
	card := &entity.TechCardInsert{
		AssemblyAware: true,
		BomItems: []entity.TechCardBomItem{{
			LineKey: "INTERLINING-A", Section: entity.BomSectionInterlining,
		}},
		Pieces: []entity.TechCardPiece{
			{LineKey: "FRONT", Name: "front"},
			{LineKey: "BACK", Name: "back"},
			{LineKey: "FUSEPATCH", Name: "fuse patch", Fused: true, FusingBomLineKey: "INTERLINING-A"},
		},
		Operations: []entity.TechCardOperation{
			joinOp(10, "SHELL", nil, "FRONT", "BACK"),
			{
				OperationNumber: sql.NullInt32{Int32: 20, Valid: true},
				OperationType:   entity.OpTypeFusing, Zone: entity.ZoneInterlining,
				BomLineKeys: []string{"INTERLINING-A"},
				InputKeys:   []string{"SHELL", "FUSEPATCH"}, PieceLineKeys: []string{"FUSEPATCH"},
			},
		},
	}
	if err := dto.RevalidateTechCardAssembly(card); err != nil {
		t.Fatalf("baseline assembly graph should be valid: %v", err)
	}
	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 3 || card.Operations[0].OutputUnitKey.String != "SHELL" ||
		card.Operations[2].AutoFusingPieceKey.Valid {
		t.Fatalf("operations = %#v, want join, automatic FUSEPATCH step, hand-made step", card.Operations)
	}
	assertPerPieceStep(t, card.Operations[1], "FUSEPATCH", "INTERLINING-A", "INTERLINING-A")
	if verr := dto.RevalidateTechCardAssembly(card); verr != nil {
		t.Fatalf("post-reconcile assembly validation failed: %v", verr)
	}
	assertConverged(t, card)
}

func TestWriteModelSplitsGroupedLegacyStep(t *testing.T) {
	grouped := legacyGroupedFusing(10, "INTERLINING-A", "FRONT", "BACK")
	grouped.Draft = false // a person reviewed it
	grouped.Note = sql.NullString{String: "low steam", Valid: true}
	grouped.PressTemperatureC = sql.NullInt32{Int32: 140, Valid: true}
	grouped.PressDwellSec = sql.NullInt32{Int32: 12, Valid: true}
	grouped.BomQuantities = []entity.OperationBomQty{{LineKey: "INTERLINING-A", QtyPerGarment: decimal.NewFromInt(2)}}
	card := fusedCard(
		[]entity.TechCardOperation{grouped, joinOp(20, "SHELL", nil, "FRONT", "BACK")},
		entity.TechCardIssue{OperationNumber: opNumber(10), Description: "bubbling"},
		entity.TechCardIssue{OperationNumber: opNumber(20), Description: "seam"},
	)
	card.AssemblyAware = true
	card.Pieces[1].Fused, card.Pieces[1].FusingBomLineKey = true, "INTERLINING-A"

	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 3 {
		t.Fatalf("operations = %#v, want FRONT, BACK, join", card.Operations)
	}
	front, back := card.Operations[0], card.Operations[1]
	assertPerPieceStep(t, front, "FRONT", "INTERLINING-A", "INTERLINING-A")
	assertPerPieceStep(t, back, "BACK", "INTERLINING-A", "INTERLINING-A")
	// The row stays FRONT's: its number, its note, its review state and quantities.
	if front.OperationNumber != opNumber(10) || front.Draft || front.Note.String != "low steam" ||
		len(front.BomQuantities) != 1 || front.PressTemperatureC.Int32 != 140 {
		t.Fatalf("FRONT step = %#v, want the grouped row kept", front)
	}
	// BACK's step is a new draft with the grouped step's press settings, not its content.
	if !back.Draft || back.Note.Valid || len(back.BomQuantities) != 0 || back.PressTemperatureC.Int32 != 140 ||
		back.PressDwellSec.Int32 != 12 || back.PressEquipment.String != "fusing_press" {
		t.Fatalf("BACK step = %#v, want a draft copying the press settings", back)
	}
	if got := card.Issues[0].OperationNumber; got != opNumber(10) {
		t.Fatalf("grouped step issue = %#v, want 10 (stays with the row)", got)
	}
	if got := card.Issues[1].OperationNumber; got != opNumber(30) {
		t.Fatalf("join issue = %#v, want 30", got)
	}
	if verr := dto.RevalidateTechCardAssembly(card); verr != nil {
		t.Fatalf("split card failed assembly revalidation: %v", verr)
	}
	assertConverged(t, card)
}

func TestWriteModelAdoptsSkeletonStepAndDropsItsDuplicate(t *testing.T) {
	skeleton := fusingOp(20, "FRONT")
	skeleton.Draft = true
	skeleton.BomLineKeys = []string{"INTERLINING-A"}
	duplicate := fusingOp(30, "FRONT")
	duplicate.Draft = true
	card := fusedCard(
		[]entity.TechCardOperation{machineOp(10, "mark"), skeleton, duplicate, joinOp(40, "SHELL", nil, "FRONT", "BACK")},
		entity.TechCardIssue{OperationNumber: opNumber(30), Description: "on the duplicate"},
	)
	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 3 {
		t.Fatalf("operations = %#v, want mark, adopted step, join", card.Operations)
	}
	assertPerPieceStep(t, card.Operations[1], "FRONT", "INTERLINING-A", "INTERLINING-A")
	if !card.Operations[1].Draft || card.Operations[1].OperationNumber != opNumber(20) {
		t.Fatalf("adopted step = %#v, want the skeleton draft kept at 20", card.Operations[1])
	}
	if card.Issues[0].OperationNumber.Valid {
		t.Fatalf("issue on the deleted duplicate = %#v, want cleared", card.Issues[0])
	}
	assertConverged(t, card)
}

func TestWriteModelResyncsMaterialWhenThePieceChangesInterlining(t *testing.T) {
	step := storedAutoFusing(10, "INTERLINING-A", "FRONT")
	step.Draft = false
	step.PieceIds, step.BomIds = nil, nil
	step.BomLineKeys = []string{"INTERLINING-A", "TAPE"}
	step.BomQuantities = []entity.OperationBomQty{
		{LineKey: "INTERLINING-A", QtyPerGarment: decimal.NewFromInt(1)},
		{LineKey: "TAPE", QtyPerGarment: decimal.NewFromInt(3)},
	}
	newCard := func() *entity.TechCardInsert {
		card := fusedCard([]entity.TechCardOperation{step, joinOp(20, "SHELL", nil, "FRONT", "BACK")})
		card.BomItems = append(card.BomItems,
			entity.TechCardBomItem{LineKey: "INTERLINING-B", Section: entity.BomSectionInterlining},
			entity.TechCardBomItem{LineKey: "TAPE", Section: entity.BomSectionTrim})
		return card
	}
	tapeOnly := []entity.OperationBomQty{{LineKey: "TAPE", QtyPerGarment: decimal.NewFromInt(3)}}

	t.Run("another line selected", func(t *testing.T) {
		card := newCard()
		card.Pieces[0].FusingBomLineKey = "INTERLINING-B"
		if err := ReconcileTechCard(card); err != nil {
			t.Fatalf("ReconcileTechCard() error = %v", err)
		}
		assertPerPieceStep(t, card.Operations[0], "FRONT", "INTERLINING-B", "INTERLINING-B", "TAPE")
		if card.Operations[0].Draft || !reflect.DeepEqual(card.Operations[0].BomQuantities, tapeOnly) {
			t.Fatalf("step = %#v, want edited, the old line's quantity dropped", card.Operations[0])
		}
		assertConverged(t, card)
	})
	t.Run("selection cleared with two lines: unresolved", func(t *testing.T) {
		card := newCard()
		card.Pieces[0].FusingBomLineKey = ""
		if err := ReconcileTechCard(card); err != nil {
			t.Fatalf("ReconcileTechCard() error = %v", err)
		}
		assertPerPieceStep(t, card.Operations[0], "FRONT", "", "TAPE")
		assertConverged(t, card)
	})
	t.Run("selected line deleted, the other one is the only line", func(t *testing.T) {
		card := newCard()
		card.BomItems = card.BomItems[1:]
		card.Pieces[0].FusingBomLineKey = ""
		if err := ReconcileTechCard(card); err != nil {
			t.Fatalf("ReconcileTechCard() error = %v", err)
		}
		assertPerPieceStep(t, card.Operations[0], "FRONT", "INTERLINING-B", "INTERLINING-B", "TAPE")
		assertConverged(t, card)
	})
}

func TestUnfusedPieceOrphansEditedStepAndRefusingReclaimsIt(t *testing.T) {
	step := storedAutoFusing(10, "INTERLINING-A", "FRONT")
	step.Draft = false
	step.PieceIds, step.BomIds = nil, nil
	card := fusedCard([]entity.TechCardOperation{step, joinOp(20, "SHELL", nil, "FRONT", "BACK")})
	card.Pieces[0].Fused = false
	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	orphan := card.Operations[0]
	if len(card.Operations) != 2 || orphan.Review.String != ReviewOrphan || orphan.AutoFusingPieceKey != autoKey("FRONT") ||
		len(pieceInputKeys(&orphan, nil)) != 0 || !reflect.DeepEqual(orphan.BomLineKeys, []string{"INTERLINING-A"}) {
		t.Fatalf("operations = %#v, want an orphan keeping its key and live link, without the piece", card.Operations)
	}
	assertConverged(t, card)

	card.Pieces[0].Fused = true
	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 2 || card.Operations[0].Draft {
		t.Fatalf("operations = %#v, want the orphan reclaimed, no new step", card.Operations)
	}
	assertPerPieceStep(t, card.Operations[0], "FRONT", "INTERLINING-A", "INTERLINING-A")
	assertConverged(t, card)
}

func TestDeletedInterliningKeepsEditedGroupedStepAsLinklessOrphan(t *testing.T) {
	grouped := legacyGroupedFusing(10, "DELETED-INTERLINING")
	grouped.Draft = false
	grouped.PieceIds, grouped.BomIds = nil, nil
	card := &entity.TechCardInsert{Operations: []entity.TechCardOperation{grouped}}
	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 1 || card.Operations[0].Review.String != ReviewOrphan ||
		len(card.Operations[0].BomLineKeys) != 0 || card.Operations[0].AutoFusingLineKey != autoKey("DELETED-INTERLINING") {
		t.Fatalf("deleted-line operation was not retained as a linkless orphan: %#v", card.Operations)
	}

	// A human may acknowledge the review. The private snapshot, not that clearable mark, keeps the
	// step from being adopted as a newly fused piece's step.
	card.Operations[0].Review = sql.NullString{}
	card.Pieces = []entity.TechCardPiece{{LineKey: "FRONT", Fused: true}}
	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("second ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 2 {
		t.Fatalf("operations = %#v, want a new step for FRONT plus the orphan", card.Operations)
	}
	assertPerPieceStep(t, card.Operations[0], "FRONT", "")
	if card.Operations[1].Review.String != ReviewOrphan || card.Operations[1].Draft {
		t.Fatalf("edited orphan was re-adopted: %#v", card.Operations[1])
	}
	assertConverged(t, card)
}

func TestDeletingDraftFusingOperationNullsIssueBeforeNumberReuse(t *testing.T) {
	gone := fusingOp(20)
	gone.Draft = true
	gone.AutoFusingPieceKey = autoKey("DELETED-PIECE")
	gone.AutoFusingLineKey = autoKey("")
	card := &entity.TechCardInsert{
		Operations: []entity.TechCardOperation{
			{OperationNumber: sql.NullInt32{Int32: 10, Valid: true}, OperationType: entity.OpTypeMachine},
			gone,
			{OperationNumber: sql.NullInt32{Int32: 30, Valid: true}, OperationType: entity.OpTypeMachine},
		},
		Issues: []entity.TechCardIssue{
			{OperationNumber: sql.NullInt32{Int32: 20, Valid: true}, Description: "belongs to deleted fusing"},
			{OperationNumber: sql.NullInt32{Int32: 30, Valid: true}, Description: "belongs to retained B"},
		},
	}
	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 2 || card.Operations[1].OperationNumber.Int32 != 20 {
		t.Fatalf("retained operations were not compacted as expected: %#v", card.Operations)
	}
	if card.Issues[0].OperationNumber.Valid {
		t.Fatalf("deleted operation issue was retargeted instead of cleared: %#v", card.Issues[0])
	}
	if got := card.Issues[1].OperationNumber; !got.Valid || got.Int32 != 20 {
		t.Fatalf("retained operation issue = %#v, want 20", got)
	}
}

func TestUnrelatedUpdateDoesNotTouchHandMadeFusingStep(t *testing.T) {
	handMade := fusingOp(20, "FRONT")
	stored := &entity.TechCardInsert{
		Name:       "before",
		Pieces:     []entity.TechCardPiece{{LineKey: "FRONT"}},
		Operations: []entity.TechCardOperation{{OperationType: entity.OpTypeMachine}, handMade},
	}
	incoming := *stored
	incoming.Name = "unrelated edit"
	incoming.Pieces = append([]entity.TechCardPiece(nil), stored.Pieces...)
	incoming.Operations = append([]entity.TechCardOperation(nil), stored.Operations...)
	before := append([]entity.TechCardOperation(nil), incoming.Operations...)

	outcome, err := ReconcileTechCardUpdate(stored, &incoming)
	if err != nil {
		t.Fatalf("ReconcileTechCardUpdate() error = %v", err)
	}
	if outcome.Reconcile || outcome.Carried {
		t.Fatalf("unrelated save changed the operation list: %+v", outcome)
	}
	if !reflect.DeepEqual(incoming.Operations, before) {
		t.Fatalf("unrelated save mutated a hand-made FUSING step: before=%#v after=%#v", before, incoming.Operations)
	}
}

// TestPressCopyColumnsMatchCopyPressSettings pins the store's INSERT … SELECT column list to the
// write model's field copy: both must hand a split-off step exactly the same settings.
func TestPressCopyColumnsMatchCopyPressSettings(t *testing.T) {
	var src entity.TechCardOperation
	fillNonZero(reflect.ValueOf(&src).Elem())
	var dst entity.TechCardOperation
	CopyPressSettings(&dst, &src)

	want := make(map[string]bool, len(PressCopyColumns))
	for _, column := range PressCopyColumns {
		want[column] = true
	}
	typ := reflect.TypeOf(dst)
	seen := make(map[string]bool, len(PressCopyColumns))
	for i := 0; i < typ.NumField(); i++ {
		column := typ.Field(i).Tag.Get("db")
		copied := !reflect.ValueOf(dst).Field(i).IsZero()
		if copied != want[column] {
			t.Errorf("field %s (db %q): copied = %v, listed in PressCopyColumns = %v", typ.Field(i).Name, column, copied, want[column])
		}
		if want[column] {
			seen[column] = true
		}
	}
	for _, column := range PressCopyColumns {
		if !seen[column] {
			t.Errorf("PressCopyColumns lists %q, which is no TechCardOperation column", column)
		}
	}
}

func fillNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				fillNonZero(v.Field(i))
			}
		}
	}
}
