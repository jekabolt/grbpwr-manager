package entity

import "testing"

// ЛИЧНОСТЬ ТКАНИ ПРОТИВ ВЕДРА УНИКАЛЬНОСТИ — расхождение, на котором умер замер площадей.
//
// Карточка 38 на бете: девять связей блок→деталь, все с scope_key = ключ строки BOM, и та же строка
// с назначением 'main'. Клиент и деньги спрашивали про 'main', доказательство полноты сверяло с
// ведром — и отвечало «блоки не привязаны» на карточке, где привязано всё. Тесты ниже фиксируют
// обе половины: ведро НЕ ДВИГАЕТСЯ (иначе поедет уникальный индекс), личность ДВИГАЕТСЯ вместе с BOM.

const (
	fsLineMain    = "01KZBYMSGP95NPJAHBC5CCYWSR"
	fsLineLining  = "01KZBYMSGP95NPJAHBC5CCYWSS"
	fsLineNoPurp  = "01KZBYMSGP95NPJAHBC5CCYWST"
	fsLineDeleted = "01KZBYMSGP95NPJAHBC5CCYWSZ"
)

func fsSortedCard() []RollGoodsLine {
	return []RollGoodsLine{
		{LineKey: fsLineMain, Purpose: "main", Section: BomSectionFabric},
		{LineKey: fsLineLining, Purpose: "lining", Section: BomSectionLining},
		{LineKey: fsLineNoPurp, Section: BomSectionFabric},
	}
}

func TestFabricScopeIdentityFollowsALineIntoItsPurpose(t *testing.T) {
	lines := fsSortedCard()
	tests := []struct {
		name          string
		purpose, line string
		wantIdentity  string
		wantBucket    string
	}{
		{
			// КАРТОЧКА 38. The link was written before anybody sorted the BOM, so it names the LINE —
			// and it goes on naming it forever (the read hands both halves back and the client
			// returns them unchanged). Its bucket must not move; its fabric must.
			name: "a link written before the sort names the line, and its fabric is now the purpose",
			line: fsLineMain, wantIdentity: "main", wantBucket: fsLineMain,
		},
		{
			name:    "a link written after the sort names the purpose and agrees with the one above",
			purpose: "main", line: fsLineMain, wantIdentity: "main", wantBucket: "main",
		},
		{
			// Сегодняшняя популяция: карточку никто не раскладывал. Личность и ведро совпадают, то
			// есть починка ничего не двигает у неразобранных карточек.
			name: "an unsorted line resolves to itself, exactly as before",
			line: fsLineNoPurp, wantIdentity: fsLineNoPurp, wantBucket: fsLineNoPurp,
		},
		{
			// «Слот удалён» — состояние интерфейса; переименовывать ведро не за что.
			name: "a line that no longer exists keeps the key the binding named",
			line: fsLineDeleted, wantIdentity: fsLineDeleted, wantBucket: fsLineDeleted,
		},
		{
			// Повисшее назначение остаётся собой: пустой ключ склеил бы все повисшие записи карточки
			// в одно ведро. Читателю это не видно — у такого назначения нет строки BOM.
			name:    "a purpose no line carries stays itself rather than collapsing to the empty bucket",
			purpose: "mesh", wantIdentity: "mesh", wantBucket: "mesh",
		},
		{
			// Ключи строк заглавные на входе, но у старой записи мог остаться другой регистр. Личность
			// берётся СО СТРОКИ — иначе два ведра под одну ткань, и разрыв повторяется тише.
			name: "a line key stored in another case resolves to the BOM's spelling",
			line: "01kzbymsgp95npjahbc5ccywst", wantIdentity: fsLineNoPurp, wantBucket: "01kzbymsgp95npjahbc5ccywst",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FabricScopeIdentity(tt.purpose, tt.line, lines); got != tt.wantIdentity {
				t.Fatalf("FabricScopeIdentity(%q, %q) = %q, want %q", tt.purpose, tt.line, got, tt.wantIdentity)
			}
			if got := FabricScopeKey(tt.purpose, tt.line); got != tt.wantBucket {
				t.Fatalf("FabricScopeKey(%q, %q) = %q, want %q — the uniqueness bucket may not move with the BOM",
					tt.purpose, tt.line, got, tt.wantBucket)
			}
		})
	}
}

// TestFabricScopeIdentityLeavesAnUnboundBindingUnbound: a sheet that names NOTHING (uploaded before
// 0260) must not be captured by a cloth line whose own line_key is empty — uniq_bom_line_key still
// permits several NULLs, and equality of two empty strings would file an unbound sheet under a
// назначение, letting it answer for cloth nobody bound it to.
func TestFabricScopeIdentityLeavesAnUnboundBindingUnbound(t *testing.T) {
	lines := append(fsSortedCard(), RollGoodsLine{LineKey: "", Purpose: "contrast"})
	if got := FabricScopeIdentity("", "", lines); got != "" {
		t.Fatalf("FabricScopeIdentity(\"\", \"\") = %q, want \"\" — an unbound binding names no cloth", got)
	}
	if got := FabricScopeIdentity("", "   ", lines); got != "" {
		t.Fatalf("FabricScopeIdentity(\"\", \"   \") = %q, want \"\"", got)
	}
}

// TestFabricScopeIdentityMakesTheCompletenessProofCompareLikeWithLike is the property the areas save
// actually needs: a SHEET and a BLOCK LINK of the same cloth land under ONE key even when the two
// were written on different sides of the sort. Comparing raw buckets is what refused card 38.
func TestFabricScopeIdentityMakesTheCompletenessProofCompareLikeWithLike(t *testing.T) {
	lines := fsSortedCard()
	// Card 38: the sheet was re-bound to the назначение, the block links were never touched again.
	sheet := FabricScopeIdentity("main", "", lines)
	link := FabricScopeIdentity("", fsLineMain, lines)
	if sheet != link {
		t.Fatalf("sheet scope %q != block-link scope %q — the completeness proof would look for links in an empty bucket", sheet, link)
	}
	if FabricScopeKey("main", "") == FabricScopeKey("", fsLineMain) {
		t.Fatal("the buckets are expected to DIFFER here; if they agree this test no longer reproduces the blocker")
	}
}

func TestResolveFabricScopeKeepsOrdinaryLegacyLineBucket(t *testing.T) {
	scope := ResolveFabricScope("", fsLineMain, fsSortedCard())
	if scope.Key != fsLineMain || scope.ByPurpose || len(scope.LineKeys) != 1 || scope.LineKeys[0] != fsLineMain {
		t.Fatalf("ordinary line-only binding resolved as %#v, want its raw line bucket", scope)
	}
	if got := FabricScopeIdentity("", fsLineMain, fsSortedCard()); got != "main" {
		t.Fatalf("ordinary line identity = %q, want purpose main", got)
	}
}

// TestFabricScopeIdentityIsTheKeyTheMoneyLooksAreasUpUnder pins the identity to the reader that
// spends it: dto.slotAreaEstimate keys tc.PieceAreaScopes by FabricScopeKey(строка.purpose,
// строка.line_key). Every binding of that line must resolve to exactly that string, or the areas are
// stored where the estimate never looks.
func TestFabricScopeIdentityIsTheKeyTheMoneyLooksAreasUpUnder(t *testing.T) {
	lines := fsSortedCard()
	for _, l := range lines {
		want := FabricScopeKey(l.Purpose, l.LineKey) // what dto.slotAreaEstimate computes for this slot
		if got := FabricScopeIdentity("", l.LineKey, lines); got != want {
			t.Fatalf("line %q: binding resolves to %q, the estimate looks under %q", l.LineKey, got, want)
		}
		if l.Purpose == "" {
			continue
		}
		if got := FabricScopeIdentity(l.Purpose, "", lines); got != want {
			t.Fatalf("purpose %q: binding resolves to %q, the estimate looks under %q", l.Purpose, got, want)
		}
	}
}

// TestFabricScopeIdentityMergesTwoLinesOfOnePurpose is the whole point of 0267 seen from the areas
// side: основная ткань в двух артикулах — это две строки одного назначения и ОДИН комплект лекал.
// Their bindings must not measure two separate garments.
func TestFabricScopeIdentityMergesTwoLinesOfOnePurpose(t *testing.T) {
	lines := []RollGoodsLine{
		{LineKey: fsLineMain, Purpose: "main", Section: BomSectionFabric},
		{LineKey: fsLineLining, Purpose: "main", Section: BomSectionLining},
	}
	a := FabricScopeIdentity("", fsLineMain, lines)
	b := FabricScopeIdentity("", fsLineLining, lines)
	if a != "main" || b != "main" {
		t.Fatalf("two lines of one назначение resolved to %q and %q, want both \"main\"", a, b)
	}
}

func TestInterliningLinesNeverFoldIntoTheirSharedPurpose(t *testing.T) {
	const fuseA, fuseB = "FUSE-A", "FUSE-B"
	lines := []RollGoodsLine{
		{LineKey: fuseA, Purpose: "interfacing", Section: BomSectionInterlining},
		{LineKey: fuseB, Purpose: "interfacing", Section: BomSectionInterlining},
	}
	for _, line := range lines {
		if got := EffectiveFabricScopeKey(line); got != line.LineKey {
			t.Errorf("effective scope of %s = %q, want its own line key", line.LineKey, got)
		}
		if got := FabricScopeIdentity(line.Purpose, line.LineKey, lines); got != line.LineKey {
			t.Errorf("identity of purpose+line binding %s = %q, want line scope", line.LineKey, got)
		}
	}
	if a, b := FabricScopeIdentity("", fuseA, lines), FabricScopeIdentity("", fuseB, lines); a == b {
		t.Fatalf("same-purpose interlining lines collapsed into one scope %q", a)
	}
	if got := ResolveFabricScope("interfacing", "", lines); got.Live() {
		t.Fatalf("ambiguous legacy purpose binding resolved to lines %v; N>1 must have no fallback", got.LineKeys)
	}
	if got := PurposesOfRollGoods(lines); len(got) != 0 {
		t.Fatalf("interlining purposes exposed as writable purpose scopes: %v", got)
	}
}

func TestInterliningLegacyPurposeFallbackIsSingletonOnly(t *testing.T) {
	line := RollGoodsLine{LineKey: "FUSE-A", Purpose: "interfacing", Section: BomSectionInterlining}
	if got := ResolveFabricScope("interfacing", "", []RollGoodsLine{line}); got.Key != "FUSE-A" || got.ByPurpose || len(got.LineKeys) != 1 || got.LineKeys[0] != "FUSE-A" {
		t.Fatalf("singleton purpose-only binding = %#v, want the interlining line identity", got)
	}
	if got := FabricScopeReadKeys(line, []RollGoodsLine{line}); len(got) != 2 ||
		got[0] != "FUSE-A" || got[1] != "interfacing" {
		t.Fatalf("singleton read keys = %v, want [FUSE-A interfacing]", got)
	}
	lines := []RollGoodsLine{
		line,
		{LineKey: "FUSE-B", Purpose: "interfacing", Section: BomSectionInterlining},
	}
	if got := FabricScopeReadKeys(line, lines); len(got) != 1 || got[0] != "FUSE-A" {
		t.Fatalf("ambiguous read keys = %v, want only the line scope", got)
	}
}

func TestNonInterliningSortedLineHasNoRawLineReadBucket(t *testing.T) {
	line := RollGoodsLine{LineKey: "RIB-A", Purpose: "other", Section: BomSectionFabric}
	if got := FabricScopeReadKeys(line, []RollGoodsLine{line}); len(got) != 1 || got[0] != "other" {
		t.Fatalf("ordinary sorted-line read keys = %v, want only [other]", got)
	}
	if got := FabricScopeIdentity("", line.LineKey, []RollGoodsLine{line}); got != "other" {
		t.Fatalf("line-only rib identity = %q, want purpose group other", got)
	}
}

func TestFabricScopeKeyLiveUsesEffectiveScopes(t *testing.T) {
	lines := []RollGoodsLine{
		{LineKey: "FABRIC-A", Purpose: "main", Section: BomSectionFabric},
		{LineKey: "FUSE-A", Purpose: "interfacing", Section: BomSectionInterlining},
	}
	for _, key := range []string{"main", "FUSE-A", "interfacing"} {
		if !FabricScopeKeyLive(key, lines) {
			t.Errorf("scope %q should be live", key)
		}
	}
	for _, key := range []string{"", "FABRIC-A", "lining", "GONE"} {
		if FabricScopeKeyLive(key, lines) {
			t.Errorf("scope %q should be dead", key)
		}
	}
}

func TestNormalizeFabricBomLineKeyClearsOnlyStalePurposeEcho(t *testing.T) {
	lines := []RollGoodsLine{
		{LineKey: "MAIN-A", Purpose: "main", Section: BomSectionFabric},
		{LineKey: "MAIN-B", Purpose: "main", Section: BomSectionLining},
		{LineKey: "LINING-A", Purpose: "lining", Section: BomSectionLining},
		{LineKey: "FUSE-A", Purpose: "interfacing", Section: BomSectionInterlining},
	}
	tests := []struct {
		name, purpose, line, want string
	}{
		{"same purpose", "main", "MAIN-A", "MAIN-A"},
		{"line moved to another purpose", "main", "LINING-A", ""},
		{"line deleted", "main", "GONE", ""},
		{"singleton interlining compatibility", "interfacing", "FUSE-A", "FUSE-A"},
		{"dead purpose remains repairable", "mesh", "GONE", "GONE"},
		{"line-only binding is unchanged", "", "MAIN-A", "MAIN-A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeFabricBomLineKey(tt.purpose, tt.line, lines); got != tt.want {
				t.Fatalf("NormalizeFabricBomLineKey(%q, %q) = %q, want %q", tt.purpose, tt.line, got, tt.want)
			}
		})
	}
}

func TestNormalizeFabricBomLineKeyKeepsALiveInterliningLine(t *testing.T) {
	// The felt used to share «other» with the rib; it has just been re-sorted to «interfacing». A
	// sheet stored as {other, FELT-A} still names the felt (the line wins), so the key must survive —
	// clearing it would rebind the sheet to the rib's «other» group, which is live, so nothing fails.
	lines := []RollGoodsLine{
		{LineKey: "RIB-A", Purpose: "other", Section: BomSectionFabric},
		{LineKey: "FELT-A", Purpose: "interfacing", Section: BomSectionInterlining},
	}
	if got := NormalizeFabricBomLineKey("other", "FELT-A", lines); got != "FELT-A" {
		t.Fatalf("interlining line key = %q, want it kept", got)
	}
	if got := FabricScopeIdentity("other", NormalizeFabricBomLineKey("other", "FELT-A", lines), lines); got != "FELT-A" {
		t.Fatalf("normalized felt binding resolves to %q, want the felt", got)
	}
	// Same key case-folded, and an ordinary line moved out of the purpose is still cleared.
	if got := NormalizeFabricBomLineKey("other", "felt-a", lines); got != "felt-a" {
		t.Fatalf("case-folded interlining key = %q, want it kept", got)
	}
	lines = append(lines, RollGoodsLine{LineKey: "MAIN-A", Purpose: "main", Section: BomSectionFabric})
	if got := NormalizeFabricBomLineKey("other", "MAIN-A", lines); got != "" {
		t.Fatalf("ordinary stale echo = %q, want cleared", got)
	}
}

func TestLiveFabricScopeKeyMapsARawSortedLineToItsPurpose(t *testing.T) {
	lines := []RollGoodsLine{
		{LineKey: "MAIN-A", Purpose: "main", Section: BomSectionFabric},
		{LineKey: "RAW-A", Section: BomSectionFabric},
		{LineKey: "FUSE-A", Purpose: "interfacing", Section: BomSectionInterlining},
	}
	tests := []struct {
		key, want string
		ok        bool
	}{
		{"main", "main", true},
		{"MAIN-A", "main", true}, // raw key of a sorted ordinary line → its purpose
		{"main-a", "main", true},
		{"RAW-A", "RAW-A", true}, // unsorted line: its own key is the scope
		{"FUSE-A", "FUSE-A", true},
		{"interfacing", "interfacing", true}, // singleton legacy interlining fallback stays live
		{"lining", "", false},
		{"GONE", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := LiveFabricScopeKey(tt.key, lines)
		if got != tt.want || ok != tt.ok {
			t.Errorf("LiveFabricScopeKey(%q) = (%q, %v), want (%q, %v)", tt.key, got, ok, tt.want, tt.ok)
		}
	}
}

func TestPurposeSharedByRibAndFeltKeepsFeltInItsOwnScope(t *testing.T) {
	lines := []RollGoodsLine{
		{LineKey: "RIB-A", Purpose: "other", Section: BomSectionFabric},
		{LineKey: "FELT-A", Purpose: "other", Section: BomSectionInterlining},
	}
	scope := ResolveFabricScope("other", "", lines)
	if scope.Key != "other" || !scope.ByPurpose || len(scope.LineKeys) != 1 || scope.LineKeys[0] != "RIB-A" {
		t.Fatalf("rib + felt purpose-only binding = %#v, want the rib-only purpose group", scope)
	}
	if got := FabricScopeIdentity("other", "", lines); got != "other" {
		t.Fatalf("purpose-only identity = %q, want other", got)
	}
	if _, ok := LegacyInterliningPurposeScope(lines[1], lines); ok {
		t.Fatal("felt received a legacy purpose fallback even though rib shares the purpose")
	}
	if got := FabricScopeIdentity("other", "FELT-A", lines); got != "FELT-A" {
		t.Fatalf("explicit felt binding = %q, want its own interlining line", got)
	}
}

func TestPreferredFabricArtifactsKeepLineAndLegacyInterliningSetsExclusive(t *testing.T) {
	line := RollGoodsLine{LineKey: "FUSE-A", Purpose: "interfacing", Section: BomSectionInterlining}
	lines := []RollGoodsLine{line}

	got := PreferredFabricArtifactsByScope([]FabricArtifact[string]{
		{FabricPurpose: "interfacing", Value: "legacy-a"},
		// A purpose-scoped row may retain the compatibility line; its raw DB scope is still the
		// purpose because scope_key is COALESCE(fabric_purpose, bom_line_key).
		{FabricPurpose: "interfacing", BomLineKey: "FUSE-A", Value: "legacy-b"},
		{BomLineKey: "FUSE-A", Value: "line-a"},
		{BomLineKey: "FUSE-A", Value: "line-b"},
	}, lines)
	if values := got["FUSE-A"]; len(values) != 2 || values[0] != "line-a" || values[1] != "line-b" {
		t.Fatalf("line-scoped set did not replace the legacy set wholesale: %v", values)
	}

	got = PreferredFabricArtifactsByScope([]FabricArtifact[string]{
		{FabricPurpose: "interfacing", Value: "legacy-a"},
		{FabricPurpose: "interfacing", Value: "legacy-b"},
	}, lines)
	if values := got["FUSE-A"]; len(values) != 2 || values[0] != "legacy-a" || values[1] != "legacy-b" {
		t.Fatalf("empty line set did not fall back to the complete singleton legacy set: %v", values)
	}
}

func TestPreferredFabricArtifactsLeaveOrdinaryPurposeGroupingUnchanged(t *testing.T) {
	lines := []RollGoodsLine{{LineKey: "RIB-A", Purpose: "other", Section: BomSectionFabric}}
	got := PreferredFabricArtifactsByScope([]FabricArtifact[string]{
		{FabricPurpose: "other", Value: "purpose"},
		{BomLineKey: "RIB-A", Value: "line"},
	}, lines)
	values := got["other"]
	if len(values) != 2 || values[0] != "purpose" || values[1] != "line" {
		t.Fatalf("ordinary purpose grouping changed: %v", values)
	}
}
