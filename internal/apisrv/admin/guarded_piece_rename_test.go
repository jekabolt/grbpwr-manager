package admin

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGuardedZeroOperationPieceRenameMirrorsOnlyUniqueTechnicalCallout(t *testing.T) {
	const nextName = "Передняя половинка"
	number := sql.NullInt32{Valid: true, Int32: 4}
	frontID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	card := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Pieces: []entity.TechCardPiece{{Name: "Front", LineKey: frontID, CalloutNumber: number, PiecesPerGarment: 2}},
		Media: []entity.TechCardMediaItem{
			{MediaId: 3, Category: entity.TechCardMediaCategoryTechnical},
			{MediaId: 7, Category: entity.TechCardMediaCategoryMoodboard},
		},
		Callouts: []entity.TechCardCallout{
			{Number: 4, Part: sql.NullString{String: "Front", Valid: true}, Parts: []string{"Front", "Back"}, MediaId: sql.NullInt32{Valid: true, Int32: 3}},
			{Number: 4, Part: sql.NullString{String: "Inspiration", Valid: true}, MediaId: sql.NullInt32{Valid: true, Int32: 7}},
		},
	}}
	t.Run("rewrite both part representations", func(t *testing.T) {
		body := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{}).GetTechCard()
		if err := renameSavedZeroOperationPiece(card, body, 0, nextName); err != nil {
			t.Fatal(err)
		}
		if body.Pieces[0].GetName() != nextName || body.Pieces[0].GetLineKey() != frontID ||
			body.Pieces[0].GetPiecesPerGarment() != 2 || body.Callouts[0].GetPart() != nextName ||
			!reflect.DeepEqual(body.Callouts[0].GetParts(), []string{nextName, "Back"}) ||
			body.Callouts[1].GetPart() != "Inspiration" {
			t.Fatalf("unexpected rewrite: %s", body)
		}
	})
	t.Run("refuse shared technical callout", func(t *testing.T) {
		card.Pieces = append(card.Pieces, entity.TechCardPiece{Name: "Shadow", CalloutNumber: number})
		body := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{}).GetTechCard()
		if err := renameSavedZeroOperationPiece(card, body, 0, nextName); !errors.Is(err, entity.ErrTechCardPieceRenameProtected) {
			t.Fatalf("shared callout got %v", err)
		}
		if body.Pieces[0].GetName() != "Front" || body.Callouts[0].GetPart() != "Front" {
			t.Fatal("refused plan changed body")
		}
	})
}

func TestGuardedZeroOperationPieceRenameCannotCarryFullBody(t *testing.T) {
	s := &Server{}
	_, err := s.UpdateTechCard(context.Background(), &pb_admin.UpdateTechCardRequest{
		Id: 12, ExpectedLockVersion: 14,
		GuardedPieceRename: &pb_admin.GuardedZeroOperationPieceRename{},
		TechCard:           &pb_common.TechCardInsert{},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("mixed request got %v", err)
	}
}
