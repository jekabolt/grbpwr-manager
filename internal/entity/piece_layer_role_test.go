package entity

import "testing"

// Таблица деривации роли слоя (T4): рулонные секции — purpose, иначе фолбэк по секции; fabric без
// назначения — «не разложено» (единственная секция без фолбэка, 0265 сознательно не гадал);
// нерулонные — вне модели ролей.
func TestDerivePieceLayerRole(t *testing.T) {
	cases := []struct {
		name    string
		section TechCardBomSection
		purpose string
		role    TechCardBomPurpose
		roll    bool
	}{
		{"fabric без назначения — не разложено", BomSectionFabric, "", PieceLayerRoleUnsorted, true},
		{"fabric main", BomSectionFabric, "main", BomPurposeMain, true},
		{"fabric pocketing", BomSectionFabric, "pocketing", BomPurposePocketing, true},
		{"fabric contrast", BomSectionFabric, "contrast", BomPurposeContrast, true},
		{"lining без назначения — фолбэк подкладка", BomSectionLining, "", BomPurposeLining, true},
		{"interlining без назначения — фолбэк дублерин", BomSectionInterlining, "", BomPurposeInterfacing, true},
		{"insulation без назначения — фолбэк утеплитель", BomSectionInsulation, "", BomPurposeInsulation, true},
		{"назначение бьёт фолбэк секции", BomSectionLining, "pocketing", BomPurposePocketing, true},
		{"мусорное назначение читается как не задано", BomSectionFabric, "BOGUS", PieceLayerRoleUnsorted, true},
		{"мусорное назначение на lining падает в фолбэк", BomSectionLining, "BOGUS", BomPurposeLining, true},
		{"hardware — вне модели ролей", BomSectionHardware, "", "", false},
		{"trim — вне модели ролей", BomSectionTrim, "piping", "", false},
		{"label — вне модели ролей", BomSectionLabel, "", "", false},
	}
	for _, tc := range cases {
		role, roll := DerivePieceLayerRole(tc.section, tc.purpose)
		if role != tc.role || roll != tc.roll {
			t.Errorf("%s: DerivePieceLayerRole(%s, %q) = (%q, %v), want (%q, %v)",
				tc.name, tc.section, tc.purpose, role, roll, tc.role, tc.roll)
		}
	}
}

// Подписи ролей: каждая роль словаря назначений имеет имя, «не разложено» — своё.
//
// Покрытие проверяется ПО СЛОВАРЮ, а не сравнением подписи с самим значением: подписи английские,
// и у половины ролей человеческое имя совпадает с значением enum'а ('lining' → "lining"), так что
// проверка «подпись ≠ значение» доказывала бы только то, что подписи не английские.
func TestPieceLayerRoleLabel(t *testing.T) {
	if got := PieceLayerRoleLabel(PieceLayerRoleUnsorted); got != "unsorted" {
		t.Errorf("unsorted label = %q", got)
	}
	if got := PieceLayerRoleLabel(BomPurposeMain); got != "main fabric" {
		t.Errorf("main label = %q", got)
	}
	for _, p := range BomPurposeOrder {
		if _, ok := pieceLayerRoleLabels[p]; !ok {
			t.Errorf("purpose %q has no human label in the dictionary", p)
		}
		if PieceLayerRoleLabel(p) == "" {
			t.Errorf("purpose %q renders an empty label", p)
		}
	}
}
