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

// Подписи ролей: каждая роль словаря назначений имеет русское имя, «не разложено» — своё.
func TestPieceLayerRoleLabel(t *testing.T) {
	if got := PieceLayerRoleLabel(PieceLayerRoleUnsorted); got != "не разложено" {
		t.Errorf("unsorted label = %q", got)
	}
	if got := PieceLayerRoleLabel(BomPurposeMain); got != "основная ткань" {
		t.Errorf("main label = %q", got)
	}
	for _, p := range BomPurposeOrder {
		if PieceLayerRoleLabel(p) == "" || PieceLayerRoleLabel(p) == string(p) {
			t.Errorf("purpose %q has no human label", p)
		}
	}
}
