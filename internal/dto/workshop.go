package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/shopspring/decimal"
)

// WorkshopSettingsToPb maps the shop-floor configuration onto the wire. An unset setting travels as
// an ABSENT field, never as a zero — the whole point of the column being NULL-able (see
// entity.WorkshopSettings).
func WorkshopSettingsToPb(s *entity.WorkshopSettings) *pb_admin.WorkshopSettings {
	if s == nil {
		return &pb_admin.WorkshopSettings{}
	}
	out := &pb_admin.WorkshopSettings{
		UpdatedBy: s.UpdatedBy,
	}
	if s.CuttingTableLengthCm.Valid {
		out.CuttingTableLengthCm = &pb_decimal.Decimal{Value: s.CuttingTableLengthCm.Decimal.String()}
	}
	// Ф3.2. Emitted only when SET, so a configured ZERO travels as "0" and an unconfigured setting
	// travels as an absent field — the one distinction this whole tenant exists to preserve.
	if s.DefaultSeamAllowanceMm.Valid {
		out.DefaultSeamAllowanceMm = &pb_decimal.Decimal{Value: s.DefaultSeamAllowanceMm.Decimal.String()}
	}
	// Ф6.9. Emitted only when SET, for the same presence reason as the decimals above — a workshop
	// that never touched the switch must stay distinguishable from one that turned it off. The
	// BEHAVIOUR of the two is identical (report-only); the distinction is an audit fact, and the
	// screen prints «не настроено» rather than «выключено» for the absent case.
	if s.RunReadinessBlocking.Valid {
		v := s.RunReadinessBlocking.Bool
		out.RunReadinessBlocking = &v
	}
	// Ф4.8. Absent when unconfigured, and this one has teeth: a consumer that received "0" would
	// compute «предел 0 см» and fail every настил in the shop, where an absent field makes it withhold
	// the height verdict entirely and tell the operator which half is missing.
	if s.MaxStackHeightCm.Valid {
		out.MaxStackHeightCm = &pb_decimal.Decimal{Value: s.MaxStackHeightCm.Decimal.String()}
	}
	if !s.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(s.UpdatedAt)
	}
	return out
}

// WorkshopSettingsPatchFromPb reads the tri-state patch off an update request.
//
// PRESENCE, not value. A nil field means the caller did not mention the setting and the stored value
// must be carried forward; a present-but-empty google.type.Decimal means "clear it". Collapsing
// those two — which the shared nullDecimalFromPb helper does, since it treats nil and "" alike —
// would make every client that predates a setting silently wipe it on save.
func WorkshopSettingsPatchFromPb(req *pb_admin.UpdateWorkshopSettingsRequest) (entity.WorkshopSettingsPatch, error) {
	var p entity.WorkshopSettingsPatch
	if req == nil {
		return p, nil
	}
	v, err := presentNullDecimal(req.CuttingTableLengthCm, "cutting_table_length_cm")
	if err != nil {
		return p, err
	}
	p.CuttingTableLengthCm = v
	seam, err := presentNullDecimal(req.DefaultSeamAllowanceMm, "default_seam_allowance_mm")
	if err != nil {
		return p, err
	}
	p.DefaultSeamAllowanceMm = seam
	// Ф4.8 — same tri-state, same helper. Going through presentNullDecimal rather than the shared
	// nullDecimalFromPb is the whole safety of this screen: the latter folds nil and "" together, and a
	// workshop tab from before this setting existed sends nil on every save of the table length.
	stack, err := presentNullDecimal(req.MaxStackHeightCm, "max_stack_height_cm")
	if err != nil {
		return p, err
	}
	p.MaxStackHeightCm = stack
	// Ф6.9 — presence carried by proto3 `optional`, which is the ONLY reason this is safe to add. On
	// a bare bool, «did not send it» and «turn it off» are the same bytes, and a workshop tab holding
	// a bundle from before this field existed would disable the gate on every unrelated save.
	if req.RunReadinessBlocking != nil {
		v := req.GetRunReadinessBlocking()
		p.RunReadinessBlocking = &v
	}
	return p, nil
}

// presentNullDecimal turns a proto decimal into the tri-state pointer the entity patch expects:
// nil field -> nil (absent), empty value -> a non-nil invalid NullDecimal (clear), a parsable
// number -> a non-nil valid one.
func presentNullDecimal(d *pb_decimal.Decimal, field string) (*decimal.NullDecimal, error) {
	if d == nil {
		return nil, nil
	}
	if d.GetValue() == "" {
		return &decimal.NullDecimal{}, nil
	}
	v, err := decimal.NewFromString(d.GetValue())
	if err != nil {
		return nil, entity.NewFieldViolation(field, "not_a_number", d.GetValue(),
			fmt.Sprintf("enter a decimal number, e.g. 600 (%v)", err))
	}
	return &decimal.NullDecimal{Decimal: v, Valid: true}, nil
}
