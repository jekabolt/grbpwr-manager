package entity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPersistedModelPhysicalInputsSHA256V1MatchesECMAScript(t *testing.T) {
	// SHA256 of the exact JSON.stringify wire object. U+2028 and literal
	// backslash-u2028 must remain distinct, as must null provenance.
	withProvenance := ModelMeasurement{
		Name: BodyWaist, ValueMM: 740,
		Metadata: &BodyMeasurementMetadataV1{
			SchemaVersion: 1,
			Source:        BodyMeasurementManualTape,
			ProtocolID:    "Tape \"A\" <3 – Я 🌱\u2028literal\\u2028",
		},
	}
	withoutProvenance := ModelMeasurement{Name: BodyNeck, ValueMM: 381}
	const expected = "5b593e7a90d59751b52f507abc10c1c85568240b46e8333cd36f55ee6155a964"
	for _, measurements := range [][]ModelMeasurement{
		{withProvenance, withoutProvenance}, {withoutProvenance, withProvenance},
	} {
		actual, err := PersistedModelPhysicalInputsSHA256V1(42, Unisex, measurements)
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	}
	changed := *withProvenance.Metadata
	changed.Source = BodyMeasurementThreeDScan
	withProvenance.Metadata = &changed
	actual, err := PersistedModelPhysicalInputsSHA256V1(42, Unisex, []ModelMeasurement{withoutProvenance, withProvenance})
	require.NoError(t, err)
	require.NotEqual(t, expected, actual)
}

func TestPersistedModelPhysicalInputsSHA256V1RejectsInvalidData(t *testing.T) {
	valid := ModelMeasurement{Name: BodyChest, ValueMM: 920}
	for _, invalid := range [][]ModelMeasurement{
		{{Name: "future_measurement", ValueMM: 100}},
		{{Name: BodyChest, ValueMM: 0}},
		{{Name: BodyHeight, ValueMM: 5001}},
		{valid, valid},
		{{Name: BodyChest, ValueMM: 920, Metadata: &BodyMeasurementMetadataV1{SchemaVersion: 1, Source: "unknown", ProtocolID: "valid"}}},
		{{Name: BodyChest, ValueMM: 920, Metadata: &BodyMeasurementMetadataV1{SchemaVersion: 1, Source: BodyMeasurementImported, ProtocolID: "a\nb"}}},
	} {
		got, err := PersistedModelPhysicalInputsSHA256V1(1, Female, invalid)
		require.Error(t, err)
		require.Empty(t, got)
	}
	_, err := PersistedModelPhysicalInputsSHA256V1(1, "", []ModelMeasurement{valid})
	require.Error(t, err)
}

// The locally archived neutral diagnostic model is exposed to the Workbench by
// the production GetModel projection. Pin the same id, seven measurements and
// no provenance as the independently verified frontend fixture.
func TestPersistedModelPhysicalInputsSHA256V1DiagnosticModelParity(t *testing.T) {
	digest, err := PersistedModelPhysicalInputsSHA256V1(1, Unisex, []ModelMeasurement{
		{Name: BodyAcrossShoulder, ValueMM: 420},
		{Name: BodyChest, ValueMM: 920},
		{Name: BodyHeight, ValueMM: 1720},
		{Name: BodyHip, ValueMM: 980},
		{Name: BodyInseam, ValueMM: 800},
		{Name: BodySleeveLength, ValueMM: 700},
		{Name: BodyWaist, ValueMM: 740},
	})
	require.NoError(t, err)
	require.Equal(t, "d98cb41820dc77db335af8728ea246a28765f08d17f5a2f950753766638582aa", digest)
}
