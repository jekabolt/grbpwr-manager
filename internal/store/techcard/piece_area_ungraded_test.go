package techcard

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
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

// Второй конец того же правила: пометку НЕЛЬЗЯ поставить на деталь, у которой уже лежат
// пер-размерные площади. Проверяется именно ПЕРЕХОД — тесты ниже держат обе стороны, потому что
// ошибиться здесь можно в две стороны, и обе тихие: слишком строгая проверка делает несохраняемой
// карточку, к градации которой никто не прикасался (а это весь живой массив), слишком слабая
// пропускает карточку, которая утверждает «контур один», пока норма считается по пяти разным.
func TestUngradedFlipKeys(t *testing.T) {
	const (
		pocket = "01POCKET0000000000000001"
		front  = "01FRONT00000000000000001"
	)
	mark := func(key string, ungraded, omitted bool) entity.TechCardPiece {
		return entity.TechCardPiece{LineKey: key, Name: "деталь", Ungraded: ungraded, UngradedOmitted: omitted}
	}

	cases := map[string]struct {
		pieces []entity.TechCardPiece
		stored map[string]bool
		want   []string // ключи (в верхнем регистре), которые считаются включением
	}{
		"false → true is a flip": {
			pieces: []entity.TechCardPiece{mark(pocket, true, false)},
			stored: map[string]bool{pocket: false},
			want:   []string{strings.ToUpper(pocket)},
		},
		// Весь живой массив: галочки никто не трогал, и правило обязано быть полностью инертным —
		// иначе первое же сохранение любой карточки уходит в запрос к площадям, а то и в отказ.
		"untouched false is not a flip": {
			pieces: []entity.TechCardPiece{mark(pocket, false, false), mark(front, false, false)},
			stored: map[string]bool{pocket: false, front: false},
		},
		// Молчащая вкладка не может ни включить флаг, ни нарваться на отказ: стор сохранит хранимое.
		"omitted field is never a flip": {
			pieces: []entity.TechCardPiece{mark(pocket, false, true), mark(front, true, true)},
			stored: map[string]bool{pocket: false, front: false},
		},
		// Уже помеченная деталь — не переход. Если противоречие в базе уже лежит, отказ на КАЖДОМ
		// сохранении сделал бы такую карточку неспасаемой.
		"already true is not a flip": {
			pieces: []entity.TechCardPiece{mark(pocket, true, false)},
			stored: map[string]bool{pocket: true},
		},
		// Обратный переход не ограничиваем вовсе: пер-размерные замеры для градуируемой детали — это
		// и есть правильная форма.
		"true → false is not a flip": {
			pieces: []entity.TechCardPiece{mark(pocket, false, false)},
			stored: map[string]bool{pocket: true},
		},
		// Новая деталь: ключ ей выпишет upsert, площадей под ним нет по построению.
		"a piece with no line key is skipped": {
			pieces: []entity.TechCardPiece{mark("  ", true, false)},
			stored: map[string]bool{},
		},
		// Деталь, которой в карточке ещё нет, но ключ у неё свой: хранимого значения нет — значит
		// false, значит включение. Ключ мог пережить удаление детали вместе со своими площадями.
		"a new keyed piece marked UNI counts as a flip": {
			pieces: []entity.TechCardPiece{mark(pocket, true, false)},
			stored: map[string]bool{},
			want:   []string{strings.ToUpper(pocket)},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ungradedFlipKeys(tc.pieces, tc.stored)
			if len(got) != len(tc.want) {
				t.Fatalf("flips = %v, want %v", got, tc.want)
			}
			for _, k := range tc.want {
				if _, ok := got[k]; !ok {
					t.Fatalf("flips = %v, must contain %q", got, k)
				}
			}
		})
	}
}

// Отказ обязан назвать деталь И сказать, что делать. Молчаливого удаления замеров здесь нет и быть
// не может: площади — измеренная геометрия, а не производное значение.
func TestUngradedFlipOnSizedAreas(t *testing.T) {
	const (
		pocket = "01POCKET0000000000000001"
		belt   = "01BELT000000000000000001"
	)
	pieces := []entity.TechCardPiece{
		{LineKey: "01FRONT00000000000000001", Name: "полочка"},
		{LineKey: pocket, Name: "карман"},
		{LineKey: belt, Name: "шлёвка"},
	}
	flips := map[string]int{strings.ToUpper(pocket): 1, strings.ToUpper(belt): 2}

	t.Run("refused when the piece carries per-size areas", func(t *testing.T) {
		ve := ungradedFlipOnSizedAreas(pieces, flips, map[string]bool{strings.ToUpper(pocket): true})
		if ve == nil {
			t.Fatal("marking a per-size measured piece UNI must be refused: the card would claim one outline while the norm keeps five")
		}
		if !strings.Contains(ve.Message, "карман") {
			t.Fatalf("refusal must name the piece, got %q", ve.Message)
		}
		if !strings.Contains(ve.Message, "перемерьте") {
			t.Fatalf("refusal must say how to fix it, got %q", ve.Message)
		}
		if strings.Contains(ve.Message, "шлёвка") {
			t.Fatalf("refusal named a piece with no sized areas, got %q", ve.Message)
		}
		if !strings.Contains(ve.Message, "pieces[1].ungraded") {
			t.Fatalf("refusal must point at the offending piece's field, got %q", ve.Message)
		}
	})

	t.Run("allowed when the piece has no areas at all", func(t *testing.T) {
		if ve := ungradedFlipOnSizedAreas(pieces, flips, map[string]bool{}); ve != nil {
			t.Fatalf("a piece nobody measured must be markable: %v", ve.Message)
		}
	})

	t.Run("allowed when only a sizeless area is stored", func(t *testing.T) {
		// Безразмерная строка в множество sizedAreas не попадает по построению (запрос берёт только
		// size_id IS NOT NULL) — то есть именно та форма, которую пометка и утверждает, ей не мешает.
		if ve := ungradedFlipOnSizedAreas(pieces, flips, map[string]bool{"01OTHER00000000000000001": true}); ve != nil {
			t.Fatalf("a piece measured once without a size must be markable: %v", ve.Message)
		}
	})

	t.Run("every offender is named at once", func(t *testing.T) {
		ve := ungradedFlipOnSizedAreas(pieces, flips, map[string]bool{
			strings.ToUpper(pocket): true, strings.ToUpper(belt): true,
		})
		if ve == nil {
			t.Fatal("two offenders must still be refused")
		}
		if !strings.Contains(ve.Message, "карман") || !strings.Contains(ve.Message, "шлёвка") {
			t.Fatalf("both offenders must be named in one refusal, got %q", ve.Message)
		}
	})

	t.Run("nothing flipping is a no-op", func(t *testing.T) {
		if ve := ungradedFlipOnSizedAreas(pieces, map[string]int{}, map[string]bool{strings.ToUpper(pocket): true}); ve != nil {
			t.Fatalf("a card that changes no flag must not be judged: %v", ve.Message)
		}
	})
}
