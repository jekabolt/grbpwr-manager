package fusingops

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

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
