package migrationlint

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/materialattr"
)

// This file extends migrationlint's static, database-free guards (see doc.go) to catch enum-value
// drift between an entity Go const set and the DB CHECK constraint that is supposed to enforce the
// same values (problem 033/50-F: "enum single-source выполнен частично" — enum_drift_test.go in
// internal/dto only ever compared entity<->proto, never entity<->DB). Each test below greps the exact
// migration file that owns the constraint and extracts its literal value list, so a future edit to
// either side that forgets the other fails here instead of silently compiling.
//
// TechCardPurpose has no proto enum yet (techcard.proto still carries `string purpose`) — T-B owns
// that conversion; only entity<->DB is checked here. See the track Dump for a TODO marker to add the
// third leg once the proto enum lands.

// readMigrationFile reads one migration by file name from ../sql (see migrationsDir in
// idempotency_test.go, shared across this package's tests).
func readMigrationFile(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(migrationsDir, name))
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	return string(body)
}

var (
	// regexpAlternationRe matches a MySQL CHECK's REGEXP alternation, e.g.
	// REGEXP '^(sellable|auxiliary)$' or (doubled quotes, when embedded in a dynamic PREPARE string)
	// REGEXP ''^(male|female|unisex)$''.
	regexpAlternationRe = regexp.MustCompile(`REGEXP\s+'+\^\(([a-zA-Z_|]+)\)\$'+`)
	// valueListRe matches a MySQL IN(...)/ENUM(...) quoted value list, doubled-quote tolerant.
	valueListRe = regexp.MustCompile(`(?:IN|ENUM)\s*\(([^)]*)\)`)
)

// extractDBEnumValues finds anchor in content, then extracts the REGEXP alternation or IN/ENUM value
// list appearing within the next window characters — bounding the search keeps it from matching an
// unrelated CHECK elsewhere in the same migration file.
func extractDBEnumValues(t *testing.T, content, anchor string, window int) []string {
	t.Helper()
	idx := strings.Index(content, anchor)
	if idx < 0 {
		t.Fatalf("anchor %q not found in migration content", anchor)
	}
	end := idx + window
	if end > len(content) {
		end = len(content)
	}
	scope := content[idx:end]

	if m := regexpAlternationRe.FindStringSubmatch(scope); m != nil {
		return strings.Split(m[1], "|")
	}
	if m := valueListRe.FindStringSubmatch(scope); m != nil {
		parts := strings.Split(m[1], ",")
		values := make([]string, 0, len(parts))
		for _, p := range parts {
			values = append(values, strings.Trim(strings.TrimSpace(p), "'"))
		}
		return values
	}
	t.Fatalf("no REGEXP alternation or IN/ENUM value list found within %d chars of anchor %q", window, anchor)
	return nil
}

// mapKeysAsStrings converts a set map (as every entity Valid* map here is shaped) to a plain string
// slice for comparison against DB-extracted values.
func mapKeysAsStrings[K ~string](m map[K]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, string(k))
	}
	return out
}

// assertSameSet fails with the specific missing/extra values on either side — deliberately not just
// a length check, so a same-count-but-different-value drift (e.g. a typo'd rename) is caught too.
func assertSameSet(t *testing.T, label string, dbValues, entityValues []string) {
	t.Helper()
	db := make(map[string]bool, len(dbValues))
	for _, v := range dbValues {
		if db[v] {
			t.Errorf("%s: DB value list has a duplicate: %q (%v)", label, v, dbValues)
		}
		db[v] = true
	}
	ent := make(map[string]bool, len(entityValues))
	for _, v := range entityValues {
		ent[v] = true
	}
	for v := range db {
		if !ent[v] {
			t.Errorf("%s: DB CHECK allows %q but the entity set does not", label, v)
		}
	}
	for v := range ent {
		if !db[v] {
			t.Errorf("%s: entity set allows %q but the DB CHECK does not", label, v)
		}
	}
}

// TestTechCardPurposeDBCheckNoDrift is the drift test the brief asks for by name: entity
// (TechCardPurpose/ValidTechCardPurposes) <-> DB CHECK (migration 0111, chk_tech_card_purpose).
func TestTechCardPurposeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0111_new_flow_auxiliary_tech_card.sql")
	dbValues := extractDBEnumValues(t, content, "purpose REGEXP", 100)
	assertSameSet(t, "TechCardPurpose", dbValues, mapKeysAsStrings(entity.ValidTechCardPurposes))
}

// TestTechCardAuxSubtypeDBCheckNoDrift is the WS7 drift guard: entity (TechCardAuxSubtype/
// ValidTechCardAuxSubtypes) <-> DB CHECK (chk_tech_card_aux_subtype). The value set must stay
// identical on both sides. It reads the migration that LAST redefined the constraint — 0173 created
// it, 0227 widened it with garment_case, 0255 with tote_bag — so a further widening must point this
// at its own file. Migration 0173's backfill CASE stays pinned to entity.AuxSubtypeFromName instead
// (asserted in the entity unit test): that heuristic is frozen history, not the live value set.
func TestTechCardAuxSubtypeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0255_tech_card_aux_subtype_tote_bag.sql")
	dbValues := extractDBEnumValues(t, content, "aux_subtype REGEXP", 200)
	assertSameSet(t, "TechCardAuxSubtype", dbValues, mapKeysAsStrings(entity.ValidTechCardAuxSubtypes))
}

// TestGenderDBCheckNoDrift extends the drift test to gender (entity.ValidProductTargetGenders), whose
// DB source of truth is migration 0067's tech_card.target_gender CHECK. product.target_gender was
// dropped by migration 0140 (PR6 style de-dup) so tech_card is the only remaining gender CHECK.
func TestGenderDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0067_add_tech_card_core.sql")
	dbValues := extractDBEnumValues(t, content, "target_gender REGEXP", 100)
	assertSameSet(t, "GenderEnum", dbValues, mapKeysAsStrings(entity.ValidProductTargetGenders))
}

// TestSeasonDBCheckNoDrift extends the drift test to season (entity.ValidSeasons), whose DB source of
// truth is migration 0134's tech_card_season_code_enum CHECK.
func TestSeasonDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0134_tech_card_season_normalize.sql")
	dbValues := extractDBEnumValues(t, content, "tech_card_season_code_enum", 300)
	assertSameSet(t, "SeasonEnum", dbValues, mapKeysAsStrings(entity.ValidSeasons))
}

// TestSizeSKUSystemDBCheckNoDrift extends the drift test to the size SKU system (entity.SizeSKUSystem/
// ValidSizeSKUSystems), whose DB source of truth is migration 0147's chk_size_sku_contract CHECK.
func TestSizeSKUSystemDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0147_size_sku_contract.sql")
	dbValues := extractDBEnumValues(t, content, "chk_size_sku_contract", 300)
	assertSameSet(t, "SizeSKUSystem", dbValues, mapKeysAsStrings(entity.ValidSizeSKUSystems))
}

// TestColorwayStatusDBCheckNoDrift extends the drift test to product lifecycle status
// (entity.ColorwayStatus/ValidColorwayStatuses). The DB source of truth is migration 0137's stored
// lifecycle_status with the named `chk_product_lifecycle_status CHECK (... BETWEEN <lo> AND <hi>)`.
// The entity side must be exactly the contiguous numeric range the CHECK stores — UNKNOWN=0 is a
// read-only fail-closed sentinel and must NOT be storable.
func TestColorwayStatusDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0137_product_status.sql")
	m := regexp.MustCompile(
		`chk_product_lifecycle_status CHECK \(lifecycle_status BETWEEN (\d+) AND (\d+)\)`,
	).FindStringSubmatch(content)
	if m == nil {
		t.Fatal("0137: named CHECK chk_product_lifecycle_status with BETWEEN bounds not found")
	}
	lo, _ := strconv.Atoi(m[1])
	hi, _ := strconv.Atoi(m[2])

	var got []int
	for s := range entity.ValidColorwayStatuses {
		got = append(got, int(s))
	}
	sort.Ints(got)
	var want []int
	for v := lo; v <= hi; v++ {
		want = append(want, v)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ColorwayStatus drift: entity storable set %v != DB CHECK range %v", got, want)
	}
	if entity.ValidColorwayStatuses[entity.ColorwayStatusUnknown] {
		t.Fatal("ColorwayStatusUnknown must not be storable")
	}
}

// TestMaterialClassDBCheckNoDrift extends the drift test to the material CTI discriminant
// (entity.MaterialClass/ValidMaterialClasses) <-> DB CHECK (migration 0157, chk_material_class).
func TestMaterialClassDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0157_material_cti.sql")
	dbValues := extractDBEnumValues(t, content, "material_class REGEXP", 120)
	assertSameSet(t, "MaterialClass", dbValues, mapKeysAsStrings(entity.ValidMaterialClasses))
}

// TestMaterialPurposeDBCheckNoDrift extends the drift test to the material purpose mark (#40)
// (entity.MaterialPurpose/ValidMaterialPurposes) <-> DB CHECK (migration 0184, chk_material_purpose).
func TestMaterialPurposeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0184_material_image_purpose.sql")
	dbValues := extractDBEnumValues(t, content, "purpose REGEXP", 120)
	assertSameSet(t, "MaterialPurpose", dbValues, mapKeysAsStrings(entity.ValidMaterialPurposes))
}

// TestMaterialPriceSourceDBCheckNoDrift extends the drift test to material_price.source
// (entity.ValidMaterialPriceSources) <-> DB CHECK (migration 0158, chk_material_price_source).
func TestMaterialPriceSourceDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0158_material_price_source_check.sql")
	dbValues := extractDBEnumValues(t, content, "source REGEXP", 120)
	assertSameSet(t, "MaterialPriceSource", dbValues, mapKeysAsStrings(entity.ValidMaterialPriceSources))
}

// TestCategorySizeSystemDBCheckNoDrift extends the drift test to category_size_system.size_system
// (S10/WS5): it must accept exactly entity.ValidSizeSKUSystems, the SAME set migration 0147's
// chk_size_sku_contract already enforces on size.sku_system (TestSizeSKUSystemDBCheckNoDrift above) --
// the two CHECKs are independent constraints on different tables and must not drift from each other
// or from the entity enum.
func TestCategorySizeSystemDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0175_category_size_system.sql")
	dbValues := extractDBEnumValues(t, content, "chk_category_size_system_system", 200)
	assertSameSet(t, "CategorySizeSystem.size_system", dbValues, mapKeysAsStrings(entity.ValidSizeSKUSystems))
}

// TestAcctEntrySourceTypeDBCheckNoDrift extends the drift test to the accounting journal entry's
// source (entity.AcctSourceType/ValidAcctSourceTypes) <-> DB CHECK. The CHECK was defined in 0189,
// extended through 0195/0196/0197/0201 (wave 2 delivered types, wave 3 pulls, depreciation/corp_tax,
// order_dispute) and last redefined by 0248 (Phase 6: +production_receive_reversal) — the test reads
// the LATEST migration that redefines the full value set (which sorts last), 07 §7.2 pattern.
func TestAcctEntrySourceTypeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0248_receipt_reversal.sql")
	dbValues := extractDBEnumValues(t, content, "source_type IN", 900)
	assertSameSet(t, "AcctSourceType", dbValues, mapKeysAsStrings(entity.ValidAcctSourceTypes))
}

// TestAcctSectionDBCheckNoDrift extends the drift test to the account section (entity.AcctSection/
// ValidAcctSections) <-> DB CHECK. Defined in 0189 and last extended by 0196 (phase 2, wave 3: +tax) —
// read the latest migration that redefines the full set (07 §7.2 extend-CHECK pattern).
func TestAcctSectionDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0196_accounting_wave3_pnl.sql")
	dbValues := extractDBEnumValues(t, content, "section IN", 300)
	assertSameSet(t, "AcctSection", dbValues, mapKeysAsStrings(entity.ValidAcctSections))
}

// TestAcctLineSideDBCheckNoDrift extends the drift test to the accounting journal line's side
// (entity.AcctSide/ValidAcctSides) <-> DB CHECK (migration 0189, chk_acct_line_side).
func TestAcctLineSideDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0189_accounting_core.sql")
	dbValues := extractDBEnumValues(t, content, "side IN", 100)
	assertSameSet(t, "AcctSide", dbValues, mapKeysAsStrings(entity.ValidAcctSides))
}

// TestAcctEventTypeDBCheckNoDrift extends the drift test to the accounting outbox event type
// (entity.AcctEventType/ValidAcctEventTypes) <-> DB CHECK. Defined in 0189, last extended by 0198
// (phase 2, wave 4: +order_dispute; 0195 added order_shipped/order_delivered) — read the latest
// migration that redefines the set.
func TestAcctEventTypeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0201_accounting_wave4_money.sql")
	dbValues := extractDBEnumValues(t, content, "event_type IN", 300)
	assertSameSet(t, "AcctEventType", dbValues, mapKeysAsStrings(entity.ValidAcctEventTypes))
}

// TestVatRegimeDBCheckNoDrift extends the drift test to the order VAT regime (entity.VatRegime/
// ValidVatRegimes) <-> DB CHECK (migration 0191, chk_customer_order_vat_regime). Phase 2, wave 1.
func TestVatRegimeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0191_accounting_vat_regime.sql")
	dbValues := extractDBEnumValues(t, content, "vat_regime IN", 200)
	assertSameSet(t, "VatRegime", dbValues, mapKeysAsStrings(entity.ValidVatRegimes))
}

// TestInputVatRegimeDBCheckNoDrift extends the drift test to the material input-VAT regime
// (entity.InputVatRegime/ValidInputVatRegimes) <-> DB CHECK (migration 0192,
// chk_material_input_vat_regime). Phase 2, wave 1.
func TestInputVatRegimeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0192_material_input_vat.sql")
	dbValues := extractDBEnumValues(t, content, "input_vat_regime IN", 200)
	assertSameSet(t, "InputVatRegime", dbValues, mapKeysAsStrings(entity.ValidInputVatRegimes))
}

// TestFabricDirectionFixtureVsDBCheck asserts the material-attributes fixture's fabric_direction set
// matches the DB CHECK (migration 0157, material_fabric_attr) — the fixture<->DB leg of the CTI drift
// guard (entity<->DB is TestMaterialClassDBCheckNoDrift; entity<->proto lives in internal/dto).
func TestFabricDirectionFixtureVsDBCheck(t *testing.T) {
	content := readMigrationFile(t, "0157_material_cti.sql")
	dbValues := extractDBEnumValues(t, content, "fabric_direction REGEXP", 120)
	assertSameSet(t, "fabric_direction", dbValues, materialattr.AllowedEnumValues("fabric", "fabric_direction"))
}

// TestEnumDriftExtractorsDetectTamperedInput guards the extractor helpers themselves (mirrors
// TestMigrationIdempotencyDetectors' rationale in idempotency_test.go) so this suite cannot silently
// pass because a regex stopped matching.
func TestEnumDriftExtractorsDetectTamperedInput(t *testing.T) {
	got := extractDBEnumValues(t, "CHECK (purpose REGEXP '^(sellable|auxiliary)$')", "purpose REGEXP", 100)
	assertSameSet(t, "sanity: REGEXP alternation", got, []string{"sellable", "auxiliary"})

	got = extractDBEnumValues(t, "CHECK (x IN (''SS'',''FW''))", "IN", 50)
	assertSameSet(t, "sanity: doubled-quote IN list", got, []string{"SS", "FW"})

	got = extractDBEnumValues(t, "ENUM(''active'',''hidden'',''archived'')", "ENUM(", 80)
	assertSameSet(t, "sanity: ENUM list", got, []string{"active", "hidden", "archived"})
}

// TestBomPurposeDBCheckNoDrift extends the drift test to НАЗНАЧЕНИЕ on a BOM line (0265):
// entity.TechCardBomPurpose/ValidTechCardBomPurposes <-> DB CHECK chk_bom_item_purpose.
//
// This one guards the field's whole reason for existing. The purpose list is closed BECAUSE the
// field is a grouping key — the moment the DB accepts a value the entity does not know (or the other
// way round) a line lands in a bucket no screen renders, and the grouping stops being trustworthy
// without failing anywhere visible.
func TestBomPurposeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0265_bom_item_purpose.sql")
	dbValues := extractDBEnumValues(t, content, "purpose REGEXP", 120)
	assertSameSet(t, "TechCardBomPurpose", dbValues, mapKeysAsStrings(entity.ValidTechCardBomPurposes))
}

// TestBomPurposeOrderCoversVocabulary keeps the PRESENTATION order in lockstep with the vocabulary.
// A purpose missing from BomPurposeOrder does not fail anywhere: it silently drops out of every
// ordered listing built from it — including the pattern viewer's group order, where the effect is a
// группа that exists in the data and never appears on screen. Duplicates are just as quiet: the same
// группа twice.
func TestBomPurposeOrderCoversVocabulary(t *testing.T) {
	ordered := make([]string, 0, len(entity.BomPurposeOrder))
	seen := make(map[entity.TechCardBomPurpose]bool, len(entity.BomPurposeOrder))
	for _, p := range entity.BomPurposeOrder {
		if seen[p] {
			t.Fatalf("BomPurposeOrder lists %q twice", p)
		}
		seen[p] = true
		ordered = append(ordered, string(p))
	}
	assertSameSet(t, "BomPurposeOrder", ordered, mapKeysAsStrings(entity.ValidTechCardBomPurposes))
}

// TestFabricPurposeDBCheckNoDrift extends the drift guard to the 0267 bindings: the выкройка's and
// the alias's fabric_purpose speak the SAME closed vocabulary as the BOM line's purpose (0265), and
// they must, because that is what makes them join.
//
// A value one side accepts and the other does not would not fail anywhere visible: the sheet would
// simply group under a heading no screen renders, and the раскладка for that cloth would read as
// «not bound» while the row insists it is. Both CHECKs are asserted, and asserted to be IDENTICAL —
// a drift between the pattern's list and the alias's would split one vocabulary in two.
func TestFabricPurposeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0267_pattern_purpose_binding.sql")
	const anchor = "fabric_purpose REGEXP"
	if n := strings.Count(content, anchor); n != 2 {
		t.Fatalf("expected exactly 2 fabric_purpose CHECKs (pattern + alias), found %d", n)
	}
	want := mapKeysAsStrings(entity.ValidTechCardBomPurposes)
	first := strings.Index(content, anchor)
	assertSameSet(t, "TechCardSizePattern.fabric_purpose",
		extractDBEnumValues(t, content[first:], anchor, 120), want)
	second := strings.Index(content[first+len(anchor):], anchor) + first + len(anchor)
	assertSameSet(t, "TechCardPieceDxfAlias.fabric_purpose",
		extractDBEnumValues(t, content[second:], anchor, 120), want)
}

// TestPieceCutSymmetryDBCheckNoDrift is the entity<->DB leg for КАК КРОИТСЯ (0275):
// entity.ValidTechCardPieceCutSymmetries <-> the DB CHECK chk_tcp_cut_symmetry. The entity<->proto
// leg is TestPieceCutSymmetryEnumNoDrift in internal/dto.
//
// The anchor is deliberately `cut_symmetry REGEXP`, which occurs exactly once in the file: the
// second CHECK in 0275 (chk_tcp_mirrored_needs_even_count) compares with `<>`, not REGEXP, so it
// cannot be picked up by mistake. That also means the pattern literal in 0275 must stay UNPREFIXED —
// extractDBEnumValues matches `REGEXP` followed directly by quotes, and a `_utf8mb4` in between
// would make this test fail to find the value list rather than fail to compare it.
func TestPieceCutSymmetryDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0275_piece_cut_symmetry.sql")
	dbValues := extractDBEnumValues(t, content, "cut_symmetry REGEXP", 120)
	assertSameSet(t, "TechCardPieceCutSymmetry", dbValues,
		mapKeysAsStrings(entity.ValidTechCardPieceCutSymmetries))
}

// TestPieceCutSymmetryDBCheckIsCaseClosed guards the OTHER half of chk_tcp_cut_symmetry, which the
// drift test above cannot see: tech_card_piece is utf8mb4_0900_ai_ci and REGEXP inherits the column's
// collation, so the alternation alone accepts 'MIRRORED' and 'Fold' — it refuses 'mirror' and nothing
// about case. The STRCMP-over-binary comparison is what actually closes the vocabulary (precedent:
// chk_tcpdb_fabric_purpose in 0267).
//
// This is the static half; TestPieceCutSymmetryDBCheckIsCaseSensitive in internal/store proves the
// behaviour against the real migrated table. Both exist because this one runs without a database and
// so runs everywhere, while only the other one can prove MySQL agrees.
func TestPieceCutSymmetryDBCheckIsCaseClosed(t *testing.T) {
	content := readMigrationFile(t, "0275_piece_cut_symmetry.sql")
	const guard = "STRCMP(CAST(cut_symmetry AS BINARY), CAST(LOWER(cut_symmetry) AS BINARY)) = 0"
	if !strings.Contains(content, guard) {
		t.Fatalf("chk_tcp_cut_symmetry must close the vocabulary against case as well as spelling; missing %q", guard)
	}
	// The guard has to sit INSIDE the vocabulary CHECK, not merely somewhere in the file, and it has to
	// come AFTER the REGEXP so extractDBEnumValues' anchor still finds the alternation.
	stmt := strings.Index(content, "chk_tcp_cut_symmetry CHECK")
	if stmt < 0 {
		t.Fatal("named vocabulary CHECK not found")
	}
	rx := strings.Index(content[stmt:], "cut_symmetry REGEXP")
	gd := strings.Index(content[stmt:], guard)
	if rx < 0 || gd < 0 || gd < rx {
		t.Fatalf("the case guard must live inside chk_tcp_cut_symmetry and follow the REGEXP (regexp at %d, guard at %d)", rx, gd)
	}
}

// TestBomKindDBCheckNoDrift is the entity<->DB leg for ЧТО ЭТО ЗА ПОЗИЦИЯ (0278):
// entity.TechCardBomKind/ValidTechCardBomKinds <-> the DB CHECK chk_bom_item_kind. The entity<->proto
// leg is TestBomKindEnumNoDrift in internal/dto.
//
// It guards the same thing TestBomPurposeDBCheckNoDrift guards on the other half of the BOM, and for
// the same reason: the list is closed BECAUSE the field is a grouping key, so a value one side
// accepts and the other does not puts a line in a bucket no screen renders — and the mismatch fails
// nowhere visible. Note that entity.ValidTechCardBomKinds is itself derived from bomKindHomeSection,
// so this one assertion covers the vocabulary AND the pairing table's key set at once.
//
// The window is wide (52 values) — extractDBEnumValues bounds its search from the anchor, and a
// window shorter than the alternation would fail to FIND the list rather than fail to compare it.
func TestBomKindDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0278_bom_item_kind.sql")
	dbValues := extractDBEnumValues(t, content, "kind REGEXP", 800)
	assertSameSet(t, "TechCardBomKind", dbValues, mapKeysAsStrings(entity.ValidTechCardBomKinds))
}

// TestBomKindDBCheckIsCaseClosed guards the half of chk_bom_item_kind the drift test cannot see.
// REGEXP inherits the column's collation, which is case-INSENSITIVE on both the utf8mb3 of prod and
// the utf8mb4_0900_ai_ci of the container tests, so the alternation alone accepts 'ZIPPER'. It
// refuses 'zip' and nothing about case. STRCMP over a BINARY cast is what actually closes the
// vocabulary (precedent: chk_bom_item_purpose in 0265, chk_tcp_cut_symmetry in 0275).
func TestBomKindDBCheckIsCaseClosed(t *testing.T) {
	content := readMigrationFile(t, "0278_bom_item_kind.sql")
	const guard = "STRCMP(CAST(kind AS BINARY), CAST(LOWER(kind) AS BINARY)) = 0"
	stmt := strings.Index(content, "chk_bom_item_kind CHECK")
	if stmt < 0 {
		t.Fatal("named vocabulary CHECK chk_bom_item_kind not found")
	}
	// The guard must live INSIDE the vocabulary CHECK and come AFTER the REGEXP, so
	// extractDBEnumValues' anchor still finds the alternation.
	rx := strings.Index(content[stmt:], "kind REGEXP")
	gd := strings.Index(content[stmt:], guard)
	if rx < 0 || gd < 0 || gd < rx {
		t.Fatalf("the case guard must live inside chk_bom_item_kind and follow the REGEXP (regexp at %d, guard at %d)", rx, gd)
	}
}

// TestBomKindNoteCheckIsNullSafe locks the one comparison in 0278 that is wrong in the obvious form
// and silent about it. `kind = 'other'` yields NULL when kind IS NULL, and MySQL treats a CHECK that
// evaluates to NULL as SATISFIED — so the obvious spelling catches a note on kind='zipper' and lets
// a note through on kind IS NULL, which is the state of EVERY row that predates the migration and
// every line nobody has classified since. That is precisely where a note would become a shadow kind:
// on the lines that have no kind at all. Only the NULL-safe `<=>` closes it (as in
// chk_bom_item_purpose_note, 0265).
func TestBomKindNoteCheckIsNullSafe(t *testing.T) {
	content := readMigrationFile(t, "0278_bom_item_kind.sql")
	stmt := strings.Index(content, "chk_bom_item_kind_note CHECK")
	if stmt < 0 {
		t.Fatal("named CHECK chk_bom_item_kind_note not found")
	}
	end := stmt + 140
	if end > len(content) {
		end = len(content)
	}
	scope := content[stmt:end]
	if !strings.Contains(scope, "kind <=> ''other''") {
		t.Errorf("chk_bom_item_kind_note must compare NULL-safely with <=>; got %q", scope)
	}
	if strings.Contains(scope, "kind = ''other''") {
		t.Errorf("chk_bom_item_kind_note uses `kind = 'other'`, which MySQL treats as SATISFIED on every kind IS NULL row: %q", scope)
	}
}

// TestLabelTypeDBCheckNoDrift is the entity<->DB leg for the label vocabulary (0070):
// entity.TechCardLabelType/ValidTechCardLabelTypes <-> the CHECK on tech_card_label.label_type. The
// entity<->proto leg is TestLabelTypeEnumNoDrift in internal/dto.
//
// It had no drift guard until 0278, which is what makes it load-bearing now: 0278 excludes
// section='label' from `kind` BECAUSE label_type is the sole owner of this vocabulary. That
// exclusion is only honest while label_type actually stays in lockstep with the enum the client
// renders — an entity value the CHECK refuses would leave the "already answered" question with no
// answer at all, on a card whose LABELS sign-off already hashed the old value.
func TestLabelTypeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0070_add_tech_card_production.sql")
	dbValues := extractDBEnumValues(t, content, "label_type REGEXP", 120)
	assertSameSet(t, "TechCardLabelType", dbValues, mapKeysAsStrings(entity.ValidTechCardLabelTypes))
}

// TestConsumptionSourceDBCheckNoDrift is the entity<->DB leg for the norm-provenance vocabulary:
// entity.ValidConsumptionSources <-> chk_tccu_consumption_source on tech_card_colorway_usage.
//
// The anchor is 0294 (which added 'dxf'), not 0261 (which created the constraint), and that is the
// rule for a recreated CHECK: the file that owns the CURRENT vocabulary is the one whose literal
// list must match. Anchoring on the creating migration would have kept the test passing while the
// live constraint moved on — the drift would be invisible in exactly the direction it matters.
//
// It is load-bearing because the source decides MONEY: the whole point of 'marker' is that costing
// must NOT gross it up, while 'manual' and 'dxf' must. A value Go accepts and the CHECK refuses
// fails the recipe save on a constraint the operator cannot interpret; a value the CHECK accepts and
// Go does not silently lands in the wastage branch of every consumer.
func TestConsumptionSourceDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0294_usage_consumption_source_dxf.sql")
	dbValues := extractDBEnumValues(t, content, "chk_tccu_consumption_source CHECK", 200)
	assertSameSet(t, "ConsumptionSource", dbValues, mapKeysAsStrings(entity.ValidConsumptionSources))
}

// --- 0306: машинки и режимы ВТО -------------------------------------------------------------------
//
// Ten vocabularies land at once, and SEVEN of them are written into the schema TWICE — once on
// tech_card_operation (the step's override) and once on tech_card_equipment_profile (the card's
// default). That doubling is the whole reason these tests are worth their length: a widening applied
// to one CHECK and forgotten on the other fails NOWHERE at write time on the side that was widened,
// and fails with a bare 3819 on the side that was not — on whichever of the two the operator happens
// to touch second. So each pair is asserted against the SAME entity slice, which is what makes the
// two copies provably one vocabulary.
//
// The anchor migration is 0306 for every one of them, including attachment_kind: 0289 created that
// CHECK, 0306 recreated it with three more tokens, and the rule for a recreated CHECK is that the
// file owning the CURRENT vocabulary is the one the test must read (the 0294-over-0261 precedent
// above). Anchoring on 0289 would keep this passing while the live constraint moved on.

const migration0306 = "0306_operation_machines.sql"

// assertPairedCheckNoDrift asserts BOTH schema copies of one vocabulary — the operation's override
// CHECK and the profile's default CHECK — against the single entity slice.
func assertPairedCheckNoDrift(t *testing.T, label, opAnchor, eqpAnchor string, window int, tokens []string) {
	t.Helper()
	content := readMigrationFile(t, migration0306)
	assertSameSet(t, label+" (tech_card_operation)",
		extractDBEnumValues(t, content, opAnchor, window), tokens)
	assertSameSet(t, label+" (tech_card_equipment_profile)",
		extractDBEnumValues(t, content, eqpAnchor, window), tokens)
}

// TestMachineTypeDBCheckNoDrift is the entity<->DB leg for «на чём» (0306): entity.MachineTypeTokens
// <-> chk_op_machine_type. The profile side of the same vocabulary is NOT a separate CHECK — it is
// folded into the union chk_eqp_equipment, asserted by TestEquipmentUnionDBCheckNoDrift below.
//
// It is load-bearing beyond the usual drift argument: migration 0306 step 5 writes nine of these
// tokens into existing rows, and the digest's compat projection reads them back through
// entity.MachineTypeLegacyToken. A machine the CHECK refuses would fail the migration itself; a
// machine the entity does not know would hash as an unmapped tail and stale every signed
// CONSTRUCTION approval on the card.
func TestMachineTypeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, migration0306)
	dbValues := extractDBEnumValues(t, content, "chk_op_machine_type CHECK", 600)
	assertSameSet(t, "TechCardMachineType", dbValues, entity.MachineTypeTokens)
}

// TestPressEquipmentDBCheckNoDrift is the entity<->DB leg for the ВТО half: entity.PressEquipmentTokens
// <-> chk_op_press_equipment. As with the machine list, the profile side lives in the union CHECK.
func TestPressEquipmentDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, migration0306)
	dbValues := extractDBEnumValues(t, content, "chk_op_press_equipment CHECK", 250)
	assertSameSet(t, "TechCardPressEquipment", dbValues, entity.PressEquipmentTokens)
}

// TestEquipmentUnionDBCheckNoDrift guards the one CHECK in 0306 that is not a copy of any single Go
// slice: tech_card_equipment_profile.equipment holds a machine token when kind='machine' and a press
// token when kind='press', so its vocabulary is the UNION of the two — with 'other' appearing ONCE,
// because it is legal under both kinds and a duplicate in a REGEXP alternation is a silent invitation
// to let the two lists drift apart while the CHECK still "matches".
//
// assertSameSet already fails on a duplicated DB value, so the single-'other' rule is enforced by
// construction rather than by a second assertion.
func TestEquipmentUnionDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, migration0306)
	dbValues := extractDBEnumValues(t, content, "chk_eqp_equipment CHECK", 700)

	union := make([]string, 0, len(entity.MachineTypeTokens)+len(entity.PressEquipmentTokens))
	seen := make(map[string]bool)
	for _, tok := range append(append([]string{}, entity.MachineTypeTokens...), entity.PressEquipmentTokens...) {
		if seen[tok] {
			continue
		}
		seen[tok] = true
		union = append(union, tok)
	}
	assertSameSet(t, "equipment union (machine + press)", dbValues, union)

	// The overlap is exactly {other} today. If a future token lands in both slices the union above
	// still works, but the CHECK author has to know it happened — a machine and a press sharing a
	// token means `equipment` alone stops identifying the kind.
	overlap := 0
	machines := make(map[string]bool, len(entity.MachineTypeTokens))
	for _, m := range entity.MachineTypeTokens {
		machines[m] = true
	}
	for _, p := range entity.PressEquipmentTokens {
		if machines[p] {
			if p != "other" {
				t.Errorf("machine and press vocabularies both claim %q; `equipment` no longer identifies the kind", p)
			}
			overlap++
		}
	}
	if overlap != 1 {
		t.Errorf("expected exactly one shared token ('other') between the machine and press vocabularies, found %d", overlap)
	}
}

// TestNeedleTypeDBCheckNoDrift — entity.NeedleTypeTokens <-> chk_op_needle_type / chk_eqp_needle_type.
func TestNeedleTypeDBCheckNoDrift(t *testing.T) {
	assertPairedCheckNoDrift(t, "TechCardNeedleType",
		"chk_op_needle_type CHECK", "chk_eqp_needle_type CHECK", 250, entity.NeedleTypeTokens)
}

// TestThreadTensionDBCheckNoDrift — entity.ThreadTensionTokens <-> chk_op_thread_tension /
// chk_eqp_thread_tension.
func TestThreadTensionDBCheckNoDrift(t *testing.T) {
	assertPairedCheckNoDrift(t, "TechCardThreadTension",
		"chk_op_thread_tension CHECK", "chk_eqp_thread_tension CHECK", 200, entity.ThreadTensionTokens)
}

// TestPressClothDBCheckNoDrift — entity.PressClothTokens <-> chk_op_press_cloth / chk_eqp_press_cloth.
// 'none' being IN the vocabulary is the point of the whole token: NULL means «inherit the profile»,
// so without a spelled-out 'none' a step could not cancel the profile's press cloth.
func TestPressClothDBCheckNoDrift(t *testing.T) {
	assertPairedCheckNoDrift(t, "TechCardPressCloth",
		"chk_op_press_cloth CHECK", "chk_eqp_press_cloth CHECK", 250, entity.PressClothTokens)
	for _, tok := range entity.PressClothTokens {
		if tok == "none" {
			return
		}
	}
	t.Error("PressClothTokens lost 'none': without it a step cannot cancel the profile's press cloth, and NULL already means «inherit»")
}

// TestAttachmentKindDBCheckNoDrift — entity.AttachmentKindTokens <-> chk_op_attachment_kind (widened
// by 0306 step 7) / chk_eqp_attachment. Same 'none' argument as the press cloth: it was genuinely
// absent before profiles existed (0289's own comment said so) and is required now.
func TestAttachmentKindDBCheckNoDrift(t *testing.T) {
	assertPairedCheckNoDrift(t, "TechCardAttachmentKind",
		"chk_op_attachment_kind CHECK", "chk_eqp_attachment CHECK", 350, entity.AttachmentKindTokens)
	for _, tok := range entity.AttachmentKindTokens {
		if tok == "walking_foot" {
			t.Error("walking_foot is deliberately NOT an attachment: industrially it is a machine with unison/top feed — a transport property that belongs next to bed_type")
		}
	}
}

// TestBedTypeDBCheckNoDrift — entity.BedTypeTokens <-> chk_eqp_bed_type. Profile-only by design: the
// bed is machine IDENTITY, so a step cannot override it (a different bed is a different machine),
// and there is deliberately no chk_op_bed_type to pair this with.
func TestBedTypeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, migration0306)
	dbValues := extractDBEnumValues(t, content, "chk_eqp_bed_type CHECK", 200)
	assertSameSet(t, "TechCardBedType", dbValues, entity.BedTypeTokens)
	if strings.Contains(content, "chk_op_bed_type") {
		t.Error("bed_type must not become a per-step override: it is machine identity (see 0306 header)")
	}
}

// TestAutomationLevelDBCheckNoDrift — entity.AutomationLevelTokens <-> chk_eqp_automation.
// Profile-only for the same reason as the bed, and with no 'other' member: it is an ORDERED SCALE,
// and a scale with an «other» has stopped being one.
func TestAutomationLevelDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, migration0306)
	dbValues := extractDBEnumValues(t, content, "chk_eqp_automation CHECK", 200)
	assertSameSet(t, "TechCardAutomationLevel", dbValues, entity.AutomationLevelTokens)
	for _, v := range dbValues {
		if v == "other" {
			t.Error("automation is an ordered scale and must not carry 'other'")
		}
	}
	if strings.Contains(content, "chk_op_automation") {
		t.Error("automation must not become a per-step override: it is machine identity (see 0306 header)")
	}
}

// TestOperationTypeDBCheckNoDrift is the entity<->DB leg for the STORED operation type after the
// split: entity.OperationTypeTokens <-> chk_op_operation_type (0306 step 6).
//
// The nine legacy tokens must NOT be here, and that is the assertion with teeth. They stay alive
// forever on the WIRE (release snapshots are protojson carrying those names), but 0306 step 5
// rewrites every stored row into (machine, machine_type), so a legacy token in the CHECK would mean
// the schema still admits a shape the canonicalisation is supposed to have made impossible — and the
// digest's compat projection, which reads `machine` + machine_type, would silently disagree with a
// row that kept the old spelling.
func TestOperationTypeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, migration0306)
	dbValues := extractDBEnumValues(t, content, "chk_op_operation_type CHECK", 250)
	assertSameSet(t, "TechCardOperationType (stored)", dbValues, entity.OperationTypeTokens)

	for _, v := range dbValues {
		if _, isLegacy := entity.LegacyOperationMachineType[entity.TechCardOperationType(v)]; isLegacy {
			t.Errorf("chk_op_operation_type still admits the legacy token %q, which 0306 step 5 rewrites away", v)
		}
	}
}

// Test0306VocabulariesAreCaseClosed guards the half of every 0306 vocabulary CHECK that the drift
// tests above cannot see. REGEXP inherits the column's collation, which is case-INSENSITIVE on both
// the utf8mb3_general_ci of prod and the utf8mb4_0900_ai_ci of the container, so the alternation
// alone accepts 'OVERLOCK' and 'Iron'. It refuses 'overlok' and nothing about case. The STRCMP over a
// BINARY cast is what actually closes the vocabulary.
//
// This is not theoretical here: 0076's operation_type CHECK was written WITHOUT the guard, prod
// therefore admits 'LOCKSTITCH' legally, and 0306 has to LOWER the column (step 3) before it dares
// add a strict CHECK — because ADD CONSTRAINT re-validates the whole table and a failure there halts
// startup. This test is what keeps the next vocabulary from repeating that.
func Test0306VocabulariesAreCaseClosed(t *testing.T) {
	content := readMigrationFile(t, migration0306)
	for _, c := range []struct{ constraint, column string }{
		{"chk_op_machine_type", "machine_type"},
		{"chk_op_press_equipment", "press_equipment"},
		{"chk_op_needle_type", "needle_type"},
		{"chk_op_thread_tension", "thread_tension"},
		{"chk_op_press_cloth", "press_cloth"},
		{"chk_op_attachment_kind", "attachment_kind"},
		{"chk_op_operation_type", "operation_type"},
		{"chk_eqp_kind", "kind"},
		{"chk_eqp_equipment", "equipment"},
		{"chk_eqp_needle_type", "needle_type"},
		{"chk_eqp_bed_type", "bed_type"},
		{"chk_eqp_automation", "automation"},
		{"chk_eqp_thread_tension", "thread_tension"},
		{"chk_eqp_attachment", "attachment_kind"},
		{"chk_eqp_press_op_type", "press_operation_type"},
		{"chk_eqp_press_cloth", "press_cloth"},
	} {
		stmt := strings.Index(content, c.constraint+" CHECK")
		if stmt < 0 {
			t.Errorf("named vocabulary CHECK %s not found in 0306", c.constraint)
			continue
		}
		guard := "STRCMP(CAST(" + c.column + " AS BINARY), CAST(LOWER(" + c.column + ") AS BINARY)) = 0"
		rx := strings.Index(content[stmt:], c.column+" REGEXP")
		gd := strings.Index(content[stmt:], guard)
		if rx < 0 {
			t.Errorf("%s: no REGEXP alternation on %s", c.constraint, c.column)
			continue
		}
		// The guard has to sit INSIDE this CHECK (i.e. before the next constraint starts) and AFTER
		// the REGEXP, so extractDBEnumValues' anchor still finds the alternation.
		next := strings.Index(content[stmt+len(c.constraint):], "CONSTRAINT chk_")
		limit := len(content) - stmt
		if next >= 0 {
			limit = next + len(c.constraint)
		}
		if gd < 0 || gd < rx || gd > limit {
			t.Errorf("%s must close %s against case as well as spelling: STRCMP guard missing or outside the CHECK (regexp at %d, guard at %d, next constraint at %d)",
				c.constraint, c.column, rx, gd, limit)
		}
	}
}
