package techcard

import (
	"strings"
	"testing"
)

// Пометка UNI (0302) и форма замера — РАЗНЫЕ вещи, и правило одностороннее. Тест держит обе стороны
// именно потому, что «симметричная» правка тут выглядит естественной и ломает всю базу: запрет
// безразмерной строки у непомеченной детали сделал бы несохраняемой каждую карточку, замеренную до
// 0302 (там флаг стоит false по умолчанию, а безразмерные строки уже лежат).
func TestUngradedPieceSizedDiff(t *testing.T) {
	const pocket = "01POCKET0000000000000001"
	const front = "01FRONT00000000000000001"

	cases := map[string]struct {
		sizes    map[string]map[int]bool
		ungraded map[string]string
		want     string // "" = принять; иначе подстрока, которую обязан назвать отказ
	}{
		"marked piece measured per size is refused by name": {
			sizes:    map[string]map[int]bool{pocket: {4: true, 5: true}},
			ungraded: map[string]string{pocket: "карман"},
			want:     "карман",
		},
		"marked piece measured once without a size passes": {
			sizes:    map[string]map[int]bool{pocket: {0: true}},
			ungraded: map[string]string{pocket: "карман"},
		},
		// Смесь форм — жалоба sizeCoverageDiff, но у ПОМЕЧЕННОЙ детали пер-размерные строки не
		// становятся законными оттого, что рядом лежит и безразмерная.
		"marked piece measured both ways is still refused": {
			sizes:    map[string]map[int]bool{pocket: {0: true, 4: true}},
			ungraded: map[string]string{pocket: "карман"},
			want:     "карман",
		},
		// Обратная сторона правила, которую вводить НЕЛЬЗЯ.
		"unmarked piece measured without a size is legal": {
			sizes:    map[string]map[int]bool{front: {0: true}},
			ungraded: map[string]string{},
		},
		"unmarked piece measured per size is legal": {
			sizes:    map[string]map[int]bool{front: {4: true, 5: true}},
			ungraded: map[string]string{pocket: "карман"},
		},
		// Деталь без имени всё равно должна быть опознаваема — иначе отказ показывает пустые кавычки.
		"nameless marked piece falls back to its line key": {
			sizes:    map[string]map[int]bool{pocket: {4: true}},
			ungraded: map[string]string{pocket: ""},
			want:     pocket,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ungradedPieceSizedDiff(tc.sizes, tc.ungraded)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("legal measurement refused: %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("refusal must name the piece: got %q, want it to contain %q", got, tc.want)
			}
		})
	}
}
