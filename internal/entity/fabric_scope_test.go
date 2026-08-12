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
		{LineKey: fsLineMain, Purpose: "main"},
		{LineKey: fsLineLining, Purpose: "lining"},
		{LineKey: fsLineNoPurp},
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
		{LineKey: fsLineMain, Purpose: "main"},
		{LineKey: fsLineLining, Purpose: "main"},
	}
	a := FabricScopeIdentity("", fsLineMain, lines)
	b := FabricScopeIdentity("", fsLineLining, lines)
	if a != "main" || b != "main" {
		t.Fatalf("two lines of one назначение resolved to %q and %q, want both \"main\"", a, b)
	}
}
