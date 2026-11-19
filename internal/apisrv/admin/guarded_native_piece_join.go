package admin

import (
	"context"
	"path"
	"slices"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patternjoin"
	pb "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) prepareGuardedNativePieceJoin(ctx context.Context, id, lock int, req *pb.GuardedNativePieceJoin) (*entity.TechCardInsert, error) {
	old := req.GetExpectedRow()
	a, b := req.GetFixed(), req.GetMoving()
	if id <= 0 || lock <= 0 || old == nil || int(old.GetTechCardId()) != id || int(old.GetCardLockVersion()) != lock || old.GetPatternVersion() <= 0 || a == nil || b == nil ||
		req.GetReviewedFilename() == "" || strings.TrimSpace(req.ReviewedFilename) != req.ReviewedFilename || path.Base(req.ReviewedFilename) != req.ReviewedFilename || strings.ContainsAny(req.ReviewedFilename, "\\\x00") || !strings.HasSuffix(strings.ToLower(req.ReviewedFilename), ".dxf") {
		return nil, status.Error(codes.InvalidArgument, "send the exact saved pattern, two selected boundaries and a reviewed DXF")
	}
	managed, ok := s.bucket.(*bucket.Bucket)
	if !ok || managed == nil || managed.Config == nil {
		return nil, status.Error(codes.FailedPrecondition, "managed pattern storage is unavailable")
	}
	row := entity.SavedPatternRowIdentity{
		TechCardID: id, ScopeKey: old.GetScopeKey(), PatternLineKey: old.GetPatternLineKey(),
		SavedRowRevision: old.GetSavedRowRevision(), CardLockVersion: lock, PatternVersion: int(old.GetPatternVersion()),
		URL: old.GetUrl(), SizeBytes: old.GetSizeBytes(), SHA256: old.GetSha256(),
	}
	doc := patternjoin.Document{
		Row: row, AssemblyRevision: req.GetExpectedAssemblyRevision(), CompleteSourceSHA256: req.GetExpectedCompleteSourceSha256(),
		FixedLineKey: a.GetPieceLineKey(), MovingLineKey: b.GetPieceLineKey(), ResultLineKey: req.GetResultLineKey(),
	}
	edge := func(e *pb.GuardedNativePieceJoin_Boundary) patternjoin.DirectedEdge {
		return patternjoin.DirectedEdge{Curve: e.GetCurveId(), Start: e.GetStartVertexId(), End: e.GetEndVertexId()}
	}
	selected := patternjoin.Selection{
		CardID: id, FabricPurpose: req.GetFabricPurpose(), BOMLineKey: req.GetBomLineKey(), SizeToken: req.GetSizeToken(), SizeID: int(req.GetSizeId()),
		Fixed: a.GetPieceIdentity(), Moving: b.GetPieceIdentity(), Result: req.GetResultIdentity(),
		FixedInterval: edge(a), MovingInterval: edge(b), FixedEdge: 1, MovingEdge: 3,
		TranslateX: req.GetTranslateXMillionthsMm(), TranslateY: req.GetTranslateYMillionthsMm(),
	}
	asset := patternjoin.Asset{URL: req.GetReviewedUrl(), Size: req.GetReviewedSizeBytes(), SHA256: req.GetReviewedSha256()}
	stored, err := s.repo.TechCards().GetTechCardByIdConsistent(ctx, id)
	if err != nil {
		return nil, s.techCardWriteError(ctx, err)
	}
	if stored == nil || stored.LockVersion != lock {
		return nil, s.techCardWriteError(ctx, entity.ErrTechCardConflict)
	}
	if stored.ApprovalState == entity.TechCardApprovalReleased {
		return nil, s.techCardWriteError(ctx, entity.ErrTechCardReleased)
	}
	body := dto.ConvertEntityTechCardToPb(stored, s.costingFx(ctx))
	// Ordinary read projections include the complete machine/pressing vocabulary.
	projected := body.GetTechCard()
	projected.AssemblyAware = true
	projected.MachineFieldsAware = true
	projected.LabelsAware = true
	projected.OperationKindsAware = true
	projected.OperationWorkAware = true
	if _, can := s.costingAccess(ctx); !can {
		stripTechCardCosting(body)
	}
	join := &entity.GuardedNativePieceJoin{Document: doc, Selection: selected, Reviewed: asset, Filename: req.GetReviewedFilename()}
	if err := projectNativeJoin(body.GetTechCard(), join); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "source lines or pattern no longer match the reviewed Join")
	}
	tc, err := s.prepareTechCardWriteFrom(ctx, id, body.GetTechCard(), "", techCardWriteSource{stored: stored})
	if err != nil {
		return nil, err
	}
	join.ManagedPatterns = managed
	tc.GuardedPieceJoin = join
	return tc, nil
}

func projectNativeJoin(body *common.TechCardInsert, j *entity.GuardedNativePieceJoin) error {
	d := j.Document
	r := d.Row
	bad := entity.ErrTechCardSavedAssemblyUnsupported
	if body == nil || body.PieceDxfAliases == nil {
		return bad
	}
	keys := []string{d.FixedLineKey, d.MovingLineKey, d.ResultLineKey, r.PatternLineKey}
	for i, k := range keys {
		if !entity.ValidTechCardSeamKey(k) || slices.Contains(keys[:i], k) {
			return bad
		}
	}
	found := map[string]int{}
	var kept []*common.TechCardPiece
	for _, p := range body.Pieces {
		switch p.GetLineKey() {
		case d.FixedLineKey, d.MovingLineKey:
			found[p.GetLineKey()]++
		case d.ResultLineKey:
			return bad
		default:
			kept = append(kept, p)
		}
	}
	if found[d.FixedLineKey] != 1 || found[d.MovingLineKey] != 1 {
		return bad
	}
	symmetry := common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_IDENTICAL
	body.Pieces = append(kept, &common.TechCardPiece{LineKey: d.ResultLineKey, Name: j.Selection.Result, PiecesPerGarment: 1, CutSymmetry: &symmetry})
	var alias *common.TechCardPieceDxfAlias
	body.PieceDxfAliases.Items = slices.DeleteFunc(body.PieceDxfAliases.Items, func(a *common.TechCardPieceDxfAlias) bool {
		return a.GetPieceLineKey() == d.FixedLineKey || a.GetPieceLineKey() == d.MovingLineKey
	})
	for _, p := range body.Patterns {
		if p.GetLineKey() != r.PatternLineKey {
			continue
		}
		if alias != nil || p.GetUrl() != r.URL || p.GetSha256() != r.SHA256 || p.GetSizeBytes() != r.SizeBytes || int(p.GetVersion()) != r.PatternVersion || p.GetSavedRowRevision() != r.SavedRowRevision {
			return bad
		}
		alias = &common.TechCardPieceDxfAlias{BlockName: j.Selection.Result, PieceLineKey: d.ResultLineKey, BomLineKey: p.GetBomLineKey(), FabricPurpose: p.GetFabricPurpose()}
		p.Url = j.Reviewed.URL
		p.Sha256 = &j.Reviewed.SHA256
		p.SizeBytes = j.Reviewed.Size
		p.Filename = j.Filename
	}
	if alias == nil {
		return bad
	}
	body.PieceDxfAliases.Items = append(body.PieceDxfAliases.Items, alias)
	return nil
}
