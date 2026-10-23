package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// restoreCarryDigest stands in for the signed digest of a stored approval that has none (an approval
// older than digests). The sign-off reconcile carries an approved row only when the payload names a
// digest; the value itself is never used — the carried row keeps the STORED digest (NULL stays NULL,
// i.e. stale). Without it a restore would mint a fresh approval in the restoring user's name.
const restoreCarryDigest = "restore-carry"

// RestoreTechCardSnapshot brings a card back to one of its own tech_card_snapshot rows.
//
// NOT A SECOND WRITE PATH. The snapshot is the card's read model (protojson of common.TechCard); its
// TechCardInsert is turned into a server-built payload and sent through prepareTechCardWriteFrom and
// UpdateTechCardAndListOrphanedPatternURLs — the same gates, carries, store transaction, revision line
// and post-commit work as UpdateTechCard. The transaction first freezes the CURRENT card as a new
// snapshot (reason «restore:<id>»), so a restore is itself undoable, and the sheets it replaces go to
// the history pointing at that new snapshot.
//
// SEAMS. tech_card_seam is not part of the card payload; the save carries the snapshot's seam list
// (restoreSeamsFromSnapshot) and the store brings the table to it in the same transaction, through
// the seam RPCs' own write and delete. Still outside lock_version and every digest.
//
// SCOPE SIDECARS. They are likewise absent from the snapshot payload. UpdateTechCardTx applies its
// normal prospective-graph rekey/dead-scope cleanup after restoring the BOM and sheets, so current
// size-index and piece-area rows cannot remain filed under a scope the restored card no longer has.
//
// What the restore does NOT take from the snapshot is the card's identity and lifecycle — approval
// state and its stamps, sign-offs, stage, style number and its source, season, category, purpose,
// aux subtype, output material, measurement unit: those are decisions taken about the card since,
// not content a DXF reconciliation changed, and rewinding them would rewind a release or a sign-off.
func (s *Server) RestoreTechCardSnapshot(ctx context.Context, req *pb_admin.RestoreTechCardSnapshotRequest) (*pb_admin.RestoreTechCardSnapshotResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	if req.GetSnapshotId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "snapshot id is required")
	}
	id := int(req.GetTechCardId())
	if cache.IsModelPhotoTechCard(id) {
		return nil, status.Error(codes.FailedPrecondition, entity.ErrTechCardSystem.Error())
	}
	snap, err := s.repo.TechCards().GetTechCardSnapshot(ctx, id, req.GetSnapshotId())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "snapshot not found on this tech card")
		}
		slog.Default().ErrorContext(ctx, "can't load tech card snapshot",
			slog.Int("tech_card_id", id), slog.Int64("snapshot_id", req.GetSnapshotId()),
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load snapshot; try again")
	}
	frozen, err := decodeTechCardSnapshot(snap)
	if err != nil {
		slog.Default().WarnContext(ctx, "tech card snapshot is unreadable",
			slog.Int("tech_card_id", id), slog.Int64("snapshot_id", req.GetSnapshotId()),
			slog.String("err", err.Error()))
		return nil, status.Error(codes.FailedPrecondition, "this snapshot cannot be restored: "+err.Error())
	}
	stored, err := s.repo.TechCards().GetTechCardByIdConsistent(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't load tech card before restore",
			slog.Int("tech_card_id", id), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card; try again")
	}
	if stored == nil {
		return nil, status.Error(codes.Internal, "can't load tech card; try again")
	}
	// The store's own order (UpdateTechCardTx): frozen before stale, so a released card hears «re-open
	// to draft» rather than «reload». Both are refused again inside the transaction.
	if stored.ApprovalState == entity.TechCardApprovalReleased {
		return nil, s.techCardWriteError(ctx, entity.ErrTechCardReleased)
	}
	if stored.LockVersion != int(req.GetExpectedLockVersion()) {
		return nil, s.techCardWriteError(ctx, entity.ErrTechCardConflict)
	}

	fx := s.costingFx(ctx)
	current := dto.ConvertEntityTechCardToPb(stored, fx).GetTechCard()
	in, err := restoreTechCardPayload(frozen.GetTechCard(), stored, current)
	if err != nil {
		return nil, err
	}
	if _, canWriteCosting := s.costingAccess(ctx); !canWriteCosting {
		// The payload may not carry money (prepare refuses it), and the stored money the pipeline puts
		// back is the CURRENT one. That is a faithful restore only if the snapshot's money is the same;
		// otherwise restoring would silently keep prices the snapshot does not have.
		if !restoreCostingMatches(frozen.GetTechCard(), current) {
			return nil, status.Error(codes.PermissionDenied,
				"costing:write is required to restore this snapshot: its costing differs from the card's")
		}
		stripTechCardCosting(&pb_common.TechCard{TechCard: in})
	}
	tc, err := s.prepareTechCardWriteFrom(ctx, id, in, fmt.Sprintf("restore:%d", snap.Id), techCardWriteSource{
		stored:    stored,
		readdress: restoreLabelBomLinks(frozen.GetTechCard().GetBomItems()),
	})
	if err != nil {
		return nil, err
	}
	tc.SnapshotChangedPaths = []string{"restored " + snap.CreatedAt.UTC().Format("02.01 15:04")}
	tc.SnapshotPinned = true
	if tc.RestoreSeams, err = restoreSeamsFromSnapshot(id, snap.Snapshot, frozen, authsrv.GetAdminUsername(ctx)); err != nil {
		return nil, err
	}
	orphanedPatternURLs, err := s.repo.TechCards().UpdateTechCardAndListOrphanedPatternURLs(
		ctx, id, tc, int(req.GetExpectedLockVersion()))
	if err != nil {
		return nil, s.techCardWriteError(ctx, err)
	}
	s.finalizeTechCardWrite(ctx, id, int(req.GetExpectedLockVersion()), orphanedPatternURLs)
	s.designBoardLabelKick(ctx, id)
	card, err := s.readTechCardForResponse(ctx, id, "")
	if err != nil {
		return nil, err
	}
	return &pb_admin.RestoreTechCardSnapshotResponse{TechCard: card}, nil
}

// decodeTechCardSnapshot parses the frozen read model. DiscardUnknown: a snapshot written by a newer
// binary than the one reading it (a rollback) still restores what this binary understands — every
// field this binary can write is one it can also read.
func decodeTechCardSnapshot(snap *entity.TechCardSnapshot) (*pb_common.TechCard, error) {
	if snap == nil || strings.TrimSpace(snap.Snapshot) == "" {
		return nil, errors.New("the snapshot is empty")
	}
	var card pb_common.TechCard
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(snap.Snapshot), &card); err != nil {
		return nil, fmt.Errorf("the snapshot is not a readable card: %w", err)
	}
	if card.GetTechCard() == nil {
		return nil, errors.New("the snapshot carries no card contract")
	}
	if card.GetId() != 0 && int(card.GetId()) != snap.TechCardId {
		return nil, errors.New("the snapshot belongs to another card")
	}
	return &card, nil
}

// restoreSeamsFromSnapshot is the seam list the restore brings the card back to, or nil to leave the
// seams as they are.
//
// ABSENT ≠ EMPTY. The snapshot is protojson, which omits an empty repeated field, so a parsed
// TechCard cannot tell «no seams» from «written before seams were frozen». The raw JSON can: the
// snapshot writer (marshalTechCardSnapshot) always names `seams`, as `[]` when the card had none.
// A snapshot without the key is an older one — the seams then are unknown, and unknown is not
// «delete them all»: nil, the restore does not touch tech_card_seam.
func restoreSeamsFromSnapshot(techCardID int, raw string, frozen *pb_common.TechCard, by string) (*entity.TechCardSeamsRestore, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "this snapshot cannot be restored: "+err.Error())
	}
	if _, ok := top[snapshotSeamsField]; !ok {
		return nil, nil
	}
	out := &entity.TechCardSeamsRestore{Stale: map[string]bool{}}
	if len(frozen.GetSeams()) == 0 {
		return out, nil
	}
	// The same conversion and validation as UpsertTechCardSeams (it normalises the keys).
	in, err := dto.TechCardSeamsWriteFromPb(techCardID, frozen.GetSeams(), by)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "this snapshot's seams cannot be restored: "+err.Error())
	}
	out.Seams = in.Seams
	for _, seam := range frozen.GetSeams() {
		if seam.GetStale() {
			out.Stale[entity.NormalizeTechCardSeamKey(seam.GetSeamKey())] = true
		}
	}
	return out, nil
}

// restoreTechCardPayload builds the UpdateTechCard payload that makes the card read like `frozen`.
//
// The read model is not a write payload as it is: absence on the wire means «keep what is stored»
// for a dozen fields (proto3 optional, nil sections, the alias wrapper), and the reader emits nil for
// a NULL in several of them. Echoed verbatim, a field that was empty in the snapshot would keep
// whatever the card has now. Every such absence is turned into an explicit «this is empty» here, and
// TestRestorePayloadSpeaksEveryField locks that no presence flag survives the conversion.
func restoreTechCardPayload(frozen *pb_common.TechCardInsert, stored *entity.TechCard, current *pb_common.TechCardInsert) (*pb_common.TechCardInsert, error) {
	if frozen == nil || stored == nil || current == nil {
		return nil, status.Error(codes.Internal, "can't build the restore payload")
	}
	in := proto.Clone(frozen).(*pb_common.TechCardInsert)

	// Identity and lifecycle stay as they are now (see RestoreTechCardSnapshot).
	in.StyleNumber = current.GetStyleNumber()
	in.StyleNumberSource = current.GetStyleNumberSource()
	in.SkuSeason = current.GetSkuSeason()
	in.CategoryId = current.GetCategoryId()
	in.Purpose = current.GetPurpose()
	in.AuxSubtype = current.GetAuxSubtype()
	in.OutputMaterialId = current.GetOutputMaterialId()
	in.Stage = current.GetStage()
	in.ApprovalState = current.GetApprovalState()
	in.ApprovedAt = current.GetApprovedAt()
	in.ReleasedAt = current.GetReleasedAt()
	// The unit describes the size chart, which a snapshot does not hold: the chart stays, so must its unit.
	in.MeasurementUnit = current.GetMeasurementUnit()
	in.Signoffs = restoreCarriedSignoffs(current.GetSignoffs())

	// The size chart, quantities and per-size facts outside the payload are pruned to the payload's
	// range by the store. A snapshot with fewer sizes would delete chart rows it cannot give back.
	if dropped := sizesDroppedBy(stored.SizeIds, in.GetSizeIds()); len(dropped) > 0 {
		return nil, status.Errorf(codes.FailedPrecondition,
			"this snapshot has fewer sizes than the card (size ids %v); restoring it would delete their measurements — add those sizes back is not possible from a snapshot, so remove them from the card first if that is intended", dropped)
	}

	// The payload is built by the server from its own read: it knows every field by construction.
	in.MachineFieldsAware = true
	in.AssemblyAware = true
	in.MediaAware = true
	in.OperationKindsAware = true
	in.OperationWorkAware = true
	in.OperationDraftAware = true
	in.OperationReviewAware = true
	in.OperationBlockMembershipAware = true
	in.BomQtyAware = true
	in.LabelsAware = true
	// The backstops refuse an aware payload that would erase stored facts without saying so. Here the
	// erasure IS the request (the snapshot had none), so it is said.
	in.AssemblyCleared = storedHasAssemblyFacts(stored) && !payloadCarriesAssemblyUnits(in)
	in.MediaCleared = storedHasOperationMedia(stored) && !payloadCarriesOperationMedia(in)

	// Optional header prose: nil = keep, so an empty snapshot value must be said.
	if in.MoodNote == nil {
		in.MoodNote = proto.String("")
	}
	if in.GarmentDescription == nil {
		in.GarmentDescription = proto.String("")
	}
	if in.FlatWords == nil {
		in.FlatWords = proto.String("")
	}
	if in.PieceDxfAliases == nil {
		in.PieceDxfAliases = &pb_common.TechCardPieceDxfAliasSet{}
	}
	// 1:1 sections: nil = keep the stored record. Costing is decided by the caller (costing:write).
	if in.Construction == nil {
		in.Construction = &pb_common.TechCardConstruction{}
	}
	if in.Construction.EquipmentDefaults == nil {
		in.Construction.EquipmentDefaults = &pb_common.TechCardEquipmentDefaults{}
	}
	if in.Packaging == nil {
		in.Packaging = &pb_common.TechCardPackaging{}
	}
	if in.CareLabel == nil {
		in.CareLabel = &pb_common.TechCardCareLabel{}
	}
	if in.Costing == nil {
		in.Costing = &pb_common.TechCardCosting{}
	}
	for _, c := range in.GetCallouts() {
		if c == nil {
			continue
		}
		if c.Kind == nil {
			c.Kind = pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN.Enum()
		}
		if c.Spec == "" {
			c.Spec = "{}" // explicit «no purpose»; "" would carry the stored one
		}
	}
	for _, b := range in.GetBomItems() {
		if b == nil {
			continue
		}
		if b.Purpose == nil {
			b.Purpose = pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET.Enum()
		}
		if b.Kind == nil && b.KindNote == nil {
			b.Kind = pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET.Enum()
		}
		if b.LabelPart == nil {
			b.LabelPart = pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_UNSPECIFIED.Enum()
		}
		if b.IsSample == nil {
			b.IsSample = proto.Bool(false)
		}
		if b.FabricDirection == nil {
			b.FabricDirection = pb_common.TechCardFabricDirection_TECH_CARD_FABRIC_DIRECTION_UNKNOWN.Enum()
		}
		if b.WastageSource == nil && b.WastageLayCount == nil {
			b.WastageSource = proto.String(entity.BomWastageSourceManual)
		}
		// A Decimal has no `optional`: presence is the pointer, and the empty value is the «clear».
		if b.QtyPerGarment == nil && b.SpareQty == nil {
			b.QtyPerGarment = &pb_decimal.Decimal{}
			b.SpareQty = &pb_decimal.Decimal{}
		}
		if b.EstUsage == nil {
			b.EstUsage = &pb_decimal.Decimal{}
		}
	}
	for _, p := range in.GetPieces() {
		if p == nil {
			continue
		}
		if p.CutSymmetry == nil {
			p.CutSymmetry = pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_UNKNOWN.Enum()
		}
		if p.Ungraded == nil {
			p.Ungraded = proto.Bool(false)
		}
		if p.FusingMode == nil {
			p.FusingMode = pb_common.TechCardPieceFusingMode_TECH_CARD_PIECE_FUSING_MODE_UNKNOWN.Enum()
		}
	}
	for _, p := range in.GetPatterns() {
		if p == nil {
			continue
		}
		// name / bom_line_key / fabric_purpose: nil keeps the stored value, ""/UNSET is NULL. sha256
		// is left as read: nil keeps the stored hash only for the very same object (same url), which
		// is the same hash.
		if p.Name == nil {
			p.Name = proto.String("")
		}
		if p.BomLineKey == nil {
			p.BomLineKey = proto.String("")
		}
		if p.FabricPurpose == nil {
			p.FabricPurpose = pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET.Enum()
		}
		p.History = nil
		p.ViewUrl, p.DownloadUrl = "", ""
	}
	return in, nil
}

// restoreCarriedSignoffs echoes the card's CURRENT sign-offs so the reconcile carries every approval
// as stored (signer, time, digest) and mints none. A restore that changes approved content leaves the
// approval reading «changed since sign-off» — the honest state — instead of re-signing it silently.
func restoreCarriedSignoffs(current []*pb_common.TechCardSignoff) []*pb_common.TechCardSignoff {
	out := make([]*pb_common.TechCardSignoff, 0, len(current))
	for _, so := range current {
		if so == nil {
			continue
		}
		c := proto.Clone(so).(*pb_common.TechCardSignoff)
		if c.State == pb_common.TechCardSignoffState_TECH_CARD_SIGNOFF_STATE_APPROVED && c.SignedDigest == "" {
			c.SignedDigest = restoreCarryDigest
		}
		out = append(out, c)
	}
	return out
}

func sizesDroppedBy(stored []int, payload []int32) []int {
	keep := make(map[int]struct{}, len(payload))
	for _, id := range payload {
		keep[int(id)] = struct{}{}
	}
	var dropped []int
	for _, id := range stored {
		if _, ok := keep[id]; !ok {
			dropped = append(dropped, id)
		}
	}
	sort.Ints(dropped)
	return dropped
}

// restoreLabelBomLinks re-addresses garment labels and packaging items by BOM line KEY. The snapshot
// names their BOM line by row id; ids are stable only while the line lives, and a line deleted after
// the snapshot comes back from the restore under a new id. The store resolves a key against the ids
// of the same save (the season clone's carry, carryCloneLabels).
func restoreLabelBomLinks(frozenBom []*pb_common.TechCardBomItem) func(*entity.TechCardInsert) {
	lineKeyByID := make(map[int32]string, len(frozenBom))
	for _, b := range frozenBom {
		if b != nil && b.GetId() > 0 && b.GetLineKey() != "" {
			lineKeyByID[int32(b.GetId())] = b.GetLineKey()
		}
	}
	return func(tc *entity.TechCardInsert) {
		for i := range tc.GarmentLabels {
			l := &tc.GarmentLabels[i]
			if key, ok := lineKeyByID[l.BomItemId.Int32]; ok && l.BomItemId.Valid && l.BomLineKey == "" {
				l.BomLineKey = key
				l.BomItemId = sql.NullInt32{}
			}
		}
		for i := range tc.PackagingItems {
			it := &tc.PackagingItems[i]
			if key, ok := lineKeyByID[it.BomItemId.Int32]; ok && it.BomItemId.Valid && it.BomLineKey == "" {
				it.BomLineKey = key
				it.BomItemId = sql.NullInt32{}
			}
		}
	}
}

// restoreCostingMatches reports whether the snapshot's money equals the card's current money, field
// by writable field: the costing block and every snapshot BOM line's price and currency. Output-only
// figures (totals, FX conversions) are not compared — they move with rates, not with the card.
func restoreCostingMatches(frozen, current *pb_common.TechCardInsert) bool {
	fc, cc := frozen.GetCosting(), current.GetCosting()
	if !decimalPbEqual(fc.GetCmtCost(), cc.GetCmtCost()) ||
		!decimalPbEqual(fc.GetLogisticsCost(), cc.GetLogisticsCost()) ||
		!decimalPbEqual(fc.GetOverheadCost(), cc.GetOverheadCost()) ||
		!decimalPbEqual(fc.GetDefectPercent(), cc.GetDefectPercent()) ||
		!decimalPbEqual(fc.GetTargetMarginPct(), cc.GetTargetMarginPct()) ||
		strings.TrimSpace(fc.GetCurrency()) != strings.TrimSpace(cc.GetCurrency()) ||
		fc.GetNotes() != cc.GetNotes() {
		return false
	}
	currentByKey := make(map[string]*pb_common.TechCardBomItem, len(current.GetBomItems()))
	for _, b := range current.GetBomItems() {
		if b != nil && b.GetLineKey() != "" {
			currentByKey[b.GetLineKey()] = b
		}
	}
	for _, b := range frozen.GetBomItems() {
		if b == nil {
			continue
		}
		cur := currentByKey[b.GetLineKey()]
		if b.GetLineKey() == "" {
			cur = nil
		}
		if !decimalPbEqual(b.GetUnitPrice(), cur.GetUnitPrice()) ||
			strings.TrimSpace(b.GetCurrency()) != strings.TrimSpace(cur.GetCurrency()) {
			return false
		}
	}
	return true
}

// decimalPbEqual compares two wire decimals by value; nil and "" are both «no value».
func decimalPbEqual(a, b *pb_decimal.Decimal) bool {
	av, bv := strings.TrimSpace(a.GetValue()), strings.TrimSpace(b.GetValue())
	if av == "" || bv == "" {
		return av == bv
	}
	ad, errA := decimal.NewFromString(av)
	bd, errB := decimal.NewFromString(bv)
	if errA != nil || errB != nil {
		return av == bv
	}
	return ad.Equal(bd)
}
