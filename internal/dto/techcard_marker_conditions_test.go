package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

func ptrStr(s string) *string { return &s }
func ptrBool(b bool) *bool    { return &b }

func mustDecimal(s string) decimal.Decimal { return decimal.RequireFromString(s) }
func mustNullDecimal(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}

// УСЛОВИЯ СЪЁМКИ (Ф3) travel with PRESENCE, and every assertion here is about a distinction the
// screen and the readiness gate both depend on.
func TestMarkerConditionsFromPayload(t *testing.T) {
	t.Run("a payload that omits them all is ACCEPTED and becomes старая норма", func(t *testing.T) {
		// The admin is an SPA: an open tab survives a deploy and a pre-Ф3 bundle sends none of these.
		// Refusing would only stop the geometry being stored; the readiness gate is what declines to
		// count an unlabelled measurement.
		out, err := ConvertPbTechCardMarkerInsertToEntity(validMarkerInsertPb())
		require.NoError(t, err)
		require.False(t, out.SeamAllowanceMm.Valid)
		require.False(t, out.ContourAllowanceMm.Valid)
		require.False(t, out.ContourLayer.Valid)
		require.False(t, out.GrainLayer.Valid)
		require.False(t, out.AllowFlip.Valid)
		require.True(t, entity.TechCardMarkerSummary{SeamAllowanceMm: out.SeamAllowanceMm}.IsLegacyNorm())
	})

	t.Run("all five are carried through", func(t *testing.T) {
		pb := validMarkerInsertPb()
		pb.SeamAllowanceMm = &pb_decimal.Decimal{Value: "1"}
		pb.ContourAllowanceMm = &pb_decimal.Decimal{Value: "0"}
		pb.ContourLayer = ptrStr("14")
		pb.GrainLayer = ptrStr("7")
		pb.AllowFlip = ptrBool(false)
		out, err := ConvertPbTechCardMarkerInsertToEntity(pb)
		require.NoError(t, err)
		require.Equal(t, "1", out.SeamAllowanceMm.Decimal.String())
		require.True(t, out.ContourAllowanceMm.Valid)
		require.True(t, out.ContourAllowanceMm.Decimal.IsZero(), "a RECORDED zero is a measurement")
		require.Equal(t, sql.NullString{String: "14", Valid: true}, out.ContourLayer)
		require.Equal(t, sql.NullString{String: "7", Valid: true}, out.GrainLayer)
		require.Equal(t, sql.NullBool{Bool: false, Valid: true}, out.AllowFlip,
			"a recorded false is a POLICY; folding it into «not recorded» would hide that flipping was banned")
	})

	t.Run("an EMPTY grain_layer survives as an empty string, not as NULL", func(t *testing.T) {
		// "" means «не разворачивать» — a decision. Collapsing it into «not recorded» would, on
		// rebuild, orient the very pieces the operator forbade orienting.
		pb := validMarkerInsertPb()
		pb.GrainLayer = ptrStr("")
		out, err := ConvertPbTechCardMarkerInsertToEntity(pb)
		require.NoError(t, err)
		require.Equal(t, sql.NullString{String: "", Valid: true}, out.GrainLayer)
	})

	t.Run("layer names are not trimmed", func(t *testing.T) {
		// They are matched literally against the layer names parsed out of the DXF when the drawing is
		// rebuilt; trimming would break that comparison for any file whose layer name carries a space.
		pb := validMarkerInsertPb()
		pb.ContourLayer = ptrStr(" 14 ")
		out, err := ConvertPbTechCardMarkerInsertToEntity(pb)
		require.NoError(t, err)
		require.Equal(t, " 14 ", out.ContourLayer.String)
	})

	t.Run("float dust in a measured allowance is rounded, not refused", func(t *testing.T) {
		// Both halves arrive as float64 from a measurement and are stored at 2 dp; refusing dust would
		// fail a save over digits the column truncates anyway.
		pb := validMarkerInsertPb()
		pb.SeamAllowanceMm = &pb_decimal.Decimal{Value: "1.0000000000000002"}
		pb.ContourAllowanceMm = &pb_decimal.Decimal{Value: "0.9999999999"}
		out, err := ConvertPbTechCardMarkerInsertToEntity(pb)
		require.NoError(t, err)
		require.Equal(t, "1", out.SeamAllowanceMm.Decimal.String())
		require.Equal(t, "1", out.ContourAllowanceMm.Decimal.String())
	})

	t.Run("a negative allowance is refused", func(t *testing.T) {
		for _, f := range []func(*pb_common.TechCardMarkerInsert){
			func(p *pb_common.TechCardMarkerInsert) { p.SeamAllowanceMm = &pb_decimal.Decimal{Value: "-1"} },
			func(p *pb_common.TechCardMarkerInsert) { p.ContourAllowanceMm = &pb_decimal.Decimal{Value: "-1"} },
		} {
			pb := validMarkerInsertPb()
			f(pb)
			_, err := ConvertPbTechCardMarkerInsertToEntity(pb)
			require.Error(t, err)
		}
	})

	t.Run("a layer name longer than the column is refused readably", func(t *testing.T) {
		pb := validMarkerInsertPb()
		long := ""
		for i := 0; i < 65; i++ {
			long += "я"
		}
		pb.ContourLayer = ptrStr(long)
		_, err := ConvertPbTechCardMarkerInsertToEntity(pb)
		require.ErrorContains(t, err, "contour_layer")
	})
}

// The double-allowance rule lives in entity and is raised by the API layer, so the dto must NOT
// refuse it — otherwise the client gets a bare InvalidArgument with no field to point at.
func TestDtoDoesNotSwallowTheDoubleAllowanceRefusal(t *testing.T) {
	pb := validMarkerInsertPb()
	pb.SeamAllowanceMm = &pb_decimal.Decimal{Value: "1"}
	pb.ContourAllowanceMm = &pb_decimal.Decimal{Value: "1"}
	out, err := ConvertPbTechCardMarkerInsertToEntity(pb)
	require.NoError(t, err, "the dto parses the FORM; the combination is judged where a field tag survives")
	ve := entity.MarkerAllowanceRefusal(out.SeamAllowanceMm, out.ContourAllowanceMm, out.ContourLayer.String)
	require.NotNil(t, ve)
	require.Equal(t, "seam_allowance_mm", ve.Field)
	require.Equal(t, entity.ReasonDoubleSeamAllowance, ve.Reason)
}

// On the way OUT, an unrecorded condition must be an ABSENT field and never a zero: «припуск 0» is a
// measurement and «не записано» is the absence of one, and a screen that cannot tell them apart shows
// every pre-Ф3 раскладка as a confidently measured zero.
func TestTechCardMarkerSummaryToPbEmitsConditionsWithPresence(t *testing.T) {
	base := entity.TechCardMarkerSummary{
		Name:         "M · основная",
		SizeId:       sql.NullInt64{Int64: 3, Valid: true},
		Sets:         sql.NullInt64{Int64: 4, Valid: true},
		UsedLengthCm: mustDecimal("512.4"),
	}

	t.Run("unrecorded conditions are absent", func(t *testing.T) {
		pb := TechCardMarkerSummaryToPb(base)
		require.Nil(t, pb.SeamAllowanceMm)
		require.Nil(t, pb.ContourAllowanceMm)
		require.Nil(t, pb.ContourLayer)
		require.Nil(t, pb.GrainLayer)
		require.Nil(t, pb.AllowFlip)
		require.False(t, pb.IsNorm)
		require.Equal(t, pb_common.TechCardMarkerPieceSetStatus_TECH_CARD_MARKER_PIECE_SET_STATUS_UNKNOWN,
			pb.PieceSetStatus, "no fingerprint recorded is UNKNOWN, never CHANGED")
		require.Empty(t, pb.NormConflict)
	})

	t.Run("recorded zeros and an empty grain layer survive the trip", func(t *testing.T) {
		m := base
		m.SeamAllowanceMm = mustNullDecimal("0")
		m.ContourAllowanceMm = mustNullDecimal("1")
		m.ContourLayer = sql.NullString{String: "14", Valid: true}
		m.GrainLayer = sql.NullString{String: "", Valid: true}
		m.AllowFlip = sql.NullBool{Bool: false, Valid: true}
		m.IsNorm = true
		pb := TechCardMarkerSummaryToPb(m)
		require.Equal(t, "0", pb.SeamAllowanceMm.Value)
		require.Equal(t, "1", pb.ContourAllowanceMm.Value)
		require.NotNil(t, pb.GrainLayer)
		require.Equal(t, "", *pb.GrainLayer, "an empty grain layer is «не разворачивать», not «не записано»")
		require.NotNil(t, pb.AllowFlip)
		require.False(t, *pb.AllowFlip)
		require.True(t, pb.IsNorm)
	})

	t.Run("the piece-set verdict is derived from the pair the store stamped", func(t *testing.T) {
		m := base
		m.PieceSetFp = sql.NullString{String: "aaa", Valid: true}
		m.CardPieceSetFp = sql.NullString{String: "aaa", Valid: true}
		require.Equal(t, pb_common.TechCardMarkerPieceSetStatus_TECH_CARD_MARKER_PIECE_SET_STATUS_MATCHES,
			TechCardMarkerSummaryToPb(m).PieceSetStatus)
		m.CardPieceSetFp = sql.NullString{String: "bbb", Valid: true}
		require.Equal(t, pb_common.TechCardMarkerPieceSetStatus_TECH_CARD_MARKER_PIECE_SET_STATUS_CHANGED,
			TechCardMarkerSummaryToPb(m).PieceSetStatus)
	})

	t.Run("a norm conflict is reported on the wire, not resolved in silence", func(t *testing.T) {
		m := base
		m.NormConflict = "на этой ткани отмечено 2 нормы сразу"
		require.Equal(t, m.NormConflict, TechCardMarkerSummaryToPb(m).NormConflict)
	})
}
