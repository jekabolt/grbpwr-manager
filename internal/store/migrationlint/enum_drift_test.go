package migrationlint

import (
	"fmt"
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
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
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
	assertSameSetNamed(t, label, "DB CHECK", dbValues, "entity set", entityValues)
}

// assertSameSetNamed is assertSameSet with both sides named, so the third leg of a vocabulary (the
// proto enum) can be compared with a message that says which of the two lists is which. A failure
// that reads "DB CHECK allows X but the entity set does not" when the two lists are actually the
// proto enum and the entity slice sends the reader to the wrong file.
func assertSameSetNamed(t *testing.T, label, leftName string, left []string, rightName string, right []string) {
	t.Helper()
	l := make(map[string]bool, len(left))
	for _, v := range left {
		if l[v] {
			t.Errorf("%s: %s value list has a duplicate: %q (%v)", label, leftName, v, left)
		}
		l[v] = true
	}
	r := make(map[string]bool, len(right))
	for _, v := range right {
		r[v] = true
	}
	for v := range l {
		if !r[v] {
			t.Errorf("%s: %s allows %q but the %s does not", label, leftName, v, rightName)
		}
	}
	for v := range r {
		if !l[v] {
			t.Errorf("%s: %s allows %q but the %s does not", label, rightName, v, leftName)
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
// The window is wide (54 values) — extractDBEnumValues bounds its search from the anchor, and a
// window shorter than the alternation would fail to FIND the list rather than fail to compare it.
//
// The anchor is 0324, not 0278: the wave recreated chk_bom_item_kind ONCE with the union of both
// phases (+seam_sealing_tape, +embroidery_stabilizer). It is also the one dictionary here whose
// sentinel is spelled ..._UNSET and whose numbering carries a promised hole (54, reserved by promise
// for the deferred wet_chemical) — see protoEnumTokens for why both are declared and not inferred.
func TestBomKindDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardBomKind",
		protoNames: pb_common.TechCardBomKind_name,
		prefix:     "TECH_CARD_BOM_KIND_",
		zeroMember: "TECH_CARD_BOM_KIND_UNSET",
		check:      "chk_bom_item_kind CHECK",
		window:     800,
		tokens:     mapKeysAsStrings(entity.ValidTechCardBomKinds),
		holes:      []int32{54},
	})
}

// TestBomKindDBCheckIsCaseClosed guards the half of chk_bom_item_kind the drift test cannot see.
// REGEXP inherits the column's collation, which is case-INSENSITIVE on both the utf8mb3 of prod and
// the utf8mb4_0900_ai_ci of the container tests, so the alternation alone accepts 'ZIPPER'. It
// refuses 'zip' and nothing about case. STRCMP over a BINARY cast is what actually closes the
// vocabulary (precedent: chk_bom_item_purpose in 0265, chk_tcp_cut_symmetry in 0275).
func TestBomKindDBCheckIsCaseClosed(t *testing.T) {
	// 0324 owns the current constraint (see TestBomKindDBCheckNoDrift): a recreated CHECK that drops
	// the STRCMP guard would reopen the vocabulary to 'ZIPPER' while the token list still matched.
	content := readMigrationFile(t, migration0324)
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
//
// Each half names its OWN anchor migration, and that is not symmetry for its own sake: the 0324 wave
// recreated both copies of press_cloth and touched neither copy of needle_type, so a single hardcoded
// file would have to be wrong for one of them. Reading a CHECK that no longer exists in the schema is
// the failure mode with no symptom — the test stays green over a live constraint it never looked at.
func assertPairedCheckNoDrift(t *testing.T, label, opMigration, opAnchor, eqpMigration, eqpAnchor string, window int, tokens []string) {
	t.Helper()
	assertSameSet(t, label+" (tech_card_operation)",
		extractDBEnumValues(t, readMigrationFile(t, opMigration), opAnchor, window), tokens)
	assertSameSet(t, label+" (tech_card_equipment_profile)",
		extractDBEnumValues(t, readMigrationFile(t, eqpMigration), eqpAnchor, window), tokens)
}

// TestMachineTypeDBCheckNoDrift is the entity<->DB leg for «на чём»: entity.MachineTypeTokens <->
// chk_op_machine_type. The profile side of the same vocabulary is NOT a separate CHECK — it is folded
// into the union chk_eqp_equipment, asserted by TestEquipmentUnionDBCheckNoDrift below.
//
// The anchor moved to 0324, which recreated this CHECK with seam_taping and ultrasonic_welder. 0306
// still contains the narrower list, and reading it would keep this test green while the live
// constraint carried two tokens nobody compared against anything.
//
// It is load-bearing beyond the usual drift argument: migration 0306 step 5 writes nine of these
// tokens into existing rows, and the digest's compat projection reads them back through
// entity.MachineTypeLegacyToken. A machine the CHECK refuses would fail the migration itself; a
// machine the entity does not know would hash as an unmapped tail and stale every signed
// CONSTRUCTION approval on the card.
func TestMachineTypeDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardMachineType",
		protoNames: pb_common.TechCardMachineType_name,
		prefix:     "TECH_CARD_MACHINE_TYPE_",
		zeroMember: "TECH_CARD_MACHINE_TYPE_UNKNOWN",
		check:      "chk_op_machine_type CHECK",
		window:     600,
		tokens:     entity.MachineTypeTokens,
	})
}

// TestPressEquipmentDBCheckNoDrift is the entity<->DB leg for the ВТО half: entity.PressEquipmentTokens
// <-> chk_op_press_equipment. As with the machine list, the profile side lives in the union CHECK.
//
// THIS ONE STAYS ON 0306, and staying is the assertion. The 0324 wave deliberately did not recreate
// chk_op_press_equipment (conveyor_dryer is deferred), so 0306 still owns the current vocabulary.
// Moving the anchor «for company» with its neighbours would point the test at a file that never
// mentions this constraint — extractDBEnumValues would fail to find the anchor, and the fix an
// unsuspecting reader would reach for is to loosen the test.
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
	// 0324 recreated this CHECK: both new machines join the union, the press half is untouched, and
	// 'other' still has to appear exactly once across the two halves.
	content := readMigrationFile(t, migration0324)
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
		migration0306, "chk_op_needle_type CHECK",
		migration0306, "chk_eqp_needle_type CHECK", 250, entity.NeedleTypeTokens)
}

// TestThreadTensionDBCheckNoDrift — entity.ThreadTensionTokens <-> chk_op_thread_tension /
// chk_eqp_thread_tension.
func TestThreadTensionDBCheckNoDrift(t *testing.T) {
	assertPairedCheckNoDrift(t, "TechCardThreadTension",
		migration0306, "chk_op_thread_tension CHECK",
		migration0306, "chk_eqp_thread_tension CHECK", 200, entity.ThreadTensionTokens)
}

// TestPressClothDBCheckNoDrift — entity.PressClothTokens <-> chk_op_press_cloth / chk_eqp_press_cloth.
// 'none' being IN the vocabulary is the point of the whole token: NULL means «inherit the profile»,
// so without a spelled-out 'none' a step could not cancel the profile's press cloth.
//
// BOTH halves moved to 0324, which recreated both with silicone_paper. They have to move together:
// one half read from 0324 and the other from 0306 would compare the two copies against different
// lists and so stop proving they are one vocabulary — which is the only thing this pairing is for.
func TestPressClothDBCheckNoDrift(t *testing.T) {
	assertPairedCheckNoDrift(t, "TechCardPressCloth",
		migration0324, "chk_op_press_cloth CHECK",
		migration0324, "chk_eqp_press_cloth CHECK", 250, entity.PressClothTokens)
	assertSameSetNamed(t, "TechCardPressCloth", "proto enum",
		protoEnumTokens(t, waveVocabulary{
			label:      "TechCardPressCloth",
			protoNames: pb_common.TechCardPressCloth_name,
			prefix:     "TECH_CARD_PRESS_CLOTH_",
			zeroMember: "TECH_CARD_PRESS_CLOTH_UNKNOWN",
		}), "entity set", entity.PressClothTokens)
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
		migration0306, "chk_op_attachment_kind CHECK",
		migration0306, "chk_eqp_attachment CHECK", 350, entity.AttachmentKindTokens)
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
// The proto leg is deliberately absent here and only here: TechCardOperationType keeps its nine
// legacy machine members on the WIRE forever, so the enum is WIDER than the stored vocabulary by
// design and a set comparison against it would fail on a healthy contract. The legacy-token
// assertion below is the shape that check takes for this vocabulary.
func TestOperationTypeDBCheckNoDrift(t *testing.T) {
	// 0324 recreated this CHECK with nine new verbs (hardware_set … wet_process); 0306 owns only the
	// pre-wave list.
	content := readMigrationFile(t, migration0324)
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

// --- 0324: виды операций ---------------------------------------------------------------------------
//
// Seventeen new closed vocabularies land on tech_card_operation at once, and the same file recreates
// seven CHECKs that already existed (operation_type, machine_type, press_cloth ×2, topstitch_mode,
// equipment, bom_item_kind). That split is exactly what the anchors above encode: a test whose CHECK
// the wave recreated now reads 0324, and a test whose CHECK the wave did NOT touch keeps reading the
// file that owns its current vocabulary (chk_op_press_equipment stays on 0306). Retargeting a test
// «for company» is worse than leaving it alone — it points the anchor at a file that never mentions
// the constraint, and the honest-looking fix for the resulting failure is to weaken the test.
//
// Each vocabulary below is asserted on THREE lists, not two: the proto enum members (what a client
// can send), the CHECK's token list (what the column accepts) and the entity slice (what Go
// validates before either). Two of the three agreeing is precisely the state that ships a value the
// third one refuses — a save that fails on a bare 3819 the operator cannot read, or a value that
// reaches the column and no screen renders.
const migration0324 = "0324_operation_kinds.sql"

// 0325 добавляет ДВА словаря (press_action, press_toward) и ПЕРЕСОЗДАЁТ шесть CHECK'ов 0324,
// дописывая в каждый выход `other`. Тест словаря обязан читать ФАЙЛ, ВЛАДЕЮЩИЙ ТЕКУЩИМ СПИСКОМ:
// у шести расширенных это 0325, у остальных одиннадцати — по-прежнему 0324. Оставить шестерых на
// 0324 значило бы сверять entity с УЖЕ ПЕРЕПИСАННЫМ списком и получить красноту на здоровой схеме;
// перевести «за компанию» всех — направить якорь в файл, где половины констрейнтов нет вовсе.
const migration0325 = "0325_press_action_toward.sql"

// 0326 СУЖАЕТ ровно один словарь — topstitch_mode, снимая член `width`. Это единственное сужение во
// всём семействе, и владение переходит к нему по тому же правилу, что у 0325: тест словаря читает
// ФАЙЛ, ВЛАДЕЮЩИЙ ТЕКУЩИМ СПИСКОМ. Оставить topstitch на 0324 значило бы сверять entity со списком,
// который 0326 уже переписал, и получить красноту на здоровой схеме.
const migration0326 = "0326_topstitch_drop_width.sql"

// waveCheckWindow bounds the search from a CHECK's anchor. The longest new alternation
// (label_attach_stitch) ends 192 characters past its anchor; a window shorter than the list would
// make extractDBEnumValues fail to FIND it rather than fail to compare it.
const waveCheckWindow = 300

// waveVocabulary describes one dictionary of the wave in the three places it exists at once.
type waveVocabulary struct {
	label      string           // what to print when the three lists disagree
	protoNames map[int32]string // the generated enum's _name map
	prefix     string           // member prefix stripped to get the stored token
	zeroMember string           // the sentinel member's FULL name — declared, never inferred
	check      string           // anchor «<constraint> CHECK», unique inside the owning migration
	migration  string           // файл, ВЛАДЕЮЩИЙ текущим списком токенов; "" = migration0324
	window     int              // 0 = waveCheckWindow
	tokens     []string         // the entity slice: the single source the validator reads
	holes      []int32          // enum numbers promised to a later phase and deliberately left empty
	retired    []int32          // enum numbers `reserved` in the .proto: снято навсегда, вернуть нельзя
}

// protoEnumTokens derives the STORABLE token list of a proto enum from its generated _name map:
// strip the member prefix, lowercase what is left. That derivation is what keeps eighteen tests
// short, and it has exactly one way to break in silence — the sentinel member.
//
// Every vocabulary of this wave spells its zero member ..._UNKNOWN. TechCardBomKind spells it
// ..._UNSET (so does TechCardBomPurpose), because there the zero is not «не указано» but «ещё не
// классифицировано». A helper that recognised the sentinel BY NAME would therefore treat UNSET as an
// ordinary member on exactly one dictionary and derive the token "unset", which no CHECK and no
// entity set contains — the test would fail, but for the wrong reason, and the reader would go
// looking for a drift that is not there. Spelled the other way round («skip whatever ends in
// UNKNOWN») it would be worse: a renamed sentinel would pass unnoticed. So the sentinel is found by
// NUMBER — proto guarantees the zero — and its spelling is asserted against what the caller declares.
//
// The numbering check is here for the same reason. `holes` are numbers the contract PROMISED to a
// later phase (TechCardBomKind skips 54 for the deferred wet_chemical); they are deliberately NOT
// `reserved` in the .proto, because reserved would close the number forever and a promise is the
// opposite of that. A density check that did not know about them would go red on a healthy contract;
// no density check at all would let a typo'd number — a member at 65 instead of 56 — pass as a new
// value and quietly become a hole nobody promised anyone.
func protoEnumTokens(t *testing.T, v waveVocabulary) []string {
	t.Helper()
	if got := v.protoNames[0]; got != v.zeroMember {
		t.Fatalf("%s: нулевой член enum'а называется %q, а тест объявил %q — sentinel is skipped by number, so a rename must be declared here (TechCardBomKind uses ..._UNSET, everything else ..._UNKNOWN)",
			v.label, got, v.zeroMember)
	}
	hole := make(map[int32]bool, len(v.holes))
	for _, h := range v.holes {
		hole[h] = true
		if name, taken := v.protoNames[h]; taken {
			t.Errorf("%s: номер %d объявлен дырой (обещан отложенной фазе), но занят членом %s", v.label, h, name)
		}
	}
	// RETIRED — ПРОТИВОПОЛОЖНОСТЬ ОБЕЩАННОЙ ДЫРЕ, и потому отдельное поле, а не запись в holes.
	// Обещанный номер ЖДЁТ своей фазы и намеренно НЕ reserved; снятый закрыт reserved НАВСЕГДА,
	// потому что отданный новому смыслу он читался бы старым клиентом как прежний член. В
	// сгенерированном _name map оба выглядят одинаково — как отсутствующий ключ, — так что без
	// этого различия густота нумерации либо краснела бы на здоровом контракте, либо принимала бы
	// опечатку в номере за законный пропуск. Тот же довод, что у retired в
	// TestOperationContractHolesStayOpen, только на уровне enum'а, а не сообщения.
	for _, r := range v.retired {
		hole[r] = true
		if name, taken := v.protoNames[r]; taken {
			t.Errorf("%s: номер %d объявлен снятым (reserved), но занят членом %s — reserved и живой член взаимоисключающи", v.label, r, name)
		}
	}
	highest := int32(0)
	for n := range v.protoNames {
		if n > highest {
			highest = n
		}
	}
	for _, h := range v.holes {
		if h > highest {
			highest = h
		}
	}
	tokens := make([]string, 0, len(v.protoNames))
	for n := int32(0); n <= highest; n++ {
		name, ok := v.protoNames[n]
		if !ok {
			if !hole[n] {
				t.Errorf("%s: в нумерации enum'а дыра на %d, и она нигде не объявлена — либо это опечатка в номере, либо пропуск, который надо внести в holes вместе с причиной", v.label, n)
			}
			continue
		}
		if !strings.HasPrefix(name, v.prefix) {
			t.Errorf("%s: член %s не начинается с %q — вывод токена из имени сломан, и сравнение ниже сравнивает мусор", v.label, name, v.prefix)
			continue
		}
		if n == 0 {
			continue
		}
		tokens = append(tokens, strings.ToLower(strings.TrimPrefix(name, v.prefix)))
	}
	return tokens
}

// assertWaveVocabularyNoDrift asserts the three lists of one wave dictionary against each other.
func assertWaveVocabularyNoDrift(t *testing.T, v waveVocabulary) {
	t.Helper()
	window := v.window
	if window == 0 {
		window = waveCheckWindow
	}
	migration := v.migration
	if migration == "" {
		migration = migration0324
	}
	dbValues := extractDBEnumValues(t, readMigrationFile(t, migration), v.check, window)
	assertSameSet(t, v.label, dbValues, v.tokens)
	assertSameSetNamed(t, v.label, "proto enum", protoEnumTokens(t, v), "entity set", v.tokens)
}

// assertVocabularyHasToken guards an explicit member whose absence is silent. NULL in these columns
// means «not said»; a spelled-out member means «said, and the answer is this» — so a vocabulary that
// loses its explicit 'none' does not start refusing anything, it just stops being able to express the
// difference (the press-cloth argument above, applied where the wave repeats it).
func assertVocabularyHasToken(t *testing.T, label string, tokens []string, want, why string) {
	t.Helper()
	if slices.Contains(tokens, want) {
		return
	}
	t.Errorf("%s потерял %q: %s", label, want, why)
}

// assertVocabularyLacksToken is the mirror of the helper above, and it exists for the one case the
// «has» form cannot cover: a token DELIBERATELY REMOVED. Дрейф-тест сам по себе такое не удержит —
// он сверяет три списка МЕЖДУ СОБОЙ, и возврат `width` во все три сразу остался бы зелёным, хотя
// вернул бы ровно тот выбор, который владелец распорядился убрать: два написания одного приёма.
func assertVocabularyLacksToken(t *testing.T, label string, tokens []string, unwanted, why string) {
	t.Helper()
	if !slices.Contains(tokens, unwanted) {
		return
	}
	t.Errorf("%s вернул снятый токен %q: %s", label, unwanted, why)
}

// TestSeamSecuringDBCheckNoDrift — S3, чем закреплён конец строчки. 'none' is a real answer here
// («без закрепки»), not the absence of one.
func TestSeamSecuringDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardSeamSecuring",
		protoNames: pb_common.TechCardSeamSecuring_name,
		prefix:     "TECH_CARD_SEAM_SECURING_",
		zeroMember: "TECH_CARD_SEAM_SECURING_UNKNOWN",
		check:      "chk_op_seam_securing CHECK",
		tokens:     entity.SeamSecuringTokens,
	})
	assertVocabularyHasToken(t, "TechCardSeamSecuring", entity.SeamSecuringTokens, "none",
		"NULL уже значит «не сказано», и без явного члена «без закрепки» сказать нечем")
}

// TestTopstitchModeDBCheckNoDrift — the one pre-existing vocabulary of this family that had no drift
// guard at all until now: 0289 created chk_op_topstitch_mode, nothing ever compared it to Go.
//
// The 0324 wave added in_ditch and parallel_to_seam, and the second one is why the migration MODIFYs
// the column to VARCHAR(16) first: the token is 16 characters and the column was VARCHAR(8), so
// without the widening the first save is a data-too-long — or, with STRICT off, a silent truncation
// that the CHECK then refuses. That makes the column width part of this vocabulary's contract, so it
// is asserted here rather than left to the reader of the migration.
//
// 0326 СНЯЛ `width` — ЕДИНСТВЕННОЕ СУЖЕНИЕ СЛОВАРЯ ВО ВСЁМ СЕМЕЙСТВЕ, и владение списком перешло к
// нему; якорь CHECK'а поэтому читается из 0326, а не из 0324. Ширина колонки проверяется по ТОМУ ЖЕ
// файлу: 0326 переписывает COMMENT колонки (он называл `width` отдельным приёмом) и обязан назвать
// тип целиком, так что VARCHAR(16) в нём стоит — и это ровно то место, где сужение словаря могло бы
// незаметно уехать вместе с сужением колонки.
//
// Номер 2 объявлен retired, а не holes: proto его RESERVED, то есть закрыл навсегда. Отданный
// новому смыслу, он читался бы старым клиентом как прежний член — молча и без ошибки на проводе.
func TestTopstitchModeDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardTopstitchMode",
		protoNames: pb_common.TechCardTopstitchMode_name,
		prefix:     "TECH_CARD_TOPSTITCH_MODE_",
		zeroMember: "TECH_CARD_TOPSTITCH_MODE_UNKNOWN",
		check:      "chk_op_topstitch_mode CHECK",
		migration:  migration0326,
		tokens:     entity.TopstitchModeTokens,
		retired:    []int32{2}, // TECH_CARD_TOPSTITCH_MODE_WIDTH, снят 0326
	})
	assertVocabularyLacksToken(t, "TechCardTopstitchMode", entity.TopstitchModeTokens, "width",
		"`width` и `edge` описывали ОДИН приём — строчку от края детали — и различались лишь тем, названо ли число; число стало опциональным свойством `edge`")
	longest := 0
	for _, tok := range entity.TopstitchModeTokens {
		if len(tok) > longest {
			longest = len(tok)
		}
	}
	content := readMigrationFile(t, migration0326)
	widen := strings.Index(content, "MODIFY COLUMN topstitch_mode VARCHAR(")
	if widen < 0 {
		t.Fatalf("0326 must restate the topstitch_mode width alongside its CHECK: the longest token is %d characters and 0289 declared VARCHAR(8)", longest)
	}
	var width int
	if _, err := fmt.Sscanf(content[widen:], "MODIFY COLUMN topstitch_mode VARCHAR(%d)", &width); err != nil {
		t.Fatalf("cannot read the topstitch_mode width: %v", err)
	}
	if width < longest {
		t.Errorf("topstitch_mode is VARCHAR(%d) but the vocabulary needs %d characters — the longest token would be truncated on write and then refused by its own CHECK", width, longest)
	}
	// The MODIFY has to come BEFORE the CHECK is recreated: the other order writes a constraint
	// against a column that cannot hold the value it admits.
	if rx := strings.Index(content, "chk_op_topstitch_mode CHECK"); rx >= 0 && rx < widen {
		t.Error("0326 recreates chk_op_topstitch_mode before the MODIFY COLUMN; the MODIFY must come first")
	}
}

// TestHardwareAttachMethodDBCheckNoDrift — H1, как фурнитура держится на изделии. REQUIRED
// discriminator of the hardware_set verb: without it the step is not described at all.
func TestHardwareAttachMethodDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardHardwareAttachMethod",
		protoNames: pb_common.TechCardHardwareAttachMethod_name,
		prefix:     "TECH_CARD_HARDWARE_ATTACH_METHOD_",
		zeroMember: "TECH_CARD_HARDWARE_ATTACH_METHOD_UNKNOWN",
		check:      "chk_op_attach_method CHECK",
		// СПИСОК ЖИВЁТ В 0325: волна «прочего» пересоздала этот CHECK, дописав `other`.
		migration: migration0325,
		tokens:    entity.HardwareAttachMethodTokens,
	})
	assertVocabularyHasToken(t, "TechCardHardwareAttachMethod", entity.HardwareAttachMethodTokens, "other",
		"REQUIRED-дискриминатор без выхода «прочее» не оставляет поле пустым, а заставляет выбрать ЧУЖОЙ приём — и это уходит в подписанный хвост дайджеста, в снапшот и на печатный лист")
}

// TestHolePrepDBCheckNoDrift — H2, чем готовится отверстие под фурнитуру.
func TestHolePrepDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardHolePrep",
		protoNames: pb_common.TechCardHolePrep_name,
		prefix:     "TECH_CARD_HOLE_PREP_",
		zeroMember: "TECH_CARD_HOLE_PREP_UNKNOWN",
		check:      "chk_op_hole_prep CHECK",
		tokens:     entity.HolePrepTokens,
	})
	assertVocabularyHasToken(t, "TechCardHolePrep", entity.HolePrepTokens, "none",
		"«отверстие не готовится» — это ответ, а не молчание")
}

// TestReinforcementDBCheckNoDrift — H3, чем усилено место установки.
func TestReinforcementDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardReinforcement",
		protoNames: pb_common.TechCardReinforcement_name,
		prefix:     "TECH_CARD_REINFORCEMENT_",
		zeroMember: "TECH_CARD_REINFORCEMENT_UNKNOWN",
		check:      "chk_op_reinforcement CHECK",
		tokens:     entity.ReinforcementTokens,
	})
	assertVocabularyHasToken(t, "TechCardReinforcement", entity.ReinforcementTokens, "none",
		"«не усилено» — решение технолога, и оно обязано отличаться от «не спросили»")
}

// TestPrintMethodDBCheckNoDrift — P1, метод печати или переноса. Discriminator of the print verb.
func TestPrintMethodDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardPrintMethod",
		protoNames: pb_common.TechCardPrintMethod_name,
		prefix:     "TECH_CARD_PRINT_METHOD_",
		zeroMember: "TECH_CARD_PRINT_METHOD_UNKNOWN",
		check:      "chk_op_print_method CHECK",
		// СПИСОК ЖИВЁТ В 0325: волна «прочего» пересоздала этот CHECK, дописав `other`.
		migration: migration0325,
		tokens:    entity.PrintMethodTokens,
	})
	assertVocabularyHasToken(t, "TechCardPrintMethod", entity.PrintMethodTokens, "other",
		"REQUIRED-дискриминатор без выхода «прочее» не оставляет поле пустым, а заставляет выбрать ЧУЖОЙ приём — и это уходит в подписанный хвост дайджеста, в снапшот и на печатный лист")
	// entity.PrintMethodLaserEngrave is the one member the Go validator singles out by name (it has
	// no carrier, no peel, no second press and no pressure scale). A rename on either side would
	// leave that whole branch of not_applicable rules matching nothing, in silence.
	assertVocabularyHasToken(t, "TechCardPrintMethod", entity.PrintMethodTokens,
		string(entity.PrintMethodLaserEngrave),
		"на нём висят правила not_applicable носителя и прижима — без него они перестают срабатывать молча")
}

// TestPeelModeDBCheckNoDrift — P2, как снимается носитель. 'none' = носителя нет вовсе.
func TestPeelModeDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardPeelMode",
		protoNames: pb_common.TechCardPeelMode_name,
		prefix:     "TECH_CARD_PEEL_MODE_",
		zeroMember: "TECH_CARD_PEEL_MODE_UNKNOWN",
		check:      "chk_op_peel_mode CHECK",
		tokens:     entity.PeelModeTokens,
	})
	assertVocabularyHasToken(t, "TechCardPeelMode", entity.PeelModeTokens, "none",
		"«носителя нет» — свойство метода печати, а не отсутствие ответа")
}

// TestPressureScaleDBCheckNoDrift — прижим термопресса ШКАЛОЙ, а не числом: raw force means nothing
// between two different presses.
func TestPressureScaleDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardPressureScale",
		protoNames: pb_common.TechCardPressureScale_name,
		prefix:     "TECH_CARD_PRESSURE_SCALE_",
		zeroMember: "TECH_CARD_PRESSURE_SCALE_UNKNOWN",
		check:      "chk_op_pressure_scale CHECK",
		tokens:     entity.PressureScaleTokens,
	})
	// Same argument as TestAutomationLevelDBCheckNoDrift: an ordered scale that grows an «other» has
	// stopped being one, and nothing else in the system would notice.
	if slices.Contains(entity.PressureScaleTokens, "other") {
		t.Error("pressure_scale is an ordered scale (light < medium < firm) and must not carry 'other'")
	}
}

// TestTrimActionDBCheckNoDrift — T1, что именно делает подрезка. Discriminator of the trim verb.
func TestTrimActionDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardTrimAction",
		protoNames: pb_common.TechCardTrimAction_name,
		prefix:     "TECH_CARD_TRIM_ACTION_",
		zeroMember: "TECH_CARD_TRIM_ACTION_UNKNOWN",
		check:      "chk_op_trim_action CHECK",
		// СПИСОК ЖИВЁТ В 0325: волна «прочего» пересоздала этот CHECK, дописав `other`.
		migration: migration0325,
		tokens:    entity.TrimActionTokens,
	})
	assertVocabularyHasToken(t, "TechCardTrimAction", entity.TrimActionTokens, "other",
		"REQUIRED-дискриминатор без выхода «прочее» не оставляет поле пустым, а заставляет выбрать ЧУЖОЙ приём — и это уходит в подписанный хвост дайджеста, в снапшот и на печатный лист")
}

// TestCleaningKindDBCheckNoDrift — C1, что именно чистят. Discriminator of the clean verb.
func TestCleaningKindDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardCleaningKind",
		protoNames: pb_common.TechCardCleaningKind_name,
		prefix:     "TECH_CARD_CLEANING_KIND_",
		zeroMember: "TECH_CARD_CLEANING_KIND_UNKNOWN",
		check:      "chk_op_cleaning_kind CHECK",
		// СПИСОК ЖИВЁТ В 0325: волна «прочего» пересоздала этот CHECK, дописав `other`.
		migration: migration0325,
		tokens:    entity.CleaningKindTokens,
	})
	assertVocabularyHasToken(t, "TechCardCleaningKind", entity.CleaningKindTokens, "other",
		"REQUIRED-дискриминатор без выхода «прочее» не оставляет поле пустым, а заставляет выбрать ЧУЖОЙ приём — и это уходит в подписанный хвост дайджеста, в снапшот и на печатный лист")
}

// TestInspectCoverageDBCheckNoDrift — Q1, что именно проверяют. The column is coverage_mode, the
// vocabulary is InspectCoverageTokens: the two names differ, which is exactly why the anchor is
// spelled out rather than derived from the label.
func TestInspectCoverageDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardInspectCoverage",
		protoNames: pb_common.TechCardInspectCoverage_name,
		prefix:     "TECH_CARD_INSPECT_COVERAGE_",
		zeroMember: "TECH_CARD_INSPECT_COVERAGE_UNKNOWN",
		check:      "chk_op_coverage_mode CHECK",
		// СПИСОК ЖИВЁТ В 0325: волна «прочего» пересоздала этот CHECK, дописав `other`.
		migration: migration0325,
		tokens:    entity.InspectCoverageTokens,
	})
	assertVocabularyHasToken(t, "TechCardInspectCoverage", entity.InspectCoverageTokens, "other",
		"REQUIRED-дискриминатор без выхода «прочее» не оставляет поле пустым, а заставляет выбрать ЧУЖОЙ приём — и это уходит в подписанный хвост дайджеста, в снапшот и на печатный лист")
}

// TestWetProcessKindDBCheckNoDrift — WP1, вид мокрой обработки. Discriminator of the wet_process verb.
func TestWetProcessKindDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardWetProcessKind",
		protoNames: pb_common.TechCardWetProcessKind_name,
		prefix:     "TECH_CARD_WET_PROCESS_KIND_",
		zeroMember: "TECH_CARD_WET_PROCESS_KIND_UNKNOWN",
		check:      "chk_op_wet_process_kind CHECK",
		// СПИСОК ЖИВЁТ В 0325: волна «прочего» пересоздала этот CHECK, дописав `other`.
		migration: migration0325,
		tokens:    entity.WetProcessKindTokens,
	})
	assertVocabularyHasToken(t, "TechCardWetProcessKind", entity.WetProcessKindTokens, "other",
		"REQUIRED-дискриминатор без выхода «прочее» не оставляет поле пустым, а заставляет выбрать ЧУЖОЙ приём — и это уходит в подписанный хвост дайджеста, в снапшот и на печатный лист")
}

// TestButtonholeStyleDBCheckNoDrift — FA1, форма петли.
func TestButtonholeStyleDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardButtonholeStyle",
		protoNames: pb_common.TechCardButtonholeStyle_name,
		prefix:     "TECH_CARD_BUTTONHOLE_STYLE_",
		zeroMember: "TECH_CARD_BUTTONHOLE_STYLE_UNKNOWN",
		check:      "chk_op_buttonhole_style CHECK",
		tokens:     entity.ButtonholeStyleTokens,
	})
}

// TestButtonholeOrientationDBCheckNoDrift — FA5, как петля лежит. Not the position: WHERE the
// buttonhole sits is a callout on the step's media, never a member of this vocabulary.
func TestButtonholeOrientationDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardButtonholeOrientation",
		protoNames: pb_common.TechCardButtonholeOrientation_name,
		prefix:     "TECH_CARD_BUTTONHOLE_ORIENTATION_",
		zeroMember: "TECH_CARD_BUTTONHOLE_ORIENTATION_UNKNOWN",
		check:      "chk_op_buttonhole_orientation CHECK",
		tokens:     entity.ButtonholeOrientationTokens,
	})
}

// TestButtonAttachPatternDBCheckNoDrift — FA9, рисунок пришива пуговицы. The anchor is the full
// «chk_op_attach_pattern CHECK»: chk_op_attach_method shares its first eighteen characters, and a
// prefix anchor would read the wrong list without failing.
func TestButtonAttachPatternDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardButtonAttachPattern",
		protoNames: pb_common.TechCardButtonAttachPattern_name,
		prefix:     "TECH_CARD_BUTTON_ATTACH_PATTERN_",
		zeroMember: "TECH_CARD_BUTTON_ATTACH_PATTERN_UNKNOWN",
		check:      "chk_op_attach_pattern CHECK",
		tokens:     entity.ButtonAttachPatternTokens,
	})
}

// TestZipperApplicationDBCheckNoDrift — FA13, способ установки молнии.
func TestZipperApplicationDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardZipperApplication",
		protoNames: pb_common.TechCardZipperApplication_name,
		prefix:     "TECH_CARD_ZIPPER_APPLICATION_",
		zeroMember: "TECH_CARD_ZIPPER_APPLICATION_UNKNOWN",
		check:      "chk_op_zipper_application CHECK",
		tokens:     entity.ZipperApplicationTokens,
	})
}

// TestBindingStyleDBCheckNoDrift — S14, как сложена бейка.
func TestBindingStyleDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardBindingStyle",
		protoNames: pb_common.TechCardBindingStyle_name,
		prefix:     "TECH_CARD_BINDING_STYLE_",
		zeroMember: "TECH_CARD_BINDING_STYLE_UNKNOWN",
		check:      "chk_op_binding_style CHECK",
		tokens:     entity.BindingStyleTokens,
	})
}

// TestLabelAttachStitchDBCheckNoDrift — S17, какими сторонами пристрочена этикетка. The label itself
// arrives as a BOM line; this vocabulary is only the stitching that seats it.
func TestLabelAttachStitchDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardLabelAttachStitch",
		protoNames: pb_common.TechCardLabelAttachStitch_name,
		prefix:     "TECH_CARD_LABEL_ATTACH_STITCH_",
		zeroMember: "TECH_CARD_LABEL_ATTACH_STITCH_UNKNOWN",
		check:      "chk_op_label_attach_stitch CHECK",
		tokens:     entity.LabelAttachStitchTokens,
	})
}

// --- 0325: под-глагол ВТО и направление припуска ---------------------------------------------------

// TestPressActionDBCheckNoDrift — ЧТО ИМЕННО делает ВТО-шаг. НЕ required ни на одном глаголе, в
// отличие от шести дискриминаторов выше: старая строка press без под-глагола обязана сохраняться как
// есть, и обязательность здесь была бы ретроактивной.
func TestPressActionDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardPressAction",
		protoNames: pb_common.TechCardPressAction_name,
		prefix:     "TECH_CARD_PRESS_ACTION_",
		zeroMember: "TECH_CARD_PRESS_ACTION_UNKNOWN",
		check:      "chk_op_press_action CHECK",
		migration:  migration0325,
		tokens:     entity.PressActionTokens,
	})
	// На обоих членах висят правила Go, и оба находятся ПО ИМЕНИ: to_one_side включает
	// обязательность направления, open — единственное, что законно на глаголе press_open.
	// Переименование любого из двух оставило бы правило матчащим ничто, и оно перестало бы
	// существовать молча.
	assertVocabularyHasToken(t, "TechCardPressAction", entity.PressActionTokens,
		string(entity.PressActionToOneSide),
		"на нём висит условная обязательность press_toward — без него правило перестаёт срабатывать молча")
	assertVocabularyHasToken(t, "TechCardPressAction", entity.PressActionTokens,
		string(entity.PressActionOpen),
		"второе написание разутюжки: чтение принимает и его, и глагол press_open")
}

// TestPressTowardDBCheckNoDrift — КУДА лёг припуск. Собственный словарь, а НЕ TechCardGarmentZone:
// «вверх», «вниз» и «к центру» зонами не являются, а второе поле зонного типа на шаге, у которого
// уже есть zone, — ровно ловушка «два ключа под одним именем».
func TestPressTowardDBCheckNoDrift(t *testing.T) {
	assertWaveVocabularyNoDrift(t, waveVocabulary{
		label:      "TechCardPressToward",
		protoNames: pb_common.TechCardPressToward_name,
		prefix:     "TECH_CARD_PRESS_TOWARD_",
		zeroMember: "TECH_CARD_PRESS_TOWARD_UNKNOWN",
		check:      "chk_op_press_toward CHECK",
		migration:  migration0325,
		tokens:     entity.PressTowardTokens,
	})
	// Ни одного `none`: у направления явного «нет» не бывает — припуск либо заутюжен на сторону, и
	// сторона названа, либо не заутюжен вовсе, и тогда поля нет. Проверяется от противного: член,
	// заведённый по инерции с seam_securing / hole_prep / peel_mode, сделал бы выразимым состояние
	// «заутюжено в никуда».
	for _, tok := range entity.PressTowardTokens {
		if tok == "none" {
			t.Error("TechCardPressToward: член \"none\" не должен существовать — «не заутюжено» выражается отсутствием press_action = to_one_side, а не направлением «никуда»")
		}
	}
}

// TestPressColumnsAreNullableWithoutDefault пришпиливает форму хранения обеих колонок 0325.
//
// NULL значит «не сказано». DEFAULT стёр бы разницу между «технолог ответил» и «технолог молчит», а
// NOT NULL сделал бы поле обязательным ретроактивно — на КАЖДОЙ существующей строке, ровно то, чего
// эта фаза обязана избежать. Обе колонки читаются из файла, а не из живой схемы: тест статический.
func TestPressColumnsAreNullableWithoutDefault(t *testing.T) {
	content := readMigrationFile(t, migration0325)
	for _, col := range []struct{ name, typ string }{
		{"press_action", "VARCHAR(16)"},
		{"press_toward", "VARCHAR(20)"},
	} {
		want := "ADD COLUMN " + col.name + " " + col.typ + " NULL"
		if !strings.Contains(content, want) {
			t.Errorf("0325 must add %s as %s NULL (found no %q) — NOT NULL would make the field required on every existing row", col.name, col.typ, want)
			continue
		}
		idx := strings.Index(content, want)
		end := strings.Index(content[idx:], "\n")
		if end < 0 {
			end = len(content) - idx
		}
		if strings.Contains(strings.ToUpper(content[idx:idx+end]), "DEFAULT") {
			t.Errorf("%s carries a DEFAULT — it would erase the difference between «technologist answered» and «technologist is silent»", col.name)
		}
	}
	// И ширины: самый длинный токен обязан помещаться, иначе первая же запись — data-too-long, а со
	// STRICT off тихое усечение, которое потом отвергнет собственный CHECK колонки (урок
	// topstitch_mode в 0324).
	for _, c := range []struct {
		col    string
		width  int
		tokens []string
	}{
		{"press_action", 16, entity.PressActionTokens},
		{"press_toward", 20, entity.PressTowardTokens},
	} {
		for _, tok := range c.tokens {
			if len(tok) > c.width {
				t.Errorf("%s is VARCHAR(%d) but token %q is %d characters — it would be truncated on write and then refused by its own CHECK", c.col, c.width, tok, len(tok))
			}
		}
	}
}

// Test0325VocabulariesAreCaseClosed — тот же довод, что у 0306 и 0324: REGEXP наследует коллацию
// колонки, а она регистронезависима и на utf8mb3 прода, и на utf8mb4 контейнера, поэтому одна
// альтернация принимает 'FRONT' и 'To_One_Side'. Закрывает словарь STRCMP по BINARY-касту.
//
// Шесть ПЕРЕСОЗДАННЫХ CHECK'ов в списке по той же причине, по которой семь пересозданных стоят в
// тесте 0324: пересоздание — ровно то место, где гейт регистра теряют по невнимательности, и потеря
// невидима: список токенов по-прежнему совпадает, и все тесты дрейфа остаются зелёными.
func Test0325VocabulariesAreCaseClosed(t *testing.T) {
	content := readMigrationFile(t, migration0325)
	for _, c := range []struct{ constraint, column string }{
		// новые словари
		{"chk_op_press_action", "press_action"},
		{"chk_op_press_toward", "press_toward"},
		// пересозданные ради выхода «прочее»
		{"chk_op_attach_method", "attach_method"},
		{"chk_op_print_method", "print_method"},
		{"chk_op_trim_action", "trim_action"},
		{"chk_op_cleaning_kind", "cleaning_kind"},
		{"chk_op_coverage_mode", "coverage_mode"},
		{"chk_op_wet_process_kind", "wet_process_kind"},
	} {
		stmt := strings.Index(content, c.constraint+" CHECK")
		if stmt < 0 {
			t.Errorf("named vocabulary CHECK %s not found in 0325", c.constraint)
			continue
		}
		guard := "STRCMP(CAST(" + c.column + " AS BINARY), CAST(LOWER(" + c.column + ") AS BINARY)) = 0"
		rx := strings.Index(content[stmt:], c.column+" REGEXP")
		gd := strings.Index(content[stmt:], guard)
		if rx < 0 {
			t.Errorf("%s: no REGEXP alternation on %s", c.constraint, c.column)
			continue
		}
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

// TestPressRecreatedChecksAreSupersets доказывает, что шаг 2 миграции 0325 РАСШИРЯЕТ шесть словарей,
// а не переписывает их. Сужение здесь — не стилистика: ADD CONSTRAINT проверяет ВСЮ историю таблицы
// и останавливает старт прода на первой же строке со снятым токеном (память
// retroactive-check-halts-deploy), а порядок и написание оставшихся членов входят в отпечаток.
func TestPressRecreatedChecksAreSupersets(t *testing.T) {
	before := readMigrationFile(t, migration0324)
	after := readMigrationFile(t, migration0325)
	for _, check := range []string{
		"chk_op_attach_method CHECK", "chk_op_print_method CHECK", "chk_op_trim_action CHECK",
		"chk_op_cleaning_kind CHECK", "chk_op_coverage_mode CHECK", "chk_op_wet_process_kind CHECK",
	} {
		old := extractDBEnumValues(t, before, check, waveCheckWindow)
		now := extractDBEnumValues(t, after, check, waveCheckWindow)
		if len(now) != len(old)+1 {
			t.Errorf("%s: 0324 listed %d tokens, 0325 lists %d — the recreation must ADD exactly «other» and touch nothing else", check, len(old), len(now))
			continue
		}
		for i, tok := range old {
			if now[i] != tok {
				t.Errorf("%s: token %d moved from %q to %q — order and spelling of the existing members are part of the fingerprint, only the append is allowed", check, i, tok, now[i])
			}
		}
		if now[len(now)-1] != "other" {
			t.Errorf("%s: the appended token is %q, not \"other\"", check, now[len(now)-1])
		}
	}
}

// Test0324VocabulariesAreCaseClosed guards the half of every vocabulary CHECK in the wave that the
// drift tests cannot see, on the same argument as Test0306VocabulariesAreCaseClosed: REGEXP inherits
// the column's collation, which is case-INSENSITIVE on both the utf8mb3 of prod and the utf8mb4 of
// the container, so an alternation on its own accepts 'BACKTACK' and 'Firm'. It refuses 'backtak' and
// nothing about case; the STRCMP over a BINARY cast is what actually closes the vocabulary.
//
// The seven recreated CHECKs are in the list too: a recreation is where the guard gets dropped by
// accident, and a dropped guard is invisible — the token list still matches, so every drift test
// above stays green.
func Test0324VocabulariesAreCaseClosed(t *testing.T) {
	content := readMigrationFile(t, migration0324)
	for _, c := range []struct{ constraint, column string }{
		// новые словари волны
		{"chk_op_seam_securing", "seam_securing"},
		{"chk_op_attach_method", "attach_method"},
		{"chk_op_hole_prep", "hole_prep"},
		{"chk_op_reinforcement", "reinforcement"},
		{"chk_op_print_method", "print_method"},
		{"chk_op_peel_mode", "peel_mode"},
		{"chk_op_pressure_scale", "pressure_scale"},
		{"chk_op_trim_action", "trim_action"},
		{"chk_op_cleaning_kind", "cleaning_kind"},
		{"chk_op_coverage_mode", "coverage_mode"},
		{"chk_op_wet_process_kind", "wet_process_kind"},
		{"chk_op_buttonhole_style", "buttonhole_style"},
		{"chk_op_buttonhole_orientation", "buttonhole_orientation"},
		{"chk_op_attach_pattern", "attach_pattern"},
		{"chk_op_zipper_application", "zipper_application"},
		{"chk_op_binding_style", "binding_style"},
		{"chk_op_label_attach_stitch", "label_attach_stitch"},
		// пересозданные волной
		{"chk_op_operation_type", "operation_type"},
		{"chk_op_machine_type", "machine_type"},
		{"chk_op_press_cloth", "press_cloth"},
		{"chk_op_topstitch_mode", "topstitch_mode"},
		{"chk_eqp_equipment", "equipment"},
		{"chk_eqp_press_cloth", "press_cloth"},
		{"chk_bom_item_kind", "kind"},
	} {
		stmt := strings.Index(content, c.constraint+" CHECK")
		if stmt < 0 {
			t.Errorf("named vocabulary CHECK %s not found in 0324", c.constraint)
			continue
		}
		guard := "STRCMP(CAST(" + c.column + " AS BINARY), CAST(LOWER(" + c.column + ") AS BINARY)) = 0"
		rx := strings.Index(content[stmt:], c.column+" REGEXP")
		gd := strings.Index(content[stmt:], guard)
		if rx < 0 {
			t.Errorf("%s: no REGEXP alternation on %s", c.constraint, c.column)
			continue
		}
		// The guard has to sit INSIDE this CHECK (before the next constraint starts) and AFTER the
		// REGEXP, so extractDBEnumValues' anchor still finds the alternation.
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

// TestOperationContractHolesStayOpen is the numbering-density guard for the message the wave grew by
// thirty-two fields: TechCardOperation. Field numbers are the one part of the contract that is
// promised and can never be renegotiated, so this test asserts the promise from both sides.
//
// Three numbers are DELIBERATELY empty — 50 (`tooling_key`, the tooling phase), 62 (`properties`, the
// extensible-properties phase) and 64 (the `handwork` block) — and none of the three is `reserved`,
// on purpose: reserved would close the number forever, and each is promised to a phase that will
// claim it. A density check that did not know about the three would go red on a healthy contract, and
// the natural "fix" for that red would be to delete the check. So the holes are declared here, next
// to the check, with the reason.
//
// The other direction matters just as much: without a density check, a member typed at 65 instead of
// the next free number reads as an ordinary append and silently opens a hole nobody promised — and a
// hole nobody promised is the number the NEXT phase will take, on a wire where an old client still
// remembers what used to be there.
func TestOperationContractHolesStayOpen(t *testing.T) {
	promised := map[int32]string{
		50: "`tooling_key` фазы оснастки",
		62: "`properties` фазы расширяемых свойств",
		64: "блок `TechCardOperationHandwork handwork`",
	}
	md := (&pb_common.TechCardOperation{}).ProtoReflect().Descriptor()

	used := make(map[int32]string)
	highest := int32(0)
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		n := int32(f.Number())
		used[n] = string(f.Name())
		if n > highest {
			highest = n
		}
	}
	// Retired numbers: the legacy fields 0289 broke apart. They are `reserved` precisely because they
	// must never come back, which is the opposite of what a promised hole is.
	retired := make(map[int32]bool)
	rr := md.ReservedRanges()
	for i := 0; i < rr.Len(); i++ {
		r := rr.Get(i)
		for n := int32(r[0]); n < int32(r[1]); n++ {
			retired[n] = true
		}
	}

	for n, why := range promised {
		if name, taken := used[n]; taken {
			t.Errorf("номер %d обещан (%s), но занят полем %s — обещание номера нельзя переиграть", n, why, name)
		}
		if retired[n] {
			t.Errorf("номер %d обещан (%s), но объявлен reserved — reserved закрывает номер навсегда, а он ждёт своей фазы", n, why)
		}
		if n > highest {
			highest = n
		}
	}

	for n := int32(1); n <= highest; n++ {
		if _, taken := used[n]; taken || retired[n] {
			continue
		}
		if _, ok := promised[n]; ok {
			continue
		}
		t.Errorf("TechCardOperation: номер %d не занят, не reserved и не объявлен обещанной дырой — либо это опечатка в номере поля, либо пропуск, который надо внести в promised вместе с причиной", n)
	}
}
