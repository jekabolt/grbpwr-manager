package dto

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// TestUnitIntervalScaleBound guards the coordinate against exponent-encoded blow-ups.
//
// A wire coordinate is a STRING, so it legitimately carries an exponent, and the range check alone
// does not save us: comparing against the frame bounds aligns exponents and materializes every
// zero. Measured on this same shopspring/decimal before the bound existed:
//
//	"0.5e-500000"  11 bytes in → 500 005 bytes marshalled into the JSON column
//	"1E-10000000"  11 bytes in → 1.2 s of CPU and 44 MiB, before any storage
//	"1E+10000000"  11 bytes in → 3.3 s and 190 MiB, on the path that already ENDS in a refusal
//
// At the annotation ceilings (30 per image × 200 ink points = 12 000 coordinates) that is hours of
// CPU behind a ~200 KB request body, so no message-size limit reaches it.
//
// This test is deliberately in dto and hits no database: the bound lives in the shared validator
// that the techcard sketch, the assembly-step photo and the card attachments all go through.
func TestUnitIntervalScaleBound(t *testing.T) {
	accepted := []string{"0", "1", "0.5", "0.25", "0.123456", "1.000000", "0.000001"}
	for _, v := range accepted {
		t.Run("accepts "+v, func(t *testing.T) {
			got, err := unitInterval("p.x", &pb_decimal.Decimal{Value: v})
			require.NoError(t, err)
			// The value must survive unrounded: the client's precision is its own business as
			// long as it stays inside the bound.
			assert.True(t, got.Equal(mustDecimal(v)), "%s came back as %s", v, got)
		})
	}

	rejected := map[string]string{
		"0.5e-500000": "too_precise",
		"1E-10000000": "too_precise",
		"0.1234567":   "too_precise",
		"1E+100":      "bad_scale",
		"1E+10000000": "bad_scale",
	}
	for v, reason := range rejected {
		t.Run("rejects "+v, func(t *testing.T) {
			start := time.Now()
			_, err := unitInterval("p.x", &pb_decimal.Decimal{Value: v})
			elapsed := time.Since(start)

			require.Error(t, err)
			var ve *entity.ValidationError
			require.True(t, errors.As(err, &ve), "expected a field violation, got %T", err)
			assert.Equal(t, "p.x", ve.Field)
			assert.Equal(t, reason, ve.Reason)

			// The refusal must be CHEAP — a refusal that costs as much as the attack is not a
			// defence. Generous ceiling so the assertion never flakes on a loaded machine; the
			// failure mode it catches is seconds, not milliseconds.
			assert.Less(t, elapsed, 50*time.Millisecond,
				"refusing %q took %v: the bound must be checked before the range comparison", v, elapsed)

			// And the refusal text must not itself expand the value it refuses.
			assert.LessOrEqual(t, len(ve.Error()), 512, "the message inlined the exploded value")
		})
	}
}

// TestUnitIntervalRejectionIsNotStored is the end of the same argument: a coordinate that passes
// the bound cannot blow up the column either, because the stored form is its decimal string.
func TestUnitIntervalRejectionIsNotStored(t *testing.T) {
	got, err := unitInterval("p.x", &pb_decimal.Decimal{Value: "0.000001"})
	require.NoError(t, err)
	raw, err := json.Marshal(entity.TechCardAnnotationPoint{X: got, Y: got})
	require.NoError(t, err)
	assert.Less(t, len(raw), 64, "a bounded coordinate must stay small in the column: %d bytes", len(raw))
}
