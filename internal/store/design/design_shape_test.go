package design

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// These probes need NO DATABASE. They are the half of the wave's guarantees that lives in the
// SHAPE of the code — the form of a statement, the discrimination of an error, the scope of an
// aggregate — and every one of them was written against a mutation that must turn it red. The
// half that needs rows (the actual lazy-birth race, the actual CAS, the actual guard) is in
// design_db_test.go, which is written and waits for a disposable container.

// TestBenchUpsertIsAnUpsertNotASelectThenInsert is the citation half of probe 1.
//
// MUTATION IT CATCHES: replacing the lazy birth with «SELECT, no row, INSERT». Two people putting
// a plate on `front` at the same moment would then both see no row, both insert, and the second
// would get a bare 1062 — an error that is in no taxonomy and that the client cannot undo,
// because what it waits for is Aborted: slot_rev_mismatch.
func TestBenchUpsertIsAnUpsertNotASelectThenInsert(t *testing.T) {
	up := strings.ToUpper(benchSlotUpsert)
	if !strings.Contains(up, "INSERT INTO DESIGN_BENCH_SLOT") {
		t.Fatal("the lazy birth of a bench slot must be an INSERT")
	}
	if !strings.Contains(up, "ON DUPLICATE KEY UPDATE") {
		t.Fatal("the lazy birth of a bench slot must be an UPSERT: without ON DUPLICATE KEY UPDATE " +
			"two simultaneous first placements race into 1062, which no client can undo")
	}
	if strings.Contains(up, "SELECT") {
		t.Fatal("the placement statement must not read first: a select-then-insert is exactly the race")
	}
}

// TestBenchUpsertAssignsSlotRevLast is the second half of probe 1, and it guards a defect the
// plan's own printed form carries.
//
// MUTATION IT CATCHES: moving `slot_rev` up among the ON DUPLICATE KEY UPDATE assignments. MySQL
// evaluates them left to right and every later expression sees what an earlier one just wrote, so
// a `slot_rev = :expected_rev` guard placed AFTER the increment is false — and set_by/set_at are
// then silently left at the previous author and the previous time on a CAS that SUCCEEDED. The
// picture still lands in the slot; only the byline lies, which is why no round trip would show it.
func TestBenchUpsertAssignsSlotRevLast(t *testing.T) {
	idx := strings.Index(benchSlotUpsert, "ON DUPLICATE KEY UPDATE")
	if idx < 0 {
		t.Fatal("no ON DUPLICATE KEY UPDATE clause")
	}
	clause := benchSlotUpsert[idx:]
	revAt := strings.Index(clause, "slot_rev    =")
	if revAt < 0 {
		revAt = strings.Index(clause, "slot_rev =")
	}
	if revAt < 0 {
		t.Fatal("slot_rev is not assigned in the duplicate branch")
	}
	for _, col := range []string{"picture_id", "detail_name", "set_by", "set_at"} {
		at := strings.Index(clause, col)
		if at < 0 {
			t.Fatalf("%s is not assigned in the duplicate branch", col)
		}
		if at > revAt {
			t.Fatalf("%s is assigned AFTER slot_rev: its CAS guard would compare against the "+
				"already-incremented revision and silently never fire", col)
		}
	}
}

// TestDupKeyTellsTheTwoUniqueKeysApart is probe 3's sibling and a correctness guard in its own
// right. design_bench_slot carries TWO unique keys that mean opposite things, and collapsing them
// would tell a person to reload when the true answer is «that plate is taken».
//
// MUTATION IT CATCHES: mapping every 1062 onto one refusal.
func TestDupKeyTellsTheTwoUniqueKeysApart(t *testing.T) {
	cases := map[string]string{
		"Duplicate entry '7-front' for key 'design_bench_slot.uq_design_bench_view'": "uq_design_bench_view",
		"Duplicate entry '7-42' for key 'design_bench_slot.uq_design_bench_picture'": "uq_design_bench_picture",
		"Duplicate entry 'abc' for key 'uq_design_batch_client_request'":             "uq_design_batch_client_request",
	}
	for msg, want := range cases {
		key, dup := mysqlDupKey(&mysql.MySQLError{Number: 1062, Message: msg})
		if !dup {
			t.Fatalf("1062 was not recognised: %q", msg)
		}
		if key != want {
			t.Fatalf("key of %q = %q, want %q", msg, key, want)
		}
	}
	if _, dup := mysqlDupKey(errors.New("some other failure")); dup {
		t.Fatal("a non-MySQL error must not be read as a duplicate key")
	}
	if _, dup := mysqlDupKey(&mysql.MySQLError{Number: 1452, Message: "fk"}); dup {
		t.Fatal("a foreign-key failure must not be read as a duplicate key")
	}
}

// TestHeaderAggregatesAreCardWideNotPageWide is probe 5's citation half.
//
// MUTATION IT CATCHES: scoping an aggregate to the loaded page — adding a LIMIT, a cursor, or a
// join to the page's ids. The header would then report «12 runs» for a card with forty, and the
// number would look entirely plausible, which is what makes the defect survive review.
func TestHeaderAggregatesAreCardWideNotPageWide(t *testing.T) {
	for name, q := range map[string]string{
		"total_runs":    designCountRuns,
		"archived_runs": designCountArchivedRuns,
		"max_rrev":      designMaxRrev,
		"total_batches": designCountBatches,
	} {
		if !strings.Contains(q, "tech_card_id = :card") {
			t.Fatalf("%s is not scoped to the card", name)
		}
		up := strings.ToUpper(q)
		for _, forbidden := range []string{"LIMIT", ":CURSOR", ":LIMIT", " IN (:IDS)"} {
			if strings.Contains(up, forbidden) {
				t.Fatalf("%s mentions %q: an aggregate computed over the page truncates the header "+
					"by exactly what is off screen", name, forbidden)
			}
		}
	}
}

// TestColourRecipesReadSnakeCaseJSONPaths pins the seam with wave 2.
//
// The snapshot columns hold protojson written with UseProtoNames: true. protojson's DEFAULT is
// lowerCamelCase, so a writer that forgets the option makes this query — and the HidePicture guard
// — return nothing at all, with no error anywhere: an empty result is a legal state for a card
// with no runs, so nothing goes red and the chips simply never appear.
func TestColourRecipesReadSnakeCaseJSONPaths(t *testing.T) {
	if !strings.Contains(entity.DesignRunJSONFieldColour, "$.colour") {
		t.Fatal("the colour path moved; the store's query must move with it")
	}
	for _, p := range []string{
		entity.DesignInputsJSONSlotMedia,
		entity.DesignInputsJSONRefMedia,
		entity.DesignParamsJSONExtraMedia,
	} {
		if strings.ContainsAny(p, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			t.Fatalf("JSON path %q is not snake_case: the writer stores protojson with "+
				"UseProtoNames and a camelCase path silently matches nothing", p)
		}
	}
}

// TestEveryRefusalOfTheTaxonomyExists keeps the store's vocabulary and the contract's aligned. A
// refusal the client has never heard of is a refusal it cannot undo.
func TestEveryRefusalOfTheTaxonomyExists(t *testing.T) {
	for _, err := range []error{
		entity.ErrDesignSlotRevMismatch, entity.ErrDesignForeignCardPlate,
		entity.ErrDesignCompositePlate, entity.ErrDesignHiddenPlate, entity.ErrDesignWrongKind,
		entity.ErrDesignPictureAlreadyInSlot, entity.ErrDesignDetailNameRequired,
		entity.ErrDesignSlotFilled, entity.ErrDesignSlotInVersion, entity.ErrDesignNotADetailSlot,
		entity.ErrDesignInSlot, entity.ErrDesignInVersion, entity.ErrDesignLiveRunInput,
		entity.ErrDesignLiveCropParent, entity.ErrDesignNotComposite,
		entity.ErrDesignLayerRevMismatch, entity.ErrDesignEmptyLayer,
		entity.ErrDesignStrokesTooLarge,
	} {
		if err == nil || err.Error() == "" {
			t.Fatal("a refusal of the taxonomy is missing")
		}
	}
}

// TestBudgetDayKeyIsComputedInTheOrgTimezone. The day that resets the money bar is an
// organisational decision, not a property of whichever database session answered.
func TestBudgetDayKeyIsComputedInTheOrgTimezone(t *testing.T) {
	// 2026-03-01T23:30Z is already 2026-03-02 in Warsaw (UTC+1).
	when := mustTime(t, "2026-03-01T23:30:00Z")
	if got := DesignBudgetDayKey(when, "Europe/Warsaw"); got != "2026-03-02" {
		t.Fatalf("Warsaw day = %q, want 2026-03-02", got)
	}
	if got := DesignBudgetDayKey(when, "UTC"); got != "2026-03-01" {
		t.Fatalf("UTC day = %q, want 2026-03-01", got)
	}
	// An unloadable zone must fall back to UTC rather than to the server's own local day, which
	// would move the reset by hours without telling anybody.
	if got := DesignBudgetDayKey(when, "Mars/Olympus"); got != "2026-03-01" {
		t.Fatalf("unloadable zone day = %q, want the UTC fallback 2026-03-01", got)
	}
}

// TestSilhouetteAndDetailAddressSpacesDoNotOverlap. A detail is never addressed by view; the four
// sides never by a minted id. Overlapping the two address spaces is how a rename moves a plate.
func TestSilhouetteAndDetailAddressSpacesDoNotOverlap(t *testing.T) {
	if entity.IsDesignSilhouetteView(entity.DesignViewDetail) {
		t.Fatal("`detail` must not be one of the four silhouette sides")
	}
	if !entity.IsDesignGhostView(entity.DesignViewDetail) {
		t.Fatal("`detail` is a legal ghost view and a legal reference role")
	}
	for _, v := range entity.DesignSilhouetteViews {
		if !entity.IsDesignGhostView(v) {
			t.Fatalf("%q must be a legal ghost view", v)
		}
	}
	if entity.IsDesignGhostView("sleeve") {
		t.Fatal("an unknown view must be refused, not accepted as an open string")
	}
}

// mustTime parses an RFC3339 instant or fails the probe.
func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// TestOrphanedMediaCatchesTheIdempotentShortCircuit is the running half of the compensation probe
// pair.
//
// The byte work of a split happens BEFORE the transaction, so every crop already has a public
// media row when the transaction runs. The verbatim upload helper cleans up its bucket object only
// while the media row does not yet exist; once AddMedia succeeds the row belongs to the caller,
// and that is exactly where this window opens.
//
// THE CASE THAT LOOKS FINE AND IS NOT: err == nil. An idempotent split returns the crops of an
// EARLIER cut, so this call's fresh uploads were adopted by nothing — and a compensation that only
// ran on error would leave them public and ownerless forever.
//
// MUTATION: make the handler sweep only when the store returned an error (i.e. treat err == nil as
// "everything was adopted"). THIS probe must go red on the third case.
func TestOrphanedMediaCatchesTheIdempotentShortCircuit(t *testing.T) {
	cases := []struct {
		name    string
		minted  []int
		adopted []int
		want    []int
	}{
		{"nothing minted", nil, []int{7}, nil},
		{"everything adopted", []int{7, 8}, []int{7, 8}, nil},
		{"idempotent short circuit: the store returned OLDER crops", []int{9, 10}, []int{7, 8}, []int{9, 10}},
		{"partial adoption", []int{9, 10}, []int{9}, []int{10}},
		{"a zero id is not a media row", []int{0, 9}, []int{}, []int{9}},
	}
	for _, c := range cases {
		got := OrphanedMedia(c.minted, c.adopted)
		if len(got) != len(c.want) {
			t.Fatalf("%s: orphans = %v, want %v", c.name, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: orphans = %v, want %v", c.name, got, c.want)
			}
		}
	}
}
