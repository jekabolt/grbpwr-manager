package fusingops

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestUnrelatedUpdateDoesNotTouchLegacyFusingOperation(t *testing.T) {
	legacy := entity.TechCardOperation{
		OperationNumber: sql.NullInt32{Int32: 20, Valid: true},
		OperationType:   entity.OpTypeFusing,
		Zone:            entity.ZoneInterlining,
		PieceLineKeys:   []string{"FRONT"},
	}
	stored := &entity.TechCardInsert{
		Name:       "before",
		Pieces:     []entity.TechCardPiece{{LineKey: "FRONT"}},
		Operations: []entity.TechCardOperation{{OperationType: entity.OpTypeMachine}, legacy},
	}
	incoming := *stored
	incoming.Name = "unrelated edit"
	incoming.Pieces = append([]entity.TechCardPiece(nil), stored.Pieces...)
	incoming.Operations = append([]entity.TechCardOperation(nil), stored.Operations...)
	before := append([]entity.TechCardOperation(nil), incoming.Operations...)

	changed, err := ReconcileTechCardUpdate(stored, &incoming)
	if err != nil {
		t.Fatalf("ReconcileTechCardUpdate() error = %v", err)
	}
	if changed {
		t.Fatal("unrelated save was classified as a fusing change")
	}
	if !reflect.DeepEqual(incoming.Operations, before) {
		t.Fatalf("unrelated save mutated legacy FUSING operation: before=%#v after=%#v", before, incoming.Operations)
	}
}

func TestPostReconcileAssemblyValidationPreventsProducedLaterDependency(t *testing.T) {
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
			{
				OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
				OperationType:   entity.OpTypeMachine,
				InputKeys:       []string{"FRONT", "BACK"}, PieceLineKeys: []string{"FRONT", "BACK"},
				OutputUnitKey: sql.NullString{String: "SHELL", Valid: true},
			},
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
	verr := dto.RevalidateTechCardAssembly(card)
	if verr == nil {
		t.Fatal("post-reconcile validation accepted a FUSING step that consumes a unit produced later")
	}
	if verr.Reason != string(entity.AssemblyDetailProducedLater) {
		t.Fatalf("reason = %q, want %q", verr.Reason, entity.AssemblyDetailProducedLater)
	}
}

func TestDeletedInterliningKeepsEditedOperationAsDistinctLinklessOrphan(t *testing.T) {
	card := &entity.TechCardInsert{
		Operations: []entity.TechCardOperation{{
			OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
			OperationType:   entity.OpTypeFusing, Zone: entity.ZoneInterlining,
			BomLineKeys: []string{"DELETED-INTERLINING"},
		}},
	}
	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 1 || card.Operations[0].Review.String != ReviewOrphan ||
		len(card.Operations[0].BomLineKeys) != 0 || !card.Operations[0].AutoFusingLineKey.Valid ||
		card.Operations[0].AutoFusingLineKey.String != "DELETED-INTERLINING" {
		t.Fatalf("deleted-line operation was not retained as a linkless orphan: %#v", card.Operations)
	}

	// A human may acknowledge the workflow review. The private group snapshot, not that clearable
	// mark, must keep the operation distinct from the shared unresolved bucket.
	card.Operations[0].Review = sql.NullString{}
	card.Pieces = []entity.TechCardPiece{{LineKey: "FRONT", Fused: true}}
	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("second ReconcileTechCard() error = %v", err)
	}
	if len(card.Operations) != 2 {
		t.Fatalf("operations = %d, want orphan plus unresolved auto operation", len(card.Operations))
	}
	if card.Operations[0].Review.String != ReviewOrphan || card.Operations[0].Draft {
		t.Fatalf("edited orphan was re-adopted: %#v", card.Operations[0])
	}
	if !card.Operations[1].Draft || card.Operations[1].Review.Valid || len(card.Operations[1].BomLineKeys) != 0 {
		t.Fatalf("shared unresolved operation = %#v", card.Operations[1])
	}
	before := append([]entity.TechCardOperation(nil), card.Operations...)
	if err := ReconcileTechCard(card); err != nil {
		t.Fatalf("third ReconcileTechCard() error = %v", err)
	}
	if !reflect.DeepEqual(card.Operations, before) {
		t.Fatalf("later reconcile duplicated or re-adopted an operation: before=%#v after=%#v", before, card.Operations)
	}
}

func TestDeletingDraftFusingOperationNullsIssueBeforeNumberReuse(t *testing.T) {
	card := &entity.TechCardInsert{
		Operations: []entity.TechCardOperation{
			{OperationNumber: sql.NullInt32{Int32: 10, Valid: true}, OperationType: entity.OpTypeMachine},
			{OperationNumber: sql.NullInt32{Int32: 20, Valid: true}, OperationType: entity.OpTypeFusing, Draft: true},
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

func TestReconcileFusingOps(t *testing.T) {
	tests := []struct {
		name       string
		pieces     []Piece
		operations []Operation
		want       Plan
	}{
		{
			name: "groups selected and unresolved pieces before untouched operations",
			pieces: []Piece{
				{ID: 11, Fused: true, BOMItemID: 101},
				{ID: 12, Fused: true, BOMItemID: 101},
				{ID: 13, Fused: true},
				{ID: 14, Fused: false, BOMItemID: 102},
			},
			operations: []Operation{{ID: 1}, {ID: 2, Fusing: true, BOMItemID: 101, Draft: true, Review: ReviewOrphan}},
			want: Plan{Operations: []Operation{
				{ID: 2, Fusing: true, BOMItemID: 101, Draft: true, PieceIDs: []int{11, 12}},
				{Fusing: true, Draft: true, PieceIDs: []int{13}},
				{ID: 1},
			}},
		},
		{
			name:   "moves a piece deletes the emptied draft and orphans an edited operation",
			pieces: []Piece{{ID: 11, Fused: true, BOMItemID: 202}},
			operations: []Operation{
				{ID: 10, Fusing: true, BOMItemID: 101, Draft: true, PieceIDs: []int{11}},
				{ID: 20, Fusing: true, BOMItemID: 202, PieceIDs: []int{99}},
				{ID: 30, Fusing: true, BOMItemID: 303, PieceIDs: []int{88}},
			},
			want: Plan{
				Operations: []Operation{
					{ID: 20, Fusing: true, BOMItemID: 202, PieceIDs: []int{11}},
					{ID: 30, Fusing: true, BOMItemID: 303, Review: ReviewOrphan},
				},
				DeletedIDs: []int{10},
			},
		},
		{
			name:   "does not create a duplicate for the same line and orphans an edited duplicate",
			pieces: []Piece{{ID: 11, Fused: true, BOMItemID: 101}},
			operations: []Operation{
				{ID: 10, Fusing: true, BOMItemID: 101},
				{ID: 20, Fusing: true, BOMItemID: 101},
			},
			want: Plan{Operations: []Operation{
				{ID: 10, Fusing: true, BOMItemID: 101, PieceIDs: []int{11}},
				{ID: 20, Fusing: true, BOMItemID: 101, Review: ReviewOrphan},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReconcileFusingOps(tt.pieces, tt.operations)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ReconcileFusingOps() = %#v, want %#v", got, tt.want)
			}
			again := ReconcileFusingOps(tt.pieces, got.Operations)
			if !reflect.DeepEqual(again.Operations, got.Operations) || len(again.DeletedIDs) != 0 {
				t.Fatalf("second reconcile changed state: first=%#v second=%#v", got, again)
			}
		})
	}

	t.Run("materializes the write model before digesting and remaps issue numbers", func(t *testing.T) {
		card := &entity.TechCardInsert{
			BomItems: []entity.TechCardBomItem{{
				LineKey: "INTERLINING-A", Section: entity.BomSectionInterlining,
			}},
			Pieces: []entity.TechCardPiece{{
				LineKey: "FRONT", Fused: true, FusingBomLineKey: "INTERLINING-A",
			}},
			Operations: []entity.TechCardOperation{{
				OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
				OperationType:   entity.OpTypeMachine, Zone: entity.ZoneOuter,
			}},
			Issues: []entity.TechCardIssue{{
				OperationNumber: sql.NullInt32{Int32: 10, Valid: true}, Description: "check seam",
			}},
		}
		if err := ReconcileTechCard(card); err != nil {
			t.Fatalf("ReconcileTechCard() error = %v", err)
		}
		if len(card.Operations) != 2 {
			t.Fatalf("operations = %d, want 2", len(card.Operations))
		}
		fusing := card.Operations[0]
		if fusing.OperationType != entity.OpTypeFusing || !fusing.Draft ||
			fusing.PressEquipment.String != "fusing_press" ||
			!reflect.DeepEqual(fusing.BomLineKeys, []string{"INTERLINING-A"}) ||
			!reflect.DeepEqual(fusing.PieceLineKeys, []string{"FRONT"}) {
			t.Fatalf("generated fusing operation = %#v", fusing)
		}
		if got := card.Operations[1].OperationNumber.Int32; got != 20 {
			t.Fatalf("existing operation number = %d, want 20", got)
		}
		if got := card.Issues[0].OperationNumber.Int32; got != 20 {
			t.Fatalf("issue operation number = %d, want 20", got)
		}
		before := append([]entity.TechCardOperation(nil), card.Operations...)
		if err := ReconcileTechCard(card); err != nil {
			t.Fatalf("second ReconcileTechCard() error = %v", err)
		}
		if !reflect.DeepEqual(card.Operations, before) {
			t.Fatalf("second write-model reconcile changed operations: before=%#v after=%#v", before, card.Operations)
		}
	})
}
