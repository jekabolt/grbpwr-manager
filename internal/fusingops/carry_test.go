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

// storedAutoFusing is a draft automatic FUSING step exactly as the read path returns it.
func storedAutoFusing(number int32, key string, pieces ...string) entity.TechCardOperation {
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
		fusing.AutoFusingLineKey != autoKey("INTERLINING-A") ||
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
	if len(incoming.Operations) != 2 || incoming.Operations[0].AutoFusingLineKey != autoKey("") ||
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
	if got := incoming.Operations[1]; got.AutoFusingLineKey != autoKey("INTERLINING-A") || got.OperationNumber != opNumber(20) {
		t.Fatalf("carried step = %#v, want auto INTERLINING-A at 20 (its stored place)", got)
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
	if len(incoming.Operations) != 3 || incoming.Operations[0].AutoFusingLineKey != autoKey("INTERLINING-A") {
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
	echo := storedAutoFusing(20, "INTERLINING-A", "FRONT")
	echo.AutoFusingLineKey = sql.NullString{} // never on the wire
	echo.PieceIds, echo.BomIds = nil, nil
	incoming := staleCopy(stored, []entity.TechCardOperation{machineOp(10, "join"), echo})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || outcome.Carried || outcome.Reconcile {
		t.Fatalf("outcome = %+v err = %v, want no carry", outcome, err)
	}
	if len(incoming.Operations) != 2 {
		t.Fatalf("renumbered step was duplicated: %#v", incoming.Operations)
	}
	if got := incoming.Operations[1]; got.AutoFusingLineKey != autoKey("INTERLINING-A") || got.OperationNumber != opNumber(20) {
		t.Fatalf("renumbered step = %#v, want identity INTERLINING-A kept at 20", got)
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
	if len(incoming.Operations) != 2 {
		t.Fatalf("operations = %#v, want recreated fusing step plus machine step", incoming.Operations)
	}
	fusing := incoming.Operations[0]
	if fusing.OperationType != entity.OpTypeFusing || !fusing.Draft ||
		fusing.AutoFusingLineKey != autoKey("INTERLINING-A") ||
		!reflect.DeepEqual(fusing.PieceLineKeys, []string{"FRONT", "BACK"}) {
		t.Fatalf("recreated fusing step = %#v", fusing)
	}
	if got := incoming.Issues[0].OperationNumber; got != opNumber(20) {
		t.Fatalf("issue operation number = %#v, want 20", got)
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
	op.AutoFusingLineKey = sql.NullString{}
	op.PieceIds, op.BomIds = nil, nil
	return op
}

func TestManualStepOnSameLineAndNumberDoesNotHideDraftAutoOp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pieces []string
	}{
		{name: "other pieces", pieces: []string{"BACK"}},
		{name: "same pieces as the automatic step", pieces: []string{"FRONT"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manual := manualFusing(20, []string{"INTERLINING-A"}, tc.pieces...)
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
			if got := incoming.Operations[0]; got.AutoFusingLineKey != autoKey("INTERLINING-A") || !got.Draft {
				t.Fatalf("first step = %#v, want the carried draft automatic step", got)
			}
			if got := incoming.Operations[1]; !reflect.DeepEqual(got, wireEcho(manual, 20)) {
				t.Fatalf("manual step was adopted or changed: want=%#v got=%#v", wireEcho(manual, 20), got)
			}
		})
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
	if len(incoming.Operations) != 3 || incoming.Operations[0].AutoFusingLineKey != autoKey("") {
		t.Fatalf("operations = %#v, want the unresolved automatic step first", incoming.Operations)
	}
	if got := incoming.Operations[1]; got.AutoFusingLineKey.Valid || !reflect.DeepEqual(got, wireEcho(manual, 20)) {
		t.Fatalf("linkless manual step was adopted into the unresolved group: %#v", got)
	}
}

func TestStepOnEditedAutoOpNumberDoesNotInheritItsIdentity(t *testing.T) {
	edited := storedAutoFusing(10, "INTERLINING-A", "FRONT")
	edited.Draft = false
	for _, tc := range []struct {
		name     string
		stored   []entity.TechCardOperation
		incoming []entity.TechCardOperation
	}{
		{
			// The operator deleted the edited automatic step; the stored manual step moved up onto 10.
			name: "stored manual step renumbered onto it",
			stored: []entity.TechCardOperation{
				edited, manualFusing(20, []string{"INTERLINING-A"}, "FRONT"), machineOp(30, "join"),
			},
			incoming: []entity.TechCardOperation{
				wireEcho(manualFusing(20, []string{"INTERLINING-A"}, "FRONT"), 10), machineOp(20, "join"),
			},
		},
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
			before := append([]entity.TechCardOperation(nil), incoming.Operations...)

			outcome, err := ReconcileTechCardUpdate(stored, incoming)
			if err != nil || outcome.Carried || outcome.Reconcile {
				t.Fatalf("outcome = %+v err = %v, want no change (edited step stays deleted)", outcome, err)
			}
			if !reflect.DeepEqual(incoming.Operations, before) {
				t.Fatalf("a manual step gained the edited step's identity: before=%#v after=%#v", before, incoming.Operations)
			}
		})
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
	if len(incoming.Operations) != 2 || incoming.Operations[0].AutoFusingLineKey != autoKey("INTERLINING-A") ||
		incoming.Operations[0].Draft {
		t.Fatalf("operations = %#v, want the reviewed step alone, keeping its identity", incoming.Operations)
	}
}

func TestDraftEchoWithStalePiecesIsNotMissing(t *testing.T) {
	stored := fusedCard([]entity.TechCardOperation{storedAutoFusing(10, "INTERLINING-A", "FRONT", "BACK"), machineOp(20, "join")})
	stored.Pieces[1].Fused = true
	stored.Pieces[1].FusingBomLineKey = "INTERLINING-A"
	echo := wireEcho(storedAutoFusing(30, "INTERLINING-A", "FRONT"), 30) // client missed BACK joining
	incoming := staleCopy(stored, []entity.TechCardOperation{machineOp(10, "join"), echo})

	outcome, err := ReconcileTechCardUpdate(stored, incoming)
	if err != nil || outcome.Carried {
		t.Fatalf("outcome = %+v err = %v, want no carry", outcome, err)
	}
	if len(incoming.Operations) != 2 || incoming.Operations[1].AutoFusingLineKey != autoKey("INTERLINING-A") {
		t.Fatalf("operations = %#v, want the draft echo matched", incoming.Operations)
	}
}
