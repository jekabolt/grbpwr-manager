package admin

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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

func TestGuardedAttachedPieceRenameOptInDoesNotBuildAFullDocument(t *testing.T) {
	const piece = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const pattern = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const clock = "asm:v1:0123456789abcdef0123456789abcdef:5"
	input := &pb_admin.GuardedZeroOperationPieceRename{
		PieceLineKey: piece, PatternLineKey: pattern, ExpectedName: "Front", NextName: "Front panel",
		ScopeKey: "main", ExpectedSavedRowRevision: 2, ExpectedDxfSha256: sha,
		ExpectedServerAssemblyRevision: proto.String(clock), ExpectedCompleteSourceSha256: proto.String(sha),
		ExpectedCurrentSidecarSha256: proto.String(sha),
	}
	for _, tc := range []struct {
		name string
		edit func(*pb_admin.GuardedZeroOperationPieceRename)
	}{
		{"missing clock", func(r *pb_admin.GuardedZeroOperationPieceRename) { r.ExpectedServerAssemblyRevision = nil }},
		{"missing Complete", func(r *pb_admin.GuardedZeroOperationPieceRename) { r.ExpectedCompleteSourceSha256 = nil }},
		{"missing carrier", func(r *pb_admin.GuardedZeroOperationPieceRename) { r.ExpectedCurrentSidecarSha256 = nil }},
		{"empty carrier", func(r *pb_admin.GuardedZeroOperationPieceRename) { r.ExpectedCurrentSidecarSha256 = proto.String("") }},
		{"wrong scope", func(r *pb_admin.GuardedZeroOperationPieceRename) { r.ScopeKey = "pocketing" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := proto.Clone(input).(*pb_admin.GuardedZeroOperationPieceRename)
			tc.edit(request)
			s := &Server{}
			_, err := s.prepareGuardedZeroOperationPieceRename(context.Background(), 12, 14, request)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("partial attached intent got %v", err)
			}
		})
	}

	ctx := context.Background()
	card := &entity.TechCard{Id: 12, LockVersion: 14, TechCardInsert: entity.TechCardInsert{
		ApprovalState: entity.TechCardApprovalDraft,
		Pieces:        []entity.TechCardPiece{{Id: 99, LineKey: piece, Name: "Front"}},
		Operations:    []entity.TechCardOperation{{}},
	}}
	cards := mocks.NewMockTechCards(t)
	cards.EXPECT().GetTechCardByIdConsistent(ctx, 12).Return(card, nil).Twice()
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().TechCards().Return(cards).Twice()
	server := &Server{repo: repo}
	native, err := server.prepareGuardedZeroOperationPieceRename(ctx, 12, 14, input)
	if err != nil {
		t.Fatal(err)
	}
	if native.GuardedPieceRename.Attached == nil || len(native.Pieces) != 0 || len(native.Operations) != 0 {
		t.Fatalf("attached command unexpectedly built a document: %#v", native)
	}
	input.ExpectedServerAssemblyRevision = nil
	input.ExpectedCompleteSourceSha256 = nil
	input.ExpectedCurrentSidecarSha256 = nil
	_, err = server.prepareGuardedZeroOperationPieceRename(ctx, 12, 14, input)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("legacy command should refuse a linked card, got %v", err)
	}
}
