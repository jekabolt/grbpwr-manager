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
