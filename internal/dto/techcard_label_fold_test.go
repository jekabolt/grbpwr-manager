package dto

import "testing"

func TestParseGarmentLabelFoldAcceptsTee(t *testing.T) {
	for _, fold := range []string{"flat", "half", "ends", "mitre", "loop", "tee"} {
		got, err := parseGarmentLabelFold("fold", fold)
		if err != nil || !got.Valid || got.String != fold {
			t.Fatalf("fold %q: got %+v, %v", fold, got, err)
		}
	}
	if _, err := parseGarmentLabelFold("fold", "zigzag"); err == nil {
		t.Fatal("unknown fold must be refused")
	}
}
