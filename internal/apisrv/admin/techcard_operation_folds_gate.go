package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Operations are full-replaced and have no stable row key. A payload from a bundle that has never
// heard of TechCardOperation.fold_keys (0444) cannot be told apart from «no step closes a fold», so it
// would silently leave every piece flat. Refuse such a write instead (operation_folds_aware, 130),
// the same shield as the seam links.

const outdatedOperationFoldsClientFix = "this version of the admin panel does not know which folds close on each step, and its save replaces the whole step list — reload the page (hard-refresh) and try again"

func outdatedOperationFoldsClient(reason string) error {
	return status.Error(codes.FailedPrecondition,
		"outdated admin client: "+reason+"; "+outdatedOperationFoldsClientFix)
}

func operationFoldsWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetOperationFoldsAware() || !payloadCarriesOperationFolds(pb) {
		return nil
	}
	slog.Default().Warn("operation folds gate refused an unaware payload that carries fold keys",
		slog.String("gate", "wire"), slog.String("cell", "payload:folds/aware:no"))
	return outdatedOperationFoldsClient("the payload closes folds on a step without declaring support for it")
}

func operationFoldsStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetOperationFoldsAware() || !storedHasOperationFolds(stored) {
		return nil
	}
	slog.Default().Warn("operation folds gate refused an outdated bundle against a card whose steps close folds",
		slog.String("gate", "stored"), slog.String("cell", "stored:folds/aware:no"),
		slog.Int("tech_card_id", storedCardID(stored)))
	return outdatedOperationFoldsClient("this tech card has steps that close folds")
}

func payloadCarriesOperationFolds(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	for _, op := range pb.GetOperations() {
		if len(op.GetFoldKeys()) > 0 {
			return true
		}
	}
	return false
}

func storedHasOperationFolds(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		if len(stored.Operations[i].FoldKeys) > 0 {
			return true
		}
	}
	return false
}

// stripOperationFoldKeys drops every step's fold keys from a server-built payload whose folds do not
// travel with it (season clone: tech_card_piece_fold is not copied). The keys would name folds the
// target card does not have and the save would refuse them.
func stripOperationFoldKeys(pb *pb_common.TechCardInsert) {
	if pb == nil {
		return
	}
	for _, op := range pb.GetOperations() {
		op.FoldKeys = nil
	}
}

// restoreOperationFoldKeys cuts a snapshot's step links down to what the card's folds admit TODAY. A
// restore does not touch tech_card_piece_fold (a snapshot does not bring the fold rows back), so a key
// of a fold deleted since, a repeat, or a pair whose seq was swapped since would make the snapshot
// unrestorable if the store refused it. The link goes and the piece stays flat at that step; a fold
// deleted in the race after this read gets the store's honest refusal, and the operator retries.
func restoreOperationFoldKeys(in *pb_common.TechCardInsert, stored *entity.TechCard) {
	ops := in.GetOperations()
	steps := make([][]string, len(ops))
	wanted := false
	for i, op := range ops {
		steps[i] = op.GetFoldKeys()
		wanted = wanted || len(steps[i]) > 0
	}
	if !wanted {
		return
	}
	refs := make(map[string]entity.TechCardFoldOrderRef, len(stored.PieceFolds))
	for _, f := range stored.PieceFolds {
		refs[entity.NormalizeTechCardSeamKey(f.FoldKey)] = entity.TechCardFoldOrderRef{PieceLineKey: f.PieceLineKey, Seq: f.Seq}
	}
	cut, _ := entity.CutTechCardFoldLinks(steps, refs)
	for i, op := range ops {
		if op != nil {
			op.FoldKeys = cut[i]
		}
	}
}
