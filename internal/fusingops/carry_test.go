package fusingops

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func opNumber(n int32) sql.NullInt32 { return sql.NullInt32{Int32: n, Valid: true} }

func autoKey(key string) sql.NullString { return sql.NullString{String: key, Valid: true} }

// storedAutoFusing is a draft per-piece automatic FUSING step exactly as the read path returns it.
func storedAutoFusing(number int32, key, piece string) entity.TechCardOperation {
	op := legacyGroupedFusing(number, key, piece)
	op.AutoFusingPieceKey = autoKey(piece)
	op.AutoFusingLineKey = sql.NullString{}
	return op
}

// legacyGroupedFusing is a draft grouped automatic step of the one-per-line model (line key, no
// piece key) as the read path returns it.
func legacyGroupedFusing(number int32, key string, pieces ...string) entity.TechCardOperation {
	op := entity.TechCardOperation{
		OperationNumber:   opNumber(number),
		OperationType:     entity.OpTypeFusing,
		Zone:              entity.ZoneInterlining,
		PressEquipment:    sql.NullString{String: "fusing_press", Valid: true},
		Draft:             true,
		AutoFusingLineKey: autoKey(key),
		PieceIds:          make([]int, len(pieces)),
	}
	if key != "" {
		op.BomLineKeys = []string{key}
		op.BomIds = []int{77}
	}
	for _, piece := range pieces {
		op.AssemblyInputs = append(op.AssemblyInputs, entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: piece})
		op.InputKeys = append(op.InputKeys, piece)
		op.PieceLineKeys = append(op.PieceLineKeys, piece)
	}
	return op
}

func machineOp(number int32, note string) entity.TechCardOperation {
	return entity.TechCardOperation{
		OperationNumber: opNumber(number),
		OperationType:   entity.OpTypeMachine,
		Zone:            entity.ZoneOuter,
		Note:            sql.NullString{String: note, Valid: true},
	}
}

// fusedCard is one INTERLINING line with FRONT fused to it.
func fusedCard(ops []entity.TechCardOperation, issues ...entity.TechCardIssue) *entity.TechCardInsert {
	return &entity.TechCardInsert{
		Name: "card",
		BomItems: []entity.TechCardBomItem{
			{LineKey: "INTERLINING-A", Section: entity.BomSectionInterlining},
		},
		Pieces: []entity.TechCardPiece{
			{LineKey: "FRONT", Name: "front", Fused: true, FusingBomLineKey: "INTERLINING-A"},
			{LineKey: "BACK", Name: "back"},
		},
		Operations: ops,
		Issues:     issues,
	}
}

// staleCopy is the payload of a client whose form never reloaded its step list after the server
// inserted the automatic steps: same pieces/BOM, its own (older) operations and issues.
func staleCopy(stored *entity.TechCardInsert, ops []entity.TechCardOperation, issues ...entity.TechCardIssue) *entity.TechCardInsert {
	incoming := *stored
	incoming.Name = "unrelated edit"
	incoming.BomItems = append([]entity.TechCardBomItem(nil), stored.BomItems...)
	incoming.Pieces = append([]entity.TechCardPiece(nil), stored.Pieces...)
	incoming.Operations = ops
	incoming.Issues = issues
	return &incoming
}

func TestStaleSaveWithoutAutoOpsCarriesThemBack(t *testing.T) {
	stored := fusedCard(
		[]entity.TechCardOperation{storedAutoFusing(10, "INTERLINING-A", "FRONT"), machineOp(20, "join")},
		entity.TechCardIssue{OperationNumber: opNumber(20), Description: "seam"},
	)
	incoming := staleCopy(stored,
		[]entity.TechCardOperation{machineOp(10, "join")},
		entity.TechCardIssue{OperationNumber: opNumber(10), Description: "seam"},
	)

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil {
		t.Fatalf("ReconcileTechCardUpdate() error = %v", err)
	}
	if outcome.Reconcile || !outcome.Carried {
		t.Fatalf("outcome = %+v, want carried without reconcile", outcome)
	}
	if !incoming.AutoFusingKeysResolved {
		t.Fatal("store number fallback was not disabled after the identity match")
	}
	if len(incoming.Operations) != 2 {
		t.Fatalf("operations = %#v, want the carried fusing step plus the machine step", incoming.Operations)
	}
	fusing := incoming.Operations[0]
	if fusing.OperationType != entity.OpTypeFusing || !fusing.Draft || fusing.OperationNumber != opNumber(10) ||
		fusing.AutoFusingLineKey.Valid || fusing.AutoFusingPieceKey != autoKey("FRONT") ||
		!reflect.DeepEqual(fusing.BomLineKeys, []string{"INTERLINING-A"}) ||
		!reflect.DeepEqual(fusing.PieceLineKeys, []string{"FRONT"}) ||
		!reflect.DeepEqual(fusing.InputKeys, []string{"FRONT"}) ||
		fusing.PieceIds != nil || fusing.BomIds != nil {
		t.Fatalf("carried fusing step = %#v", fusing)
	}
	if got := incoming.Operations[1]; got.OperationType != entity.OpTypeMachine || got.OperationNumber != opNumber(20) {
		t.Fatalf("machine step = %#v, want number 20", got)
	}
	if got := incoming.Issues[0].OperationNumber; got != opNumber(20) {
		t.Fatalf("issue operation number = %#v, want 20 (follows its machine step)", got)
	}
	// The API re-runs assembly validation after a carry; the carried step must pass it as is.
	incoming.AssemblyAware = true
	if verr := dto.RevalidateTechCardAssembly(incoming); verr != nil {
		t.Fatalf("carried card failed assembly revalidation: %v", verr)
	}
	// The stored card must not be aliased by the carried copy.
	if stored.Operations[0].PieceIds == nil || stored.Operations[0].BomIds == nil {
		t.Fatal("carry mutated the stored operation")
	}

	// Idempotent: the corrected payload saved again carries nothing and changes nothing.
	again := staleCopy(stored, append([]entity.TechCardOperation(nil), incoming.Operations...), incoming.Issues...)
	before := append([]entity.TechCardOperation(nil), again.Operations...)
	outcome, err = ReconcileTechCardUpdate(stored, again)
	if err != nil || outcome.Carried || outcome.Reconcile {
		t.Fatalf("second save outcome = %+v err = %v, want no change", outcome, err)
	}
	if !reflect.DeepEqual(again.Operations, before) {
		t.Fatalf("second save changed operations: before=%#v after=%#v", before, again.Operations)
	}
}

func TestStaleSaveCarriesUnresolvedAutoOp(t *testing.T) {
	stored := fusedCard([]entity.TechCardOperation{storedAutoFusing(10, "", "FRONT"), machineOp(20, "join")})
	// Two INTERLINING lines and no explicit choice: FRONT belongs to the shared unresolved group.
	stored.BomItems = append(stored.BomItems, entity.TechCardBomItem{LineKey: "INTERLINING-B", Section: entity.BomSectionInterlining})
	stored.Pieces[0].FusingBomLineKey = ""
	incoming := staleCopy(stored, []entity.TechCardOperation{machineOp(10, "join")})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || !outcome.Carried || outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want carried", outcome, err)
	}
	if len(incoming.Operations) != 2 || incoming.Operations[0].AutoFusingLineKey.Valid ||
		len(incoming.Operations[0].BomLineKeys) != 0 || !incoming.Operations[0].Draft {
		t.Fatalf("operations = %#v, want the unresolved draft step first", incoming.Operations)
	}
}

func TestStaleSaveDoesNotResurrectDeletedEditedAutoOp(t *testing.T) {
	edited := storedAutoFusing(10, "INTERLINING-A", "FRONT")
	edited.Draft = false // a person reviewed this step, then a person deleted it
	stored := fusedCard([]entity.TechCardOperation{edited, machineOp(20, "join")})
	incoming := staleCopy(stored, []entity.TechCardOperation{machineOp(10, "join")})
	before := append([]entity.TechCardOperation(nil), incoming.Operations...)

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || outcome.Carried || outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want no change", outcome, err)
	}
	if !reflect.DeepEqual(incoming.Operations, before) {
		t.Fatalf("deleted edited step came back: before=%#v after=%#v", before, incoming.Operations)
	}
}

func TestStaleSaveLeavesLegacyFusingOpUntouched(t *testing.T) {
	legacy := entity.TechCardOperation{
		OperationNumber: opNumber(10),
		OperationType:   entity.OpTypeFusing,
		Zone:            entity.ZoneInterlining,
		BomLineKeys:     []string{"THREAD", "TAPE"},
		AssemblyInputs:  []entity.OperationInput{{Kind: entity.AssemblyInputPiece, Key: "BACK"}},
		InputKeys:       []string{"BACK"},
		PieceLineKeys:   []string{"BACK"},
		Note:            sql.NullString{String: "hand-written", Valid: true},
	}
	stored := fusedCard([]entity.TechCardOperation{legacy, storedAutoFusing(20, "INTERLINING-A", "FRONT"), machineOp(30, "join")})
	stored.BomItems = append(stored.BomItems,
		entity.TechCardBomItem{LineKey: "THREAD"}, entity.TechCardBomItem{LineKey: "TAPE"})
	incoming := staleCopy(stored, []entity.TechCardOperation{legacy, machineOp(20, "join")})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || !outcome.Carried || outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want carried", outcome, err)
	}
	if len(incoming.Operations) != 3 {
		t.Fatalf("operations = %#v, want legacy, carried auto, machine", incoming.Operations)
	}
	if !reflect.DeepEqual(incoming.Operations[0], legacy) {
		t.Fatalf("legacy step was touched: want=%#v got=%#v", legacy, incoming.Operations[0])
	}
	if got := incoming.Operations[1]; got.AutoFusingLineKey.Valid || got.AutoFusingPieceKey != autoKey("FRONT") ||
		got.OperationNumber != opNumber(20) {
		t.Fatalf("carried step = %#v, want auto FRONT at 20 (its stored place)", got)
	}
	if got := incoming.Operations[2].OperationNumber; got != opNumber(30) {
		t.Fatalf("machine number = %#v, want 30", got)
	}
}

func TestLegacyStepOnAutoOpNumberDoesNotInheritItsIdentity(t *testing.T) {
	// The stale list puts a legacy FUSING step on the number the stored draft automatic step holds.
	// Number matching would adopt the legacy step as the automatic one and drop the real one.
	legacy := entity.TechCardOperation{
		OperationNumber: opNumber(10),
		OperationType:   entity.OpTypeFusing,
		Zone:            entity.ZoneInterlining,
		PieceLineKeys:   []string{"BACK"},
	}
	stored := fusedCard([]entity.TechCardOperation{storedAutoFusing(10, "INTERLINING-A", "FRONT"), machineOp(20, "join")})
	incoming := staleCopy(stored, []entity.TechCardOperation{legacy, machineOp(20, "join")})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || !outcome.Carried {
		t.Fatalf("outcome = %+v err = %v, want carried", outcome, err)
	}
	if len(incoming.Operations) != 3 || incoming.Operations[0].AutoFusingPieceKey != autoKey("FRONT") ||
		incoming.Operations[0].AutoFusingLineKey.Valid {
		t.Fatalf("operations = %#v, want the automatic step carried first", incoming.Operations)
	}
	got := incoming.Operations[1]
	if got.AutoFusingLineKey.Valid || got.Review.Valid || got.Draft || !reflect.DeepEqual(got.PieceLineKeys, []string{"BACK"}) {
		t.Fatalf("legacy step gained automatic identity or changed: %#v", got)
	}
}

func TestRenumberedAutoOpIsNotMissing(t *testing.T) {
	stored := fusedCard(
		[]entity.TechCardOperation{storedAutoFusing(10, "INTERLINING-A", "FRONT"), machineOp(20, "join")},
	)
	// An up-to-date client moved the automatic step after the machine step and renumbered both.
	echo := wireEcho(storedAutoFusing(20, "INTERLINING-A", "FRONT"), 20) // keys never on the wire
	incoming := staleCopy(stored, []entity.TechCardOperation{machineOp(10, "join"), echo})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || outcome.Carried || outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want no carry", outcome, err)
	}
	if len(incoming.Operations) != 2 {
		t.Fatalf("renumbered step was duplicated: %#v", incoming.Operations)
	}
	if got := incoming.Operations[1]; got.AutoFusingLineKey.Valid ||
		got.AutoFusingPieceKey != autoKey("FRONT") || got.OperationNumber != opNumber(20) {
		t.Fatalf("renumbered step = %#v, want identity FRONT kept at 20", got)
	}
}

func TestStaleSaveWithFusingChangeRecreatesMissingAutoOp(t *testing.T) {
	stored := fusedCard(
		[]entity.TechCardOperation{storedAutoFusing(10, "INTERLINING-A", "FRONT"), machineOp(20, "join")},
		entity.TechCardIssue{OperationNumber: opNumber(20), Description: "seam"},
	)
	incoming := staleCopy(stored,
		[]entity.TechCardOperation{machineOp(10, "join")},
		entity.TechCardIssue{OperationNumber: opNumber(10), Description: "seam"},
	)
	incoming.Pieces[1].Fused = true // BACK newly fused: this save does change fusing state

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil {
		t.Fatalf("ReconcileTechCardUpdate() error = %v", err)
	}
	if !outcome.Reconcile {
		t.Fatalf("outcome = %+v, want reconcile", outcome)
	}
	if len(incoming.Operations) != 3 {
		t.Fatalf("operations = %#v, want a step per fused piece plus the machine step", incoming.Operations)
	}
	// BACK resolves to the card's only INTERLINING line.
	for i, piece := range []string{"FRONT", "BACK"} {
		fusing := incoming.Operations[i]
		if !fusing.Draft {
			t.Fatalf("recreated %s step = %#v, want a draft", piece, fusing)
		}
		assertPerPieceStep(t, fusing, piece, "INTERLINING-A", "INTERLINING-A")
	}
	if got := incoming.Issues[0].OperationNumber; got != opNumber(30) {
		t.Fatalf("issue operation number = %#v, want 30", got)
	}
}

// manualFusing is a hand-made FUSING step: no automatic identity, never a draft.
func manualFusing(number int32, bom []string, pieces ...string) entity.TechCardOperation {
	op := entity.TechCardOperation{
		OperationNumber: opNumber(number),
		OperationType:   entity.OpTypeFusing,
		Zone:            entity.ZoneInterlining,
		BomLineKeys:     bom,
		Note:            sql.NullString{String: "by hand", Valid: true},
	}
	for _, piece := range pieces {
		op.AssemblyInputs = append(op.AssemblyInputs, entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: piece})
		op.InputKeys = append(op.InputKeys, piece)
		op.PieceLineKeys = append(op.PieceLineKeys, piece)
	}
	return op
}

// wireEcho is a stored step as an up-to-date client sends it back: no private key, no resolved ids.
func wireEcho(op entity.TechCardOperation, number int32) entity.TechCardOperation {
	op.OperationNumber = opNumber(number)
	op.AutoFusingLineKey, op.AutoFusingPieceKey = sql.NullString{}, sql.NullString{}
	op.PieceIds, op.BomIds = nil, nil
	return op
}

func TestManualStepOnSameLineAndNumberDoesNotHideDraftAutoOp(t *testing.T) {
	manual := manualFusing(20, []string{"INTERLINING-A"}, "BACK")
	stored := fusedCard([]entity.TechCardOperation{
		storedAutoFusing(10, "INTERLINING-A", "FRONT"), manual, machineOp(30, "join"),
	})
	// The stale list never had the automatic step, so the manual one sits on its number 10.
	incoming := staleCopy(stored, []entity.TechCardOperation{wireEcho(manual, 10), machineOp(20, "join")})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || !outcome.Carried || outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want the automatic step carried", outcome, err)
	}
	if len(incoming.Operations) != 3 {
		t.Fatalf("operations = %#v, want carried auto, manual, machine", incoming.Operations)
	}
	if got := incoming.Operations[0]; got.AutoFusingPieceKey != autoKey("FRONT") || !got.Draft {
		t.Fatalf("first step = %#v, want the carried draft automatic step", got)
	}
	if got := incoming.Operations[1]; !reflect.DeepEqual(got, wireEcho(manual, 20)) {
		t.Fatalf("manual step was adopted or changed: want=%#v got=%#v", wireEcho(manual, 20), got)
	}
}

func TestStaleSaveLetsHandMadeStepOfThePieceTakeOver(t *testing.T) {
	// A person's own single-piece step for FRONT is the shape the reconciler adopts. When the payload
	// lacks the automatic draft, that step becomes FRONT's step instead of the draft coming back.
	manual := manualFusing(20, []string{"INTERLINING-A"}, "FRONT")
	stored := fusedCard([]entity.TechCardOperation{
		storedAutoFusing(10, "INTERLINING-A", "FRONT"), manual, machineOp(30, "join"),
	})
	incoming := staleCopy(stored, []entity.TechCardOperation{wireEcho(manual, 10), machineOp(20, "join")})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || outcome.Carried || !outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want the hand-made step adopted", outcome, err)
	}
	if len(incoming.Operations) != 2 {
		t.Fatalf("operations = %#v, want the adopted step and the machine step", incoming.Operations)
	}
	adopted := incoming.Operations[0]
	assertPerPieceStep(t, adopted, "FRONT", "INTERLINING-A", "INTERLINING-A")
	if adopted.Draft || adopted.Note.String != "by hand" {
		t.Fatalf("adopted step = %#v, want the person's step kept as edited", adopted)
	}
}

func TestLinklessManualStepDoesNotHideUnresolvedAutoOp(t *testing.T) {
	manual := manualFusing(20, nil, "BACK")
	stored := fusedCard([]entity.TechCardOperation{storedAutoFusing(10, "", "FRONT"), manual, machineOp(30, "join")})
	stored.BomItems = append(stored.BomItems, entity.TechCardBomItem{LineKey: "INTERLINING-B", Section: entity.BomSectionInterlining})
	stored.Pieces[0].FusingBomLineKey = ""
	incoming := staleCopy(stored, []entity.TechCardOperation{wireEcho(manual, 10), machineOp(20, "join")})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || !outcome.Carried {
		t.Fatalf("outcome = %+v err = %v, want the unresolved step carried", outcome, err)
	}
	if len(incoming.Operations) != 3 || incoming.Operations[0].AutoFusingLineKey.Valid ||
		incoming.Operations[0].AutoFusingPieceKey != autoKey("FRONT") {
		t.Fatalf("operations = %#v, want the unresolved automatic step first", incoming.Operations)
	}
	if got := incoming.Operations[1]; got.AutoFusingLineKey.Valid || !reflect.DeepEqual(got, wireEcho(manual, 20)) {
		t.Fatalf("linkless manual step was adopted into the unresolved group: %#v", got)
	}
}

func TestUniqueSamePieceEditKeepsAutoIdentity(t *testing.T) {
	edited := storedAutoFusing(10, "INTERLINING-A", "FRONT")
	edited.Draft = false
	for _, tc := range []struct {
		name     string
		stored   []entity.TechCardOperation
		incoming []entity.TechCardOperation
	}{
		{
			name:   "new step with other links on its number",
			stored: []entity.TechCardOperation{edited, machineOp(20, "join")},
			incoming: []entity.TechCardOperation{
				manualFusing(10, []string{"INTERLINING-A", "THREAD"}, "FRONT"), machineOp(20, "join"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := fusedCard(tc.stored)
			stored.BomItems = append(stored.BomItems, entity.TechCardBomItem{LineKey: "THREAD"})
			incoming := staleCopy(stored, tc.incoming)
			outcome, err := ReconcileTechCardUpdate(stored, incoming)
			if err != nil || outcome.Carried || outcome.Reconcile {
				t.Fatalf("outcome = %+v err = %v, want the unique same-piece edit retained in place", outcome, err)
			}
			if len(incoming.Operations) != 2 {
				t.Fatalf("operations = %#v, want the edited step once", incoming.Operations)
			}
			got := incoming.Operations[0]
			if got.AutoFusingPieceKey != autoKey("FRONT") || got.AutoFusingLineKey.Valid || got.Draft ||
				got.Note.String != "by hand" || len(got.BomLineKeys) != 2 {
				t.Fatalf("same-piece edit lost identity or content: %#v", got)
			}
		})
	}
}

func TestAmbiguousSamePieceEditsKeepStoreFallbackEnabled(t *testing.T) {
	edited := storedAutoFusing(10, "INTERLINING-A", "FRONT")
	edited.Draft = false
	stored := fusedCard([]entity.TechCardOperation{edited, machineOp(20, "join")})
	stored.BomItems = append(stored.BomItems,
		entity.TechCardBomItem{LineKey: "THREAD"}, entity.TechCardBomItem{LineKey: "TAPE"})
	first := manualFusing(10, []string{"INTERLINING-A", "THREAD"}, "FRONT")
	second := manualFusing(20, []string{"INTERLINING-A", "TAPE"}, "FRONT")
	incoming := staleCopy(stored, []entity.TechCardOperation{first, second, machineOp(30, "join")})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || outcome.Carried || outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want no ambiguous content match", outcome, err)
	}
	if incoming.AutoFusingKeysResolved {
		t.Fatal("ambiguous same-piece candidates disabled the store fallback")
	}
	for i := 0; i < 2; i++ {
		if incoming.Operations[i].AutoFusingPieceKey.Valid || incoming.Operations[i].AutoFusingLineKey.Valid {
			t.Fatalf("candidate %d inherited an ambiguous identity: %#v", i, incoming.Operations[i])
		}
	}
}

func TestReorderedEditedAutoAndSameShapeManualKeepTheirIdentities(t *testing.T) {
	edited := storedAutoFusing(10, "INTERLINING-A", "FRONT")
	edited.Draft = false
	edited.Note = sql.NullString{String: "automatic settings", Valid: true}
	manual := manualFusing(20, []string{"INTERLINING-A"}, "FRONT")
	stored := fusedCard([]entity.TechCardOperation{edited, manual, machineOp(30, "join")})
	incoming := staleCopy(stored, []entity.TechCardOperation{
		wireEcho(manual, 10), wireEcho(edited, 20), machineOp(30, "join"),
	})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || outcome.Carried || outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want a content-matched reorder", outcome, err)
	}
	if !incoming.AutoFusingKeysResolved {
		t.Fatal("unambiguous full-content echoes left the store fallback enabled")
	}
	if got := incoming.Operations[0]; got.AutoFusingPieceKey.Valid || got.AutoFusingLineKey.Valid ||
		got.Note.String != "by hand" {
		t.Fatalf("manual step inherited automatic identity: %#v", got)
	}
	if got := incoming.Operations[1]; got.AutoFusingPieceKey != autoKey("FRONT") ||
		got.AutoFusingLineKey.Valid || got.Note.String != "automatic settings" {
		t.Fatalf("edited automatic step lost its identity: %#v", got)
	}
}

func TestDeletedPieceInputIsRemovedBeforeAssemblyValidation(t *testing.T) {
	for _, draft := range []bool{true, false} {
		name := "edited_orphan_is_kept"
		if draft {
			name = "draft_is_deleted"
		}
		t.Run(name, func(t *testing.T) {
			step := storedAutoFusing(10, "INTERLINING-A", "FRONT")
			step.Draft = draft
			if !draft {
				step.Note = sql.NullString{String: "operator settings", Valid: true}
			}
			stored := fusedCard([]entity.TechCardOperation{step, machineOp(20, "join")})
			echo := wireEcho(step, 10)
			incoming := staleCopy(stored, []entity.TechCardOperation{echo, machineOp(20, "join")})
			incoming.Pieces = []entity.TechCardPiece{stored.Pieces[1]} // FRONT was deleted in this save
			incoming.AssemblyAware = true

			PrepareTechCardUpdateAssembly(stored, incoming)
			prepared := incoming.Operations[0]
			if prepared.AutoFusingPieceKey != autoKey("FRONT") || len(prepared.InputKeys) != 0 ||
				len(prepared.PieceLineKeys) != 0 || len(prepared.AssemblyInputs) != 0 {
				t.Fatalf("prepared keyed step kept its dead piece input: %#v", prepared)
			}
			if verr := dto.RevalidateTechCardAssembly(incoming); verr != nil {
				t.Fatalf("first assembly validation rejected the removable server input: %v", verr)
			}
			outcome, verr := ReconcileTechCardUpdate(stored, incoming)
			if verr != nil || !outcome.Reconcile {
				t.Fatalf("outcome = %+v err = %v, want deletion reconciliation", outcome, verr)
			}
			if draft {
				if len(incoming.Operations) != 1 || incoming.Operations[0].OperationType != entity.OpTypeMachine {
					t.Fatalf("draft of the deleted piece survived: %#v", incoming.Operations)
				}
				return
			}
			if len(incoming.Operations) != 2 {
				t.Fatalf("edited step was lost: %#v", incoming.Operations)
			}
			orphan := incoming.Operations[0]
			if orphan.AutoFusingPieceKey != autoKey("FRONT") || orphan.AutoFusingLineKey.Valid ||
				orphan.Review.String != ReviewOrphan || orphan.Draft || len(pieceInputKeys(&orphan, nil)) != 0 {
				t.Fatalf("edited step was not retained as a keyed orphan: %#v", orphan)
			}
		})
	}
}

func TestDeletedEditedStepLeavesTheStoredManualStepToTakeThePiece(t *testing.T) {
	edited := storedAutoFusing(10, "INTERLINING-A", "FRONT")
	edited.Draft = false
	manual := manualFusing(20, []string{"INTERLINING-A"}, "FRONT")
	stored := fusedCard([]entity.TechCardOperation{edited, manual, machineOp(30, "join")})
	// The operator deleted the edited automatic step; the stored manual step moved up onto 10.
	incoming := staleCopy(stored, []entity.TechCardOperation{wireEcho(manual, 10), machineOp(20, "join")})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || outcome.Carried {
		t.Fatalf("outcome = %+v err = %v, want no carry", outcome, err)
	}
	if len(incoming.Operations) != 2 {
		t.Fatalf("operations = %#v, want no step back for the deleted one", incoming.Operations)
	}
	got := incoming.Operations[0]
	assertPerPieceStep(t, got, "FRONT", "INTERLINING-A", "INTERLINING-A")
	if got.Draft || got.Note.String != "by hand" || got.OperationNumber != opNumber(10) {
		t.Fatalf("remaining step = %#v, want the manual step, unchanged, now FRONT's", got)
	}
}

func TestAutoOpReviewedInThisSaveKeepsIdentity(t *testing.T) {
	stored := fusedCard([]entity.TechCardOperation{storedAutoFusing(10, "INTERLINING-A", "FRONT"), machineOp(20, "join")})
	reviewed := wireEcho(stored.Operations[0], 10)
	reviewed.Draft = false
	reviewed.PressTemperatureC = sql.NullInt32{Int32: 140, Valid: true}
	incoming := staleCopy(stored, []entity.TechCardOperation{reviewed, machineOp(20, "join")})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || outcome.Carried || outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want no carry", outcome, err)
	}
	if len(incoming.Operations) != 2 || incoming.Operations[0].AutoFusingLineKey.Valid ||
		incoming.Operations[0].AutoFusingPieceKey != autoKey("FRONT") || incoming.Operations[0].Draft {
		t.Fatalf("operations = %#v, want the reviewed step alone, keeping its identity", incoming.Operations)
	}
}

func TestDraftEchoWithStalePiecesIsNotMissing(t *testing.T) {
	stored := fusedCard([]entity.TechCardOperation{legacyGroupedFusing(10, "INTERLINING-A", "FRONT", "BACK"), machineOp(20, "join")})
	stored.Pieces[1].Fused = true
	stored.Pieces[1].FusingBomLineKey = "INTERLINING-A"
	echo := wireEcho(legacyGroupedFusing(30, "INTERLINING-A", "FRONT"), 30) // client missed BACK joining
	incoming := staleCopy(stored, []entity.TechCardOperation{machineOp(10, "join"), echo})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || outcome.Carried || !outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want the grouped echo matched and split", outcome, err)
	}
	if len(incoming.Operations) != 3 {
		t.Fatalf("operations = %#v, want one step per fused piece and the machine step", incoming.Operations)
	}
	assertPerPieceStep(t, incoming.Operations[0], "FRONT", "INTERLINING-A", "INTERLINING-A")
	assertPerPieceStep(t, incoming.Operations[1], "BACK", "INTERLINING-A", "INTERLINING-A")
}

func TestStaleDraftEchoMatchesItsStoredStepByPiece(t *testing.T) {
	// FRONT moved to INTERLINING-B and the server re-linked its draft step; the client still holds
	// the step on INTERLINING-A. Same piece, both drafts: the same step, not a missing one.
	stored := fusedCard([]entity.TechCardOperation{storedAutoFusing(10, "INTERLINING-B", "FRONT"), machineOp(20, "join")})
	stored.BomItems = append(stored.BomItems, entity.TechCardBomItem{LineKey: "INTERLINING-B", Section: entity.BomSectionInterlining})
	stored.Pieces[0].FusingBomLineKey = "INTERLINING-B"
	incoming := staleCopy(stored, []entity.TechCardOperation{
		wireEcho(storedAutoFusing(10, "INTERLINING-A", "FRONT"), 10), machineOp(20, "join"),
	})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || outcome.Carried || !outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want the echo matched and re-linked", outcome, err)
	}
	if len(incoming.Operations) != 2 {
		t.Fatalf("operations = %#v, want the step once plus the machine step", incoming.Operations)
	}
	assertPerPieceStep(t, incoming.Operations[0], "FRONT", "INTERLINING-B", "INTERLINING-B")
}

func TestGroupedLegacyStepIsSplitOnNextSave(t *testing.T) {
	grouped := legacyGroupedFusing(10, "INTERLINING-A", "FRONT", "BACK")
	grouped.Draft = false
	grouped.PressTemperatureC = sql.NullInt32{Int32: 140, Valid: true}
	stored := fusedCard(
		[]entity.TechCardOperation{grouped, joinOp(20, "SHELL", nil, "FRONT", "BACK")},
		entity.TechCardIssue{OperationNumber: opNumber(10), Description: "bubbling"},
		entity.TechCardIssue{OperationNumber: opNumber(20), Description: "seam"},
	)
	stored.Pieces[1].Fused, stored.Pieces[1].FusingBomLineKey = true, "INTERLINING-A"
	// An up-to-date client saves an unrelated edit: every step echoed as read.
	incoming := staleCopy(stored,
		[]entity.TechCardOperation{wireEcho(grouped, 10), wireEcho(stored.Operations[1], 20)}, stored.Issues...)
	incoming.AssemblyAware = true

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || !outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want the grouped step split", outcome, err)
	}
	if len(incoming.Operations) != 3 {
		t.Fatalf("operations = %#v, want FRONT, BACK, join", incoming.Operations)
	}
	front, back := incoming.Operations[0], incoming.Operations[1]
	assertPerPieceStep(t, front, "FRONT", "INTERLINING-A", "INTERLINING-A")
	assertPerPieceStep(t, back, "BACK", "INTERLINING-A", "INTERLINING-A")
	if front.Draft || front.PressTemperatureC.Int32 != 140 || !back.Draft || back.PressTemperatureC.Int32 != 140 {
		t.Fatalf("split = %#v / %#v, want the edited row for FRONT and a draft copy for BACK", front, back)
	}
	if incoming.Issues[0].OperationNumber != opNumber(10) || incoming.Issues[1].OperationNumber != opNumber(30) {
		t.Fatalf("issues = %#v, want 10 (the kept row) and 30 (the join)", incoming.Issues)
	}
	if verr := dto.RevalidateTechCardAssembly(incoming); verr != nil {
		t.Fatalf("split card failed assembly revalidation: %v", verr)
	}

	// The next save of the converted card is a no-op.
	converted := staleCopy(incoming, incoming.Operations, incoming.Issues...)
	echoes := make([]entity.TechCardOperation, 0, len(converted.Operations))
	for _, op := range converted.Operations {
		echoes = append(echoes, wireEcho(op, op.OperationNumber.Int32))
	}
	again := staleCopy(converted, echoes, converted.Issues...)
	outcome, err = ReconcileTechCardUpdate(converted, again)
	if err != nil || outcome.Reconcile || outcome.Carried {
		t.Fatalf("save after conversion: outcome = %+v err = %v, want no change", outcome, err)
	}
	if !reflect.DeepEqual(again.Operations, converted.Operations) {
		t.Fatalf("save after conversion lost identity: want=%#v got=%#v", converted.Operations, again.Operations)
	}
}
