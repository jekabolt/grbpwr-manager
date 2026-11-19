package admin

import (
	"context"
	"strings"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) prepareGuardedZeroOperationPieceRename(
	ctx context.Context, id int, expectedLock int, rename *pb_admin.GuardedZeroOperationPieceRename,
) (*entity.TechCardInsert, error) {
	name := rename.GetNextName()
	if expectedLock <= 0 || !entity.ValidTechCardSeamKey(rename.GetPieceLineKey()) ||
		!entity.ValidTechCardSeamKey(rename.GetPatternLineKey()) ||
		rename.GetExpectedSavedRowRevision() == 0 ||
		!entity.ValidPatternSemanticSidecarSHA256(rename.GetExpectedDxfSha256()) ||
		rename.GetScopeKey() == "" || strings.TrimSpace(rename.GetScopeKey()) != rename.GetScopeKey() ||
		rename.GetExpectedName() == name || name == "" || strings.TrimSpace(name) != name || len(name) > 255 {
		return nil, status.Error(codes.InvalidArgument, "send one changed piece name and its exact saved pattern binding")
	}
	intent := &entity.GuardedZeroOperationPieceRename{
		PieceLineKey: rename.GetPieceLineKey(), ExpectedName: rename.GetExpectedName(), NextName: name,
		ScopeKey: rename.GetScopeKey(), PatternLineKey: rename.GetPatternLineKey(),
		SavedRowRevision: rename.GetExpectedSavedRowRevision(), DxfSHA256: rename.GetExpectedDxfSha256(),
	}
	if rename.ExpectedServerAssemblyRevision != nil || rename.ExpectedCompleteSourceSha256 != nil ||
		rename.ExpectedCurrentSidecarSha256 != nil {
		if intent.ScopeKey != "main" || strings.TrimSpace(intent.ExpectedName) == "" ||
			!entity.ValidTechCardAssemblyRevision(rename.GetExpectedServerAssemblyRevision()) ||
			!entity.ValidPatternSemanticSidecarSHA256(rename.GetExpectedCompleteSourceSha256()) ||
			!entity.ValidPatternSemanticSidecarSHA256(rename.GetExpectedCurrentSidecarSha256()) {
			return nil, status.Error(codes.InvalidArgument, "send the current MAIN Assembly revision, Complete source SHA-256 and V10 SHA-256 together")
		}
		intent.Attached = &entity.GuardedAttachedPieceRenameAuthority{
			ExpectedServerAssemblyRevision: rename.GetExpectedServerAssemblyRevision(),
			ExpectedCompleteSourceSHA256:   rename.GetExpectedCompleteSourceSha256(),
			ExpectedCurrentSidecarSHA256:   rename.GetExpectedCurrentSidecarSha256(),
		}
	}
	stored, err := s.repo.TechCards().GetTechCardByIdConsistent(ctx, id)
	if err != nil {
		return nil, s.techCardWriteError(ctx, err)
	}
	if stored == nil || stored.LockVersion != expectedLock {
		return nil, s.techCardWriteError(ctx, entity.ErrTechCardConflict)
	}
	if stored.ApprovalState == entity.TechCardApprovalReleased {
		return nil, s.techCardWriteError(ctx, entity.ErrTechCardReleased)
	}
	if intent.Attached != nil {
		if _, err := entity.GuardedAttachedPieceRenamePiece(stored, intent); err != nil {
			return nil, s.techCardWriteError(ctx, err)
		}
		return &entity.TechCardInsert{
			GuardedPieceRename: intent,
			ApprovalState:      stored.ApprovalState,
			UpdatedBy:          authsrv.GetAdminUsername(ctx),
		}, nil
	}
	index, err := entity.GuardedPieceRenamePiece(stored, intent)
	if err != nil {
		return nil, s.techCardWriteError(ctx, err)
	}
	body := dto.ConvertEntityTechCardToPb(stored, s.costingFx(ctx))
	if _, canWrite := s.costingAccess(ctx); !canWrite {
		stripTechCardCosting(body)
	}
	if err := renameSavedZeroOperationPiece(stored, body.GetTechCard(), index, name); err != nil {
		return nil, s.techCardWriteError(ctx, err)
	}

	tc, err := s.prepareTechCardWriteFrom(ctx, id, body.GetTechCard(), "", techCardWriteSource{stored: stored})
	if err != nil {
		return nil, err
	}
	tc.GuardedPieceRename = intent
	return tc, nil
}

func renameSavedZeroOperationPiece(stored *entity.TechCard, body *pb_common.TechCardInsert, index int, name string) error {
	piece := stored.Pieces[index]
	for i, other := range stored.Pieces {
		if i != index && strings.EqualFold(strings.TrimSpace(other.Name), name) {
			return entity.ErrTechCardPieceRenameProtected
		}
	}
	if piece.CalloutNumber.Valid {
		number := int(piece.CalloutNumber.Int32)
		technical := entity.TechCardTechnicalMedia(stored.Media)
		calloutIndex := -1
		for i, callout := range body.Callouts {
			if int(callout.GetNumber()) != number || !technical[int(callout.GetMediaId())] {
				continue
			}
			if calloutIndex != -1 {
				return entity.ErrTechCardPieceRenameProtected
			}
			calloutIndex = i
		}
		if calloutIndex != -1 {
			for otherIndex, other := range stored.Pieces {
				if otherIndex != index && other.CalloutNumber.Valid && other.CalloutNumber.Int32 == piece.CalloutNumber.Int32 {
					return entity.ErrTechCardPieceRenameProtected
				}
			}
			callout := body.Callouts[calloutIndex]
			parts := callout.GetParts()
			if len(parts) == 0 && callout.GetPart() != "" {
				parts = []string{callout.GetPart()}
			}
			if len(parts) > 0 {
				if !strings.EqualFold(strings.TrimSpace(parts[0]), strings.TrimSpace(piece.Name)) {
					return entity.ErrTechCardPieceRenameProtected
				}
				for _, part := range parts[1:] {
					if strings.EqualFold(strings.TrimSpace(part), name) {
						return entity.ErrTechCardPieceRenameProtected
					}
				}
				parts = parts[1:]
			}
			callout.Part = name
			callout.Parts = append([]string{name}, parts...)
		}
	}
	body.Pieces[index].Name = name
	return nil
}
