package techcard

import (
	"reflect"
	"testing"
	"time"
)

func TestTechCardSnapshotBucketUsesServerUTCInFiveMinuteSteps(t *testing.T) {
	warsaw := time.FixedZone("CEST", 2*60*60)
	tests := []struct {
		name string
		in   time.Time
		want time.Time
	}{
		{
			name: "middle of bucket",
			in:   time.Date(2026, 10, 23, 14, 34, 59, 999, time.UTC),
			want: time.Date(2026, 10, 23, 14, 30, 0, 0, time.UTC),
		},
		{
			name: "exact boundary",
			in:   time.Date(2026, 10, 23, 14, 35, 0, 0, time.UTC),
			want: time.Date(2026, 10, 23, 14, 35, 0, 0, time.UTC),
		},
		{
			name: "non UTC input",
			in:   time.Date(2026, 10, 23, 16, 39, 1, 0, warsaw),
			want: time.Date(2026, 10, 23, 14, 35, 0, 0, time.UTC),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := techCardSnapshotBucket(tc.in); !got.Equal(tc.want) || got.Location() != time.UTC {
				t.Fatalf("techCardSnapshotBucket(%v) = %v (%v), want %v (UTC)",
					tc.in, got, got.Location(), tc.want)
			}
		})
	}
}

func TestMergeSnapshotChangesKeepsStableUniqueOrder(t *testing.T) {
	got := mergeSnapshotChanges(
		[]string{"note", "bom · 2 lines"},
		[]string{" note ", "sizes", "", "bom · 2 lines", "costing"},
	)
	want := []string{"note", "bom · 2 lines", "sizes", "costing"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeSnapshotChanges() = %#v, want %#v", got, want)
	}
}
