package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/url"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func patternSemanticSidecarToPb(row entity.PatternSemanticSidecar) *pb_admin.TechCardPatternSemanticSidecar {
	return &pb_admin.TechCardPatternSemanticSidecar{
		TechCardId:            int32(row.TechCardId),
		PatternLineKey:        row.PatternLineKey,
		CardLockVersion:       int32(row.CardLockVersion),
		DxfSha256:             row.DxfSHA256,
		PatternVersion:        int32(row.PatternVersion),
		SidecarSha256:         row.SidecarSHA256,
		MediaType:             row.MediaType,
		ByteLength:            int32(row.ByteLength),
		SidecarBytes:          append([]byte(nil), row.Payload...),
		PostSaveReceiptSha256: row.PostSaveReceiptSHA256,
		CreatedAt:             timestamppb.New(row.CreatedAt),
		CreatedBy:             row.CreatedBy,
		SavedRowRevision:      row.SavedRowRevision,
	}
}

func patternSemanticSidecarError(ctx context.Context, action string, techCardID int, err error) error {
	if st, ok := apierr.Status(err); ok {
		return st
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, "pattern semantic sidecar not found")
	case errors.Is(err, entity.ErrTechCardReleased):
		return status.Error(codes.FailedPrecondition, entity.ErrTechCardReleased.Error())
	case errors.Is(err, entity.ErrNeutralPlacementModelMoved):
		return neutralPlacementModelMovedStatus()
	case errors.Is(err, entity.ErrTechCardPatternSidecarBindingMoved):
		return apierr.FailedPrecondition(entity.NewFieldViolation(
			"expected_card_lock_version", "pattern_binding_moved", "",
			"the card or pattern changed since the post-save read — reload it and rebuild the semantic sidecar"))
	case errors.Is(err, entity.ErrTechCardPatternSidecarCorrupt):
		return status.Error(codes.DataLoss, entity.ErrTechCardPatternSidecarCorrupt.Error())
	}
	slog.Default().ErrorContext(ctx, "can't "+action+" pattern semantic sidecar",
		slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't "+action+" pattern semantic sidecar")
}

// PutTechCardPatternSemanticSidecar attaches canonical G4F4 bytes after the DXF save/readback.
func (s *Server) PutTechCardPatternSemanticSidecar(ctx context.Context, req *pb_admin.PutTechCardPatternSemanticSidecarRequest) (*pb_admin.PutTechCardPatternSemanticSidecarResponse, error) {
	var expectedCardLockVersion *int
	if req.ExpectedCardLockVersion != nil {
		value := int(req.GetExpectedCardLockVersion())
		expectedCardLockVersion = &value
	}
	var expectedSavedRowRevision *uint32
	if req.ExpectedSavedRowRevision != nil {
		value := req.GetExpectedSavedRowRevision()
		expectedSavedRowRevision = &value
	}
	var expectedPreviousSidecarSHA256 *string
	if req.ExpectedPreviousSidecarSha256 != nil {
		value := req.GetExpectedPreviousSidecarSha256()
		expectedPreviousSidecarSHA256 = &value
	}
	var expectedServerAssemblyRevision *string
	if req.ExpectedServerAssemblyRevision != nil {
		value := req.GetExpectedServerAssemblyRevision()
		expectedServerAssemblyRevision = &value
	}
	var expectedAssemblyTopologySHA256 *string
	if req.ExpectedAssemblyTopologySha256 != nil {
		value := req.GetExpectedAssemblyTopologySha256()
		expectedAssemblyTopologySHA256 = &value
	}
	in := entity.PatternSemanticSidecarWrite{
		TechCardId:                     int(req.GetTechCardId()),
		PatternLineKey:                 req.GetPatternLineKey(),
		ExpectedCardLockVersion:        expectedCardLockVersion,
		ExpectedSavedRowRevision:       expectedSavedRowRevision,
		ExpectedPreviousSidecarSHA256:  expectedPreviousSidecarSHA256,
		ExpectedServerAssemblyRevision: expectedServerAssemblyRevision,
		ExpectedAssemblyTopologySHA256: expectedAssemblyTopologySHA256,
		SelectedOperationNumber:        int(req.GetSelectedOperationNumber()),
		ExpectedDxfSHA256:              req.GetExpectedDxfSha256(),
		ExpectedPatternVersion:         int(req.GetExpectedPatternVersion()),
		ExpectedPostSaveReceiptSHA256:  req.GetExpectedPostSaveReceiptSha256(),
		MediaType:                      req.GetMediaType(),
		SidecarSHA256:                  req.GetSidecarSha256(),
		Payload:                        append([]byte(nil), req.GetSidecarBytes()...),
		By:                             authsrv.GetAdminUsername(ctx),
	}
	if err := entity.ValidatePatternSemanticSidecarWrite(&in); err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	row, err := s.repo.TechCards().PutTechCardPatternSemanticSidecar(ctx, in)
	if err != nil {
		return nil, patternSemanticSidecarError(ctx, "write", in.TechCardId, err)
	}
	return &pb_admin.PutTechCardPatternSemanticSidecarResponse{
		SemanticSidecar: patternSemanticSidecarToPb(row),
	}, nil
}

// GetTechCardSavedAssemblySnapshot reads selected topology or the opt-in complete saved source in
// one REPEATABLE READ snapshot. Download URLs refer to those exact object URLs.
func (s *Server) GetTechCardSavedAssemblySnapshot(ctx context.Context, req *pb_admin.GetTechCardSavedAssemblySnapshotRequest) (*pb_admin.GetTechCardSavedAssemblySnapshotResponse, error) {
	in := entity.SavedAssemblySnapshotRequest{
		ExpectedRow: entity.SavedPatternRowIdentity{
			TechCardID:       int(req.GetTechCardId()),
			ScopeKey:         req.GetExpectedScopeKey(),
			PatternLineKey:   req.GetPatternLineKey(),
			SavedRowRevision: req.GetExpectedSavedRowRevision(),
			CardLockVersion:  int(req.GetExpectedCardLockVersion()),
			URL:              req.GetExpectedUrl(),
			SizeBytes:        req.GetExpectedSizeBytes(),
			SHA256:           req.GetExpectedSha256(),
		},
		SelectedOperationNumber:          int(req.GetSelectedOperationNumber()),
		Profile:                          entity.SavedAssemblySnapshotProfile(req.GetProfile()),
		ExpectedCompleteSourceSHA256:     req.GetExpectedCompleteSourceSha256(),
		SelectedSizeID:                   int(req.GetSelectedSizeId()),
		ExpectedAuthoredFoldSourceSHA256: req.GetExpectedAuthoredFoldSourceSha256(),
		SourceOnly:                       req.GetSourceOnly(),
	}
	if req.ExpectedServerAssemblyRevision != nil {
		in.ExpectedServerAssemblyRevision = req.GetExpectedServerAssemblyRevision()
	}
	if req.ExpectedAssemblyTopologySha256 != nil {
		in.ExpectedTopologySHA256 = req.GetExpectedAssemblyTopologySha256()
	}
	snapshot, err := s.repo.TechCards().GetTechCardSavedAssemblySnapshot(ctx, in)
	if err != nil {
		return nil, savedAssemblySnapshotError(ctx, in.ExpectedRow.TechCardID, err)
	}

	response := &pb_admin.GetTechCardSavedAssemblySnapshotResponse{
		Row:                     dto.SavedPatternRowIdentityToPb(snapshot.Row),
		SelectedOperationNumber: int32(snapshot.SelectedOperationNumber),
		SelectedSeams:           dto.TechCardSeamsToPb(snapshot.SelectedSeams),
		ServerAssemblyRevision:  snapshot.ServerAssemblyRevision,
		AssemblyTopologySha256:  snapshot.TopologySHA256,
		TopologyCanonicalJson:   append([]byte(nil), snapshot.TopologyCanonicalJSON...),
	}
	if snapshot.SemanticSidecar != nil {
		response.SemanticSidecar = patternSemanticSidecarToPb(*snapshot.SemanticSidecar)
	}
	if snapshot.CompleteSourceV1 != nil {
		source := snapshot.CompleteSourceV1
		response.CompleteSourceV1 = dto.SavedAssemblyCompleteSourceV1ToPb(source)
		if snapshot.AuthoredFoldSourceV1 != nil {
			response.AuthoredFoldSourceV1 = dto.SavedAssemblyAuthoredFoldSourceV1ToPb(snapshot.AuthoredFoldSourceV1)
		}
		response.Operations = dto.TechCardOperationsToPbForSavedAssembly(snapshot.Card.Operations)
		patterns := make([]*pb_common.TechCardSizePattern, 0, len(source.Rows))
		for _, row := range source.Rows {
			patterns = append(patterns, savedAssemblyPatternForDownload(row))
		}
		s.fillSavedAssemblyDownloadURLs(ctx, patterns)
		for i, pattern := range patterns {
			url := pattern.GetDownloadUrl()
			if url == "" {
				return nil, status.Error(codes.FailedPrecondition, "saved Assembly source download is unavailable")
			}
			response.CompleteSourceV1.Rows[i].DownloadUrl = url
			if pattern.GetLineKey() == snapshot.Row.PatternLineKey {
				response.Row.DownloadUrl = url
			}
		}
		return response, nil
	}
	pattern := savedAssemblyPatternForDownload(snapshot.Row)
	s.fillSavedAssemblyDownloadURLs(ctx, []*pb_common.TechCardSizePattern{pattern})
	response.Row.DownloadUrl = pattern.GetDownloadUrl()
	card := dto.ConvertEntityTechCardToPb(snapshot.Card, dto.CostingFx{})
	if card != nil && card.GetTechCard() != nil {
		response.Operations = card.GetTechCard().GetOperations()
	}
	return response, nil
}

func (s *Server) fillSavedAssemblyDownloadURLs(ctx context.Context, patterns []*pb_common.TechCardSizePattern) {
	// Every team gets the public pattern link: the team travels signed inside the token (v2).
	s.patternURLs.FillTechCardPatternURLs(ctx, s.patternURLsBaseURL, patterns)
	if t, ok := tenant.From(ctx); !ok || t.ID == tenant.TeamOneID {
		return
	}
	// Outside team #1 a row the fill left without a link (no service, mint failed) still downloads
	// through the authenticated proxy, which enforces the caller's storage prefix on the exact url
	// read from the saved row — the floor these teams had before public links.
	for _, pattern := range patterns {
		if raw := pattern.GetUrl(); raw != "" && pattern.GetDownloadUrl() == "" {
			pattern.DownloadUrl = "/api" + FileProxyPath + "?" + url.Values{"url": {raw}}.Encode()
		}
	}
}

func savedAssemblyPatternForDownload(row entity.SavedPatternRowIdentity) *pb_common.TechCardSizePattern {
	return &pb_common.TechCardSizePattern{Url: row.URL, Filename: row.Filename, LineKey: row.PatternLineKey}
}

func savedAssemblySnapshotError(ctx context.Context, techCardID int, err error) error {
	var violation *entity.ValidationError
	if (errors.Is(err, entity.ErrTechCardSavedAssemblyUnsupported) ||
		errors.Is(err, entity.ErrTechCardSavedAssemblyMoved)) && errors.As(err, &violation) {
		return apierr.FailedPrecondition(violation)
	}
	if st, ok := apierr.Status(err); ok {
		return st
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, "saved Assembly row not found")
	case errors.Is(err, entity.ErrTechCardPatternSidecarCorrupt):
		return status.Error(codes.DataLoss, entity.ErrTechCardPatternSidecarCorrupt.Error())
	case errors.Is(err, entity.ErrNeutralPlacementModelMoved):
		return neutralPlacementModelMovedStatus()
	case errors.Is(err, entity.ErrTechCardSavedAssemblyMoved):
		return status.Error(codes.FailedPrecondition, entity.ErrTechCardSavedAssemblyMoved.Error())
	case errors.Is(err, entity.ErrTechCardSavedAssemblyUnsupported):
		return status.Error(codes.FailedPrecondition, entity.ErrTechCardSavedAssemblyUnsupported.Error())
	}
	slog.Default().ErrorContext(ctx, "can't read saved Assembly snapshot",
		slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't read saved Assembly snapshot")
}

func neutralPlacementModelMovedStatus() error {
	return apierr.FailedPrecondition(entity.NewFieldViolation(
		"sidecar_bytes", "placement_model_binding_moved", "",
		"the saved mannequin or its neutral pose binding changed; reload the model and placement"))
}

// GetTechCardPatternSemanticSidecar reads and revalidates one current attachment.
func (s *Server) GetTechCardPatternSemanticSidecar(ctx context.Context, req *pb_admin.GetTechCardPatternSemanticSidecarRequest) (*pb_admin.GetTechCardPatternSemanticSidecarResponse, error) {
	techCardID := int(req.GetTechCardId())
	lineKey := entity.NormalizeTechCardSeamKey(req.GetPatternLineKey())
	if techCardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	if !entity.ValidTechCardSeamKey(lineKey) {
		return nil, status.Error(codes.InvalidArgument, "pattern_line_key must be a 26-char ULID")
	}
	row, err := s.repo.TechCards().GetTechCardPatternSemanticSidecar(ctx, techCardID, lineKey)
	if err != nil {
		return nil, patternSemanticSidecarError(ctx, "read", techCardID, err)
	}
	return &pb_admin.GetTechCardPatternSemanticSidecarResponse{
		SemanticSidecar: patternSemanticSidecarToPb(row),
	}, nil
}
