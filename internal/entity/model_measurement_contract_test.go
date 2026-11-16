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

func TestP1NeckIsSeparateAppendOnlyStorageKey(t *testing.T) {
	if BodyNeck == BodyNeckBase {
		t.Fatal("mid-neck and neck base must remain distinct keys")
	}
	if BodyNeck != "neck" {
		t.Fatalf("neck storage key = %q, want \"neck\" (stored rows must not be renamed)", BodyNeck)
	}
	if len(P1BodyMeasurementNames) != 1 || !P1BodyMeasurementNames[BodyNeck] {
		t.Fatalf("P1 set = %v, want exactly neck", P1BodyMeasurementNames)
	}
	for name := range P1BodyMeasurementNames {
		if !ValidBodyMeasurementNames[name] {
			t.Errorf("P1 measurement %q is not in the accepted set", name)
		}
		if P0BodyMeasurementNames[name] {
			t.Errorf("P1 measurement %q must not also be P0", name)
		}
		if len(name) > 40 {
			t.Errorf("P1 measurement %q exceeds model_measurement.measurement_name VARCHAR(40)", name)
		}
	}
	if len(ValidBodyMeasurementNames) != 35 {
		t.Fatalf("accepted measurement count = %d, want 35", len(ValidBodyMeasurementNames))
	}
	if BodyMeasurementSchemaVersionCurrent != BodyMeasurementSchemaVersionP1 || BodyMeasurementSchemaVersionP1 != 2 {
		t.Fatalf("current schema version = %d, want P1 = 2", BodyMeasurementSchemaVersionCurrent)
	}
}

func TestBodyMeasurementIntroducedInSchemaVersion(t *testing.T) {
	cases := map[BodyMeasurementName]uint32{
		BodyChest:     0,
		BodyNeckBase:  0,
		BodyHPSHeight: BodyMeasurementSchemaVersionP0,
		BodyNeck:      BodyMeasurementSchemaVersionP1,
		"not_a_key":   0,
	}
	for name, want := range cases {
		if got := BodyMeasurementIntroducedInSchemaVersion(name); got != want {
			t.Errorf("introduced version of %q = %d, want %d", name, got, want)
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
