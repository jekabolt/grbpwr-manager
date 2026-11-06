package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Packaging items are full-replaced and the packaging sheet is replaced whole. A payload from a bundle
// that has never heard of the picks (0448: material_id, aux_tech_card_id, per_order, colorway_ids,
// packed_size, packed_media_id) cannot be told apart from «this card has none», so its save would
// silently erase them. Refuse such a write instead (packaging_picks_aware, 133).
//
// Stored gate only, and a payload that carries any of the fields passes: it has plainly heard of them.
// So what a client without the flag cannot do is clear the last of them, and it is told to reload.

const outdatedPackagingPicksClientFix = "this version of the admin panel does not know the packaging picks, and its save replaces the whole packaging list — reload the page (hard-refresh) and try again"

func packagingPicksStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetPackagingPicksAware() || payloadCarriesPackagingPicks(pb) || !storedPackagingPicksAtRisk(pb, stored) {
		return nil
	}
	slog.Default().Warn("packaging picks gate refused an outdated bundle against a card that has them",
		slog.String("gate", "stored"), slog.String("cell", "stored:picks/aware:no"),
		slog.Int("tech_card_id", storedCardID(stored)))
	return status.Error(codes.FailedPrecondition,
		"outdated admin client: this tech card has packaging picks; "+outdatedPackagingPicksClientFix)
}

func payloadCarriesPackagingPicks(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	if p := pb.GetPackaging(); p.GetPackedSize() != "" || p.GetPackedMediaId() != 0 || len(p.GetPackedMediaIds()) > 0 {
		return true
	}
	for _, it := range pb.GetPackagingItems() {
		if it.GetMaterialId() != 0 || it.GetAuxTechCardId() != 0 || it.GetPerOrder() || len(it.GetColorwayIds()) > 0 {
			return true
		}
	}
	return false
}

// storedPackagingPicksAtRisk asks only about what this write replaces: without labels_aware the store
// keeps the items, and an absent packaging sheet stays as it is.
func storedPackagingPicksAtRisk(pb *pb_common.TechCardInsert, stored *entity.TechCard) bool {
	if pb == nil || stored == nil {
		return false
	}
	if pb.GetPackaging() != nil && stored.Packaging != nil &&
		(stored.Packaging.PackedSize.Valid || stored.Packaging.PackedMediaId.Valid) {
		return true
	}
	if !pb.GetLabelsAware() {
		return false
	}
	for i := range stored.PackagingItems {
		it := &stored.PackagingItems[i]
		if it.HasPick() || it.PerOrder || len(it.ColorwayIds) > 0 {
			return true
		}
	}
	return false
}
