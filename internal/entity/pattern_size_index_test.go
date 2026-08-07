package entity

import (
	"reflect"
	"testing"
)

// TestPatternSheetFingerprintIsOrderIndependentAndChangeSensitive: the fingerprint has exactly two
// jobs and they pull in opposite directions — it must NOT change when nothing about the files
// changed (or every card would read as stale after any re-query), and it MUST change when any sheet
// is replaced (or a re-upload would leave a confident index describing files nobody parsed).
func TestPatternSheetFingerprintIsOrderIndependentAndChangeSensitive(t *testing.T) {
	a := []PatternSheetRef{
		{LineKey: "AAA", URL: "s3://x/1.dxf", Version: 1},
		{LineKey: "BBB", URL: "s3://x/2.dxf", Version: 1},
	}
	reordered := []PatternSheetRef{a[1], a[0]}
	if PatternSheetFingerprint(a) != PatternSheetFingerprint(reordered) {
		t.Fatal("row order must not move the fingerprint — the query's ORDER BY is not a fact about the card")
	}
	// Case folding on the key, as PieceSetFingerprint does, so a collation difference between prod
	// (utf8mb3) and a test container (utf8mb4) cannot stale every index at once.
	lower := []PatternSheetRef{{LineKey: "aaa", URL: "s3://x/1.dxf", Version: 1}, a[1]}
	if PatternSheetFingerprint(a) != PatternSheetFingerprint(lower) {
		t.Fatal("line-key case must not move the fingerprint")
	}
	replaced := []PatternSheetRef{{LineKey: "AAA", URL: "s3://x/1-v2.dxf", Version: 2}, a[1]}
	if PatternSheetFingerprint(a) == PatternSheetFingerprint(replaced) {
		t.Fatal("replacing a sheet MUST move the fingerprint — this is the whole staleness mechanism")
	}
	added := append(append([]PatternSheetRef{}, a...), PatternSheetRef{LineKey: "CCC", URL: "s3://x/3.dxf", Version: 1})
	if PatternSheetFingerprint(a) == PatternSheetFingerprint(added) {
		t.Fatal("adding a sheet must move the fingerprint")
	}
	// An empty scope hashes stably rather than to "": «this scope has no sheets» is a real state and
	// must stay distinguishable from «we could not compute one».
	if PatternSheetFingerprint(nil) == "" {
		t.Fatal("an empty sheet set must still produce a fingerprint")
	}
	if PatternSheetFingerprint(nil) != PatternSheetFingerprint([]PatternSheetRef{}) {
		t.Fatal("nil and empty are the same set")
	}
}

// TestPatternSizeIndexStatus walks the four states, and the two that are easiest to get wrong are
// the corrupt column and the empty token set — one must read as NO INDEX and the other as A LEGAL
// ANSWER, and collapsing either into the other produces a confident wrong verdict.
func TestPatternSizeIndexStatus(t *testing.T) {
	const fp = "fingerprint-of-today"
	tests := []struct {
		name  string
		row   *PatternSizeIndexRow
		want  PatternSizeIndexState
		want2 []string
	}{
		{"no row at all is MISSING", nil, PatternSizeIndexMissing, nil},
		{
			"a column that is not a JSON array of strings reads as MISSING, never as an empty set",
			&PatternSizeIndexRow{SheetFingerprint: fp, SizeTokensJSON: `{"broken":true}`},
			PatternSizeIndexMissing, nil,
		},
		{
			"an empty column reads as MISSING",
			&PatternSizeIndexRow{SheetFingerprint: fp, SizeTokensJSON: ``},
			PatternSizeIndexMissing, nil,
		},
		{
			"a fingerprint that no longer matches is STALE — the files changed after the parse",
			&PatternSizeIndexRow{SheetFingerprint: "yesterday", SizeTokensJSON: `["m","l"]`},
			PatternSizeIndexStale, nil,
		},
		{
			// The legal empty answer: one size per file yields no tokens, and reading that as «no sizes
			// are present» would be a false blocker on every size at once.
			"an empty token array is UNGRADED, a legal answer meaning the files carry no size coding",
			&PatternSizeIndexRow{SheetFingerprint: fp, SizeTokensJSON: `[]`},
			PatternSizeIndexUngraded, nil,
		},
		{
			"tokens under a current fingerprint are USABLE",
			&PatternSizeIndexRow{SheetFingerprint: fp, SizeTokensJSON: `["m","l"]`},
			PatternSizeIndexUsable, []string{"l", "m"},
		},
		{
			// Normalisation on READ as well as on write: a row written by an older binary, or by hand,
			// must not make a size look uncovered because of a stray bracket.
			"stored tokens are normalised on read",
			&PatternSizeIndexRow{SheetFingerprint: fp, SizeTokensJSON: `["<M>"," L "]`},
			PatternSizeIndexUsable, []string{"l", "m"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, tokens := PatternSizeIndexStatus(tt.row, fp)
			if state != tt.want {
				t.Fatalf("state = %d, want %d", state, tt.want)
			}
			if tt.want2 == nil {
				if len(tokens) != 0 {
					t.Fatalf("a non-usable state must carry no tokens, got %v", tokens)
				}
				return
			}
			got := make([]string, 0, len(tokens))
			for k := range tokens {
				got = append(got, k)
			}
			sortStrings(got)
			if !reflect.DeepEqual(got, tt.want2) {
				t.Fatalf("tokens = %v, want %v", got, tt.want2)
			}
		})
	}
}

func TestNormalizeSizeTokensDeduplicatesAndSorts(t *testing.T) {
	got := NormalizeSizeTokens([]string{"L", "<l>", "m", "", "  ", "44"})
	// Sorted so two runs of the same audit produce byte-identical columns — an order that depended on
	// map iteration would make one parse look like two different answers on inspection.
	want := []string{"44", "l", "m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := NormalizeSizeTokens(nil); len(got) != 0 {
		t.Fatalf("an empty parse stores an empty array, got %v", got)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
