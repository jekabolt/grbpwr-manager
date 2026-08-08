package entity

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
)

// rrDec / rrNull are local to this file: `dec` and `nd` already exist in this package's test suite
// with a NullDecimal return, and shadowing either of them here would silently retype the arguments
// of every existing test that calls them.
func rrDec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func rrNull(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: rrDec(s), Valid: true}
}

// TestRunReadinessUnknownNeverBlocksAndNeverPasses is THE invariant of Ф6, and it is a table over
// every severity rather than a spot check because the two failure modes are opposite and both are
// silent: an UNKNOWN that blocks refuses runs for a phase we have not built, and an UNKNOWN that
// passes advertises a check that never ran.
func TestRunReadinessUnknownNeverBlocksAndNeverPasses(t *testing.T) {
	cases := []struct {
		sev        RunReadinessSeverity
		wantBlocks bool
		wantPassed bool
	}{
		{RunReadinessOK, false, true},
		{RunReadinessUnknown, false, false},
		{RunReadinessWarning, false, false},
		{RunReadinessBlocker, true, false},
		{RunReadinessUnspecified, false, false},
	}
	for _, c := range cases {
		f := RunReadinessFinding{Severity: c.sev}
		if got := f.Blocks(); got != c.wantBlocks {
			t.Errorf("%s.Blocks() = %v, want %v", c.sev, got, c.wantBlocks)
		}
		if got := f.Passed(); got != c.wantPassed {
			t.Errorf("%s.Passed() = %v, want %v", c.sev, got, c.wantPassed)
		}
	}
}

// TestRunReadinessReportUnknownDoesNotAffectReady puts an UNKNOWN in every one of the three lists at
// once and asserts the run stays ready — in BOTH modes, because «UNKNOWN does not block» has to be
// true of the create refusal and not only of the badge.
func TestRunReadinessReportUnknownDoesNotAffectReady(t *testing.T) {
	unknownEverywhere := RunReadinessReport{
		Card: []RunReadinessFinding{{Key: RunReadinessKeyReleaseFrozen, Severity: RunReadinessUnknown}},
		Colorways: []RunReadinessColorway{{ColorwayId: 1, Findings: []RunReadinessFinding{
			{Key: RunReadinessKeyNormFlipPolicy, Severity: RunReadinessUnknown},
			{Key: RunReadinessKeyNormSeamAllowance, Severity: RunReadinessUnknown},
			{Key: RunReadinessKeyNormPieceSet, Severity: RunReadinessWarning},
		}}},
		Run: []RunReadinessFinding{{Key: RunReadinessKeySizesInDxf, Severity: RunReadinessUnknown}},
	}
	for _, blocking := range []bool{false, true} {
		r := unknownEverywhere
		r.BlockingEnabled = blocking
		if !r.Ready() {
			t.Fatalf("blocking=%v: a report of UNKNOWNs and a WARNING must stay ready", blocking)
		}
		if r.WouldBlock() {
			t.Fatalf("blocking=%v: UNKNOWN must never make create refuse", blocking)
		}
		if len(r.Blockers()) != 0 {
			t.Fatalf("blocking=%v: UNKNOWN must not appear among the blockers", blocking)
		}
		if !r.Colorways[0].Ready() {
			t.Fatalf("blocking=%v: a colourway carrying only UNKNOWN/WARNING stays selectable", blocking)
		}
	}
	if got := unknownEverywhere.UnknownCount(); got != 4 {
		t.Fatalf("UnknownCount() = %d, want 4 — the count is what tells «nothing is broken» from «nothing was checked»", got)
	}
}

// TestRunReadinessWouldBlockNeedsBothHalves: report-only never refuses however red the verdict is,
// and blocking mode refuses only when a real BLOCKER exists.
func TestRunReadinessWouldBlockNeedsBothHalves(t *testing.T) {
	red := RunReadinessReport{Run: []RunReadinessFinding{{Key: RunReadinessKeySizesInRange, Severity: RunReadinessBlocker}}}
	if red.WouldBlock() {
		t.Fatal("report-only mode must never refuse, even with a blocker")
	}
	red.BlockingEnabled = true
	if !red.WouldBlock() {
		t.Fatal("blocking mode must refuse on a blocker")
	}
	green := RunReadinessReport{BlockingEnabled: true,
		Run: []RunReadinessFinding{{Key: RunReadinessKeyStockShortage, Severity: RunReadinessWarning}}}
	if green.WouldBlock() {
		t.Fatal("a WARNING is not a refusal")
	}
}

// TestRunReadinessKeyRegistryIsComplete guards the wire vocabulary: every key constant declared in
// this package must be in the group registry, and every registry entry must be a declared key. A key
// missing from the registry is a key the completeness test in dto cannot see, and a registry entry
// with no constant is a key nobody emits.
func TestRunReadinessKeyRegistryIsComplete(t *testing.T) {
	declared := []string{
		RunReadinessKeyCardAuxiliary, RunReadinessKeyReleaseFrozen, RunReadinessKeyCardSizeRange,
		RunReadinessKeyCardPieces, RunReadinessKeyCardPiecesDxfMatched, RunReadinessKeyPatternBindingResolved,
		RunReadinessKeyColorwayLive, RunReadinessKeySlotArticle, RunReadinessKeySlotNorm,
		RunReadinessKeyNormProvenance, RunReadinessKeyNormConditionsRecorded, RunReadinessKeyNormSeamAllowance,
		RunReadinessKeyNormFlipPolicy, RunReadinessKeyNormPieceSet, RunReadinessKeyNormWidthVsArticle,
		RunReadinessKeyNormMultiple,
		RunReadinessKeySizesInRange, RunReadinessKeySizesInDxf, RunReadinessKeyQuantitiesPresent,
		RunReadinessKeyStockShortage,
	}
	if len(declared) != len(RunReadinessKeyGroups) {
		t.Fatalf("registry has %d keys, %d are declared — one of the two was edited alone",
			len(RunReadinessKeyGroups), len(declared))
	}
	seen := map[string]bool{}
	for _, k := range declared {
		if seen[k] {
			t.Errorf("key %q is declared twice — a key is never re-used for a second fact", k)
		}
		seen[k] = true
		if _, ok := RunReadinessKeyGroups[k]; !ok {
			t.Errorf("key %q has no group in RunReadinessKeyGroups", k)
		}
	}
	for k, g := range RunReadinessKeyGroups {
		if !seen[k] {
			t.Errorf("registry key %q has no constant", k)
		}
		switch g {
		case RunReadinessGroupCard, RunReadinessGroupColorway, RunReadinessGroupRun:
		default:
			t.Errorf("key %q has an unknown group %q", k, g)
		}
	}
}

// TestRunReadinessKeysCarryNoIdentifiers: a key names a FACT. An id baked into a key would make the
// vocabulary unbounded and un-mappable by a client's single lookup table.
func TestRunReadinessKeysCarryNoIdentifiers(t *testing.T) {
	for k := range RunReadinessKeyGroups {
		for _, r := range k {
			if r >= '0' && r <= '9' {
				t.Errorf("key %q contains a digit — identifiers belong in the target, never in the key", k)
			}
			if r != '_' && (r < 'a' || r > 'z') {
				t.Errorf("key %q is not lower_snake_case", k)
			}
		}
	}
}

func TestRunReadinessBlockingDefaultsToReportOnly(t *testing.T) {
	if RunReadinessBlocking(nil) {
		t.Fatal("nil settings must read as report-only, not as blocking")
	}
	if RunReadinessBlocking(&WorkshopSettings{}) {
		t.Fatal("an UNCONFIGURED column must read as report-only — a setting whose default stops the factory is not a setting")
	}
	if RunReadinessBlocking(&WorkshopSettings{RunReadinessBlocking: sql.NullBool{Bool: false, Valid: true}}) {
		t.Fatal("a stored false is report-only")
	}
	if !RunReadinessBlocking(&WorkshopSettings{RunReadinessBlocking: sql.NullBool{Bool: true, Valid: true}}) {
		t.Fatal("a stored true is blocking")
	}
}

// TestNormWidthVsArticle nails the arithmetic the width rule has to get right, and the case that
// matters most is the LOT branch: the marker's stored width is a CUTTING width while a lot's
// measured width is the FULL roll, so failing to subtract the selvedge would inflate the right-hand
// side and pass markers that do not fit.
func TestNormWidthVsArticle(t *testing.T) {
	const marker = "148" // cutting width the раскладка ran on
	tests := []struct {
		name     string
		marker   string
		lot      decimal.NullDecimal
		selvedge string
		nominal  decimal.NullDecimal
		want     RunReadinessSeverity
		wantBase NormWidthBasis
		wantCut  string
	}{
		{
			// 150 measured − 2×1 кромка = 148 of cutting width; the marker needs exactly 148.
			name:   "lot: measured width less both selvedges exactly clears the marker",
			marker: marker, lot: rrNull("150"), selvedge: "1", nominal: rrNull("999"),
			want: RunReadinessOK, wantBase: NormWidthBasisLot, wantCut: "148",
		},
		{
			// THE REGRESSION THIS TEST EXISTS FOR: comparing 148 against the RAW 149 would pass. It
			// must not — 149 − 2 = 147 of cutting width, and the marker is 148 wide.
			name:   "lot: the selvedge must come off the measured roll",
			marker: marker, lot: rrNull("149"), selvedge: "1", nominal: rrNull("999"),
			want: RunReadinessBlocker, wantBase: NormWidthBasisLot, wantCut: "147",
		},
		{
			name:   "lot wins over nominal even when the nominal is generous",
			marker: marker, lot: rrNull("140"), selvedge: "0", nominal: rrNull("160"),
			want: RunReadinessBlocker, wantBase: NormWidthBasisLot, wantCut: "140",
		},
		{
			name:   "no measured lot falls back to the article's usable width",
			marker: marker, lot: decimal.NullDecimal{}, selvedge: "1", nominal: rrNull("148"),
			want: RunReadinessOK, wantBase: NormWidthBasisNominal, wantCut: "148",
		},
		{
			name:   "nominal narrower than the marker blocks",
			marker: marker, lot: decimal.NullDecimal{}, selvedge: "1", nominal: rrNull("146"),
			want: RunReadinessBlocker, wantBase: NormWidthBasisNominal, wantCut: "146",
		},
		{
			// An absent width is not a narrow one. UNKNOWN, and it does not block.
			name:   "neither a lot nor a catalogue width is UNKNOWN, not a refusal",
			marker: marker, lot: decimal.NullDecimal{}, selvedge: "0", nominal: decimal.NullDecimal{},
			want: RunReadinessUnknown, wantBase: NormWidthBasisNone,
		},
		{
			// Operator error (кромка wider than the roll) clamps to 0 rather than producing a negative
			// width, mirroring Material.UsableFabricWidthCm.
			name:   "a selvedge wider than the roll clamps at zero",
			marker: marker, lot: rrNull("10"), selvedge: "20", nominal: decimal.NullDecimal{},
			want: RunReadinessBlocker, wantBase: NormWidthBasisLot, wantCut: "0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormWidthVsArticle(rrDec(tt.marker), tt.lot, rrDec(tt.selvedge), tt.nominal)
			if got.Severity != tt.want {
				t.Errorf("severity = %s, want %s", got.Severity, tt.want)
			}
			if got.Basis != tt.wantBase {
				t.Errorf("basis = %d, want %d", got.Basis, tt.wantBase)
			}
			if tt.wantCut != "" && (!got.TodayCuttingCm.Valid || !got.TodayCuttingCm.Decimal.Equal(rrDec(tt.wantCut))) {
				t.Errorf("today cutting width = %v, want %s", got.TodayCuttingCm, tt.wantCut)
			}
		})
	}
}

func TestNormFlipPolicy(t *testing.T) {
	yes := sql.NullBool{Bool: true, Valid: true}
	no := sql.NullBool{Bool: false, Valid: true}
	unset := sql.NullBool{}
	tests := []struct {
		name  string
		dir   TechCardFabricDirection
		known bool
		flip  sql.NullBool
		want  RunReadinessSeverity
	}{
		{"direction unknown is UNKNOWN whatever the policy", "", false, yes, RunReadinessUnknown},
		{"direction unknown is UNKNOWN even with flip forbidden", "", false, no, RunReadinessUnknown},
		{"two_way permits the flip", FabricDirectionTwoWay, true, yes, RunReadinessOK},
		{"any permits the flip", FabricDirectionAny, true, yes, RunReadinessOK},
		{"one_way with the flip forbidden at capture time is fine", FabricDirectionOneWay, true, no, RunReadinessOK},
		{"one_way with the flip ALLOWED at capture time blocks", FabricDirectionOneWay, true, yes, RunReadinessBlocker},
		{"one_way with an unrecorded policy is UNKNOWN, not a refusal", FabricDirectionOneWay, true, unset, RunReadinessUnknown},
		// The asymmetry is the point: an unrecorded policy only matters on one_way cloth, so a
		// legacy marker on two_way cloth is not dragged into «no verdict» for nothing.
		{"an unrecorded policy on two_way cloth is still OK", FabricDirectionTwoWay, true, unset, RunReadinessOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormFlipPolicy(tt.dir, tt.known, tt.flip); got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

// TestNormSeamAllowance covers the three-valued allowance, and the unconfirmed-and-below case is the
// one that decides whether this rule is honest: the recorded number is a LOWER BOUND there, so a
// refusal would be a verdict the evidence does not support.
func TestNormSeamAllowance(t *testing.T) {
	confirmed := func(mm string) MarkerAllowance {
		return MarkerAllowance{Mm: rrDec(mm), Recorded: true, Confirmed: true}
	}
	lowerBound := func(mm string) MarkerAllowance {
		return MarkerAllowance{Mm: rrDec(mm), Recorded: true}
	}
	tests := []struct {
		name     string
		a        MarkerAllowance
		standard decimal.NullDecimal
		want     RunReadinessSeverity
	}{
		{"nothing recorded is UNKNOWN", MarkerAllowance{}, rrNull("1"), RunReadinessUnknown},
		{"no standard configured is UNKNOWN, never a substituted zero", confirmed("1"), decimal.NullDecimal{}, RunReadinessUnknown},
		{"confirmed and equal to the standard passes", confirmed("1"), rrNull("1"), RunReadinessOK},
		{"confirmed and above passes", confirmed("1.5"), rrNull("1"), RunReadinessOK},
		{"confirmed and below blocks", confirmed("0.5"), rrNull("1"), RunReadinessBlocker},
		{"a lower bound ALREADY clearing the standard passes", lowerBound("1.5"), rrNull("1"), RunReadinessOK},
		{"a lower bound below the standard is UNKNOWN, not a refusal", lowerBound("0.5"), rrNull("1"), RunReadinessUnknown},
		{"a zero standard is a real standard and is met by zero", confirmed("0"), rrNull("0"), RunReadinessOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormSeamAllowance(tt.a, tt.standard); got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestNormPieceSet(t *testing.T) {
	if got := NormPieceSet(MarkerPieceSetMatches); got != RunReadinessOK {
		t.Errorf("matches → %s", got)
	}
	if got := NormPieceSet(MarkerPieceSetChanged); got != RunReadinessBlocker {
		t.Errorf("changed → %s", got)
	}
	// The one that matters: an unrecorded fingerprint must not be rendered as «changed», or the badge
	// lands on every stored marker at once.
	if got := NormPieceSet(MarkerPieceSetUnknown); got != RunReadinessUnknown {
		t.Errorf("unknown → %s, want unknown", got)
	}
}

// TestSizesInDxf: only a USABLE index produces a verdict, and an empty token set is not one.
func TestSizesInDxf(t *testing.T) {
	tokens := map[string]bool{"m": true, "l": true}
	if got := SizesInDxf(PatternSizeIndexUsable, tokens, "m_46ta_m"); got != RunReadinessOK {
		t.Errorf("a size present in the tokens → %s", got)
	}
	if got := SizesInDxf(PatternSizeIndexUsable, tokens, "xs_44ta_m"); got != RunReadinessBlocker {
		t.Errorf("a size absent from a USABLE index → %s, want blocker", got)
	}
	for _, state := range []PatternSizeIndexState{
		PatternSizeIndexMissing, PatternSizeIndexStale, PatternSizeIndexUngraded,
	} {
		if got := SizesInDxf(state, nil, "xs_44ta_m"); got != RunReadinessUnknown {
			t.Errorf("state %d → %s, want unknown: an absent or stale index is a missing INSTRUMENT, not an answer", state, got)
		}
	}
}

// TestWorkshopSettingsPatchIsEmptyCoversEveryField holds the trap 0272 named: IsEmpty lists its
// fields by hand, and a tenant missing from the list makes a request that names ONLY that tenant look
// empty and be rejected. Reflection sets one field at a time and demands IsEmpty go false.
func TestWorkshopSettingsPatchIsEmptyCoversEveryField(t *testing.T) {
	if !(WorkshopSettingsPatch{}).IsEmpty() {
		t.Fatal("a patch naming nothing must be empty")
	}
	typ := reflect.TypeOf(WorkshopSettingsPatch{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() != reflect.Ptr {
			t.Fatalf("field %s is not a pointer — the tri-state patch contract needs presence", f.Name)
		}
		v := reflect.New(typ).Elem()
		v.Field(i).Set(reflect.New(f.Type.Elem()))
		if v.Interface().(WorkshopSettingsPatch).IsEmpty() {
			t.Errorf("IsEmpty() does not mention %s: a request naming only that setting is rejected as empty", f.Name)
		}
	}
}
