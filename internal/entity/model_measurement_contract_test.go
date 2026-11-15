package entity

import "testing"

func TestP0BodyMeasurementNamesAreAppendOnlyStorageKeys(t *testing.T) {
	if BodyHeight == BodyHPSHeight {
		t.Fatal("standing height and HPS height must remain distinct")
	}
	if len(P0BodyMeasurementNames) != 14 {
		t.Fatalf("P0 measurement count = %d, want 14", len(P0BodyMeasurementNames))
	}
	for name := range P0BodyMeasurementNames {
		if !ValidBodyMeasurementNames[name] {
			t.Errorf("P0 measurement %q is not in the accepted set", name)
		}
		if len(name) > 40 {
			t.Errorf("P0 measurement %q exceeds model_measurement.measurement_name VARCHAR(40)", name)
		}
	}
}

func TestBodyMeasurementMetadataSourcesExcludeUnknown(t *testing.T) {
	if len(ValidBodyMeasurementSources) != 4 {
		t.Fatalf("source count = %d, want 4", len(ValidBodyMeasurementSources))
	}
	if ValidBodyMeasurementSources[""] {
		t.Fatal("empty/UNKNOWN source must not be accepted for present metadata")
	}
}
