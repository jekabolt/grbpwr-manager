package admin

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Иконные поля шага (0436): TechCardOperation.seam_kind (72), TechCardOperation.work_variant (73) и
// TechCardOperationFastening.button_holes (7). Operations are full-replaced and have no stable row
// key, so a payload from a bundle that has never heard of these fields cannot be told apart from
// «no step states them», and its save would silently erase every value. Refuse such a write instead
// (operation_seam_kind_aware, 129) — the same shield as operation_work_aware (116), word for word:
//
//   - wire: an unaware payload that nevertheless carries a value is an echo, refused;
//   - stored: an unaware write against a card whose steps hold any value is refused;
//   - a card where no step holds any value (today: every card) saves from an old bundle as before.
//
// The flag does not filter parsing; server-built payloads (season clone, archive, restore, beta
// seed) set it explicitly.

const outdatedOperationSeamKindClientFix = "this version of the admin panel does not know the seam construction, the work variant or the button holes of a step, and its save replaces the whole step list — reload the page (hard-refresh) and try again"

func outdatedOperationSeamKindClient(reason string) error {
	return status.Error(codes.FailedPrecondition,
		"outdated admin client: "+reason+"; "+outdatedOperationSeamKindClientFix)
}

func operationSeamKindWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetOperationSeamKindAware() || !payloadCarriesOperationIconFields(pb) {
		return nil
	}
	slog.Default().Warn("operation seam kind gate refused an unaware payload that carries icon fields",
		slog.String("gate", "wire"), slog.String("cell", "payload:icon_fields/aware:no"))
	return outdatedOperationSeamKindClient("the payload states a seam construction, a work variant or button holes without declaring support for them")
}

func operationSeamKindStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetOperationSeamKindAware() || !storedHasOperationIconFields(stored) {
		return nil
	}
	slog.Default().Warn("operation seam kind gate refused an outdated bundle against a card whose steps state icon fields",
		slog.String("gate", "stored"), slog.String("cell", "stored:icon_fields/aware:no"),
		slog.Int("tech_card_id", storedCardID(stored)))
	return outdatedOperationSeamKindClient("this tech card has steps with a seam construction, a work variant or button holes")
}

func payloadCarriesOperationIconFields(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	for _, op := range pb.GetOperations() {
		if op.GetSeamKind() != "" || op.GetWorkVariant() != "" || op.GetFastening().GetButtonHoles() != 0 {
			return true
		}
	}
	return false
}

func storedHasOperationIconFields(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	for i := range stored.Operations {
		op := &stored.Operations[i]
		if op.SeamKind.Valid || op.WorkVariant.Valid || op.ButtonHoles.Valid {
			return true
		}
	}
	return false
}
