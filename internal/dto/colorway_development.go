package dto

import (
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// devMaskPrefix is the update_mask namespace of the colourway development block. A mask entry is
// either the bare prefix (write the whole block) or "<prefix>.<leaf>".
const devMaskPrefix = "development"

// devFieldAliases maps every spelling of a development leaf onto its canonical proto field name.
// Both are accepted because a JSON/REST caller naturally sends the camelCase field name it sees on
// the wire, while a gRPC caller sends the proto field name — and a mask that silently matches
// neither would drop the write, which is exactly the failure this whole path exists to fix.
var devFieldAliases = map[string]string{
	"dev_code": "dev_code", "devcode": "dev_code",
	"name":           "name",
	"lab_dip_status": "lab_dip_status", "labdipstatus": "lab_dip_status",
	"comment":        "comment",
	"pantone":        "pantone",
	"pantone_system": "pantone_system", "pantonesystem": "pantone_system",
	"dev_hex": "dev_hex", "devhex": "dev_hex",
	"swatch_media_id": "swatch_media_id", "swatchmediaid": "swatch_media_id",
	"lab_dip_round": "lab_dip_round", "labdipround": "lab_dip_round",
	"lab_dip_submitted_at": "lab_dip_submitted_at", "labdipsubmittedat": "lab_dip_submitted_at",
	"lab_dip_decided_at": "lab_dip_decided_at", "labdipdecidedat": "lab_dip_decided_at",
	"lab_dip_decided_by": "lab_dip_decided_by", "labdipdecidedby": "lab_dip_decided_by",
	"lab_dip_reject_reason": "lab_dip_reject_reason", "labdiprejectreason": "lab_dip_reject_reason",
	"display_order": "display_order", "displayorder": "display_order",
}

// devMaskSelection is the set of development leaves an update_mask selects. selectAll is the
// "no mask, or a mask naming the whole block" case.
type devMaskSelection struct {
	selectAll bool
	leaves    map[string]bool
}

func (s devMaskSelection) has(field string) bool { return s.selectAll || s.leaves[field] }

// parseDevMask reads the development namespace out of an update_mask. A nil/empty mask selects the
// whole block: a caller that sends `development` without a mask means all of it.
func parseDevMask(mask *fieldmaskpb.FieldMask) devMaskSelection {
	paths := mask.GetPaths()
	if len(paths) == 0 {
		return devMaskSelection{selectAll: true}
	}
	sel := devMaskSelection{leaves: map[string]bool{}}
	for _, p := range paths {
		norm := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), "_", ""))
		head, tail, hasTail := strings.Cut(norm, ".")
		if head != devMaskPrefix {
			continue
		}
		if !hasTail {
			return devMaskSelection{selectAll: true}
		}
		if canonical, ok := devFieldAliases[tail]; ok {
			sel.leaves[canonical] = true
		}
	}
	return sel
}

// ColorwayDevelopmentPatchFromPb builds the development patch a colourway write applies, honouring
// update_mask. It returns nil when the request carries no development block or the mask selects
// nothing under it — nil means "leave the colourway's PLM state alone", which is what every caller
// that does not know about lab dips wants.
//
// `usages` is deliberately NOT read here: the material recipe is owned by UpdateColorwayRecipe, which
// resolves BOM line keys to real FKs. Accepting it on two paths would give it two write semantics.
func ColorwayDevelopmentPatchFromPb(dev *pb_common.ColorwayDevelopmentInsert, mask *fieldmaskpb.FieldMask) *entity.ColorwayDevelopmentPatch {
	if dev == nil {
		return nil
	}
	sel := parseDevMask(mask)
	p := &entity.ColorwayDevelopmentPatch{}
	if sel.has("dev_code") {
		v := dev.GetDevCode()
		p.DevCode = &v
	}
	if sel.has("name") {
		v := dev.GetName()
		p.Name = &v
	}
	if sel.has("lab_dip_status") {
		v, ok := techCardLabDipPbToEntity[dev.GetLabDipStatus()]
		if !ok {
			// UNKNOWN on the wire means "not stated". Defaulting it to PENDING here would silently
			// reset a colourway that is already APPROVED, so leave the stored status alone instead.
			v = ""
		}
		if v != "" {
			p.LabDipStatus = &v
		}
	}
	if sel.has("comment") {
		v := dev.GetComment()
		p.Comment = &v
	}
	if sel.has("pantone") {
		v := dev.GetPantone()
		p.Pantone = &v
	}
	if sel.has("pantone_system") {
		v := dev.GetPantoneSystem()
		p.PantoneSystem = &v
	}
	if sel.has("dev_hex") {
		v := dev.GetDevHex()
		p.DevHex = &v
	}
	if sel.has("swatch_media_id") {
		v := int(dev.GetSwatchMediaId())
		p.SwatchMediaId = &v
	}
	if sel.has("lab_dip_round") {
		v := int(dev.GetLabDipRound())
		p.LabDipRound = &v
	}
	// lab_dip_submitted_at / lab_dip_decided_at / lab_dip_decided_by are read-only audit
	// fields. The store stamps fresh lifecycle transitions from database time plus the actor the
	// authenticated handler attaches; accepting these wire values would make both current state and
	// the round journal forgeable.
	if sel.has("lab_dip_reject_reason") {
		v := dev.GetLabDipRejectReason()
		p.LabDipRejectReason = &v
	}
	if sel.has("display_order") {
		v := int(dev.GetDisplayOrder())
		p.DisplayOrder = &v
	}
	if p.IsEmpty() {
		return nil
	}
	return p
}

// ColorwayLabDipRoundsToPb projects a colourway's round journal to the wire, oldest first.
func ColorwayLabDipRoundsToPb(rounds []entity.ColorwayLabDipRound) []*pb_common.ColorwayLabDipRound {
	if len(rounds) == 0 {
		return nil
	}
	out := make([]*pb_common.ColorwayLabDipRound, 0, len(rounds))
	for _, r := range rounds {
		item := &pb_common.ColorwayLabDipRound{
			RoundNumber:   int32(r.RoundNumber),
			Status:        pbLabDipStatus(r.Status),
			DecidedBy:     r.DecidedBy.String,
			RejectReason:  r.RejectReason.String,
			Comment:       r.Comment.String,
			SwatchMediaId: r.SwatchMediaId.Int32,
		}
		if r.SubmittedAt.Valid {
			item.SubmittedAt = timestamppb.New(r.SubmittedAt.Time)
		}
		if r.DecidedAt.Valid {
			item.DecidedAt = timestamppb.New(r.DecidedAt.Time)
		}
		if !r.CreatedAt.IsZero() {
			item.CreatedAt = timestamppb.New(r.CreatedAt)
		}
		out = append(out, item)
	}
	return out
}
