package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ВХОДЫ СЕБЕСТОИМОСТИ, КОТОРЫХ НЕТ В ЗАПИСИ КАРТОЧКИ (Ф-П).
//
// ЗАЧЕМ ЭТО СУЩЕСТВУЕТ. Подпись раздела — обещание «содержимое, которое я утвердил, с тех пор не
// менялось», и держится оно на дайджесте, который считается ОДНОЙ функцией на записи и на чтении.
// Функция эта видит только entity.TechCardInsert — то, что карточка пишет о себе целиком.
//
// Но начиная с Ф1 себестоимость выводится из данных, которых в записи карточки НЕТ ВООБЩЕ:
//
//   - ПЛОЩАДИ ДЕТАЛЕЙ (0297) — их пишет отдельный RPC со вкладки выкроек;
//   - НАЗНАЧЕНИЕ ДЕТАЛИ НА ТКАНЬ в рецепте колорвея — его пишет UpdateColorwayRecipe.
//
// Оставить их вне подписи означало бы вот что: сохранённая подпись КОСТИНГ остаётся зелёной, пока
// кто-то заново разбирает выкройки или перекладывает деталь с основной ткани на подкладку — то есть
// пока меняется ровно то число, под которым стоит подпись. Это не «неточность учёта», это подпись
// под одним документом и отгрузка другого.
//
// ПОЧЕМУ ОДИН ТОКЕН, А НЕ ДЕСЯТЬ ПОЛЕЙ В ПРОЕКЦИИ. Проекция обязана считаться одинаково на обеих
// сторонах, а на стороне ЗАПИСИ этих данных нет и взяться им неоткуда: запись карточки их не несёт.
// Значит их обязан подставить тот, кто их видит — стор. Один готовый токен переносится через границу
// тривиально и не заставляет тащить в проекцию половину модели; ровно тем же приёмом сюда попадает
// разрешённая через каталог идентичность строки BOM (withResolvedBomIdentity).
//
// ЧТО СЮДА НЕ ВХОДИТ: parsed_by / parsed_at площадей и любые другие следы аудита. Правило дома —
// «метаданные о значении не двигают подпись, значение двигает» (так же исключены price_source,
// wastage_source и purpose). Пересчёт выкроек, давший ТЕ ЖЕ площади, подпись не трогает.

// PieceFabricAssignment is one «эта деталь кроится из этой ткани» statement of a colourway recipe.
type PieceFabricAssignment struct {
	// ColorwayKey identifies the colourway (its product id, else the recipe row's own id).
	ColorwayKey int
	// PieceKey / BomKey are line keys when the reader has them and ids otherwise — the digest only
	// needs them to be STABLE and to differ when the assignment differs.
	PieceKey string
	BomKey   string
	// PiecesPerGarment is the multiplicity the cost multiplies the contour area by. It lives on the
	// piece, not on the assignment, but it enters the SAME derivation and therefore the same
	// signature: «this piece goes twice» is a different cost with the same areas.
	PiecesPerGarment int
	// PinnedMaterialId is the article this colourway pins on the piece (0 = none). The estimate takes
	// BOTH the price and the cutting width from the effective article, so re-pinning a piece reprices
	// the garment — and a signature that did not see it would be green over a changed number.
	PinnedMaterialId int64
}

// DerivedCostInputsDigest fingerprints the cost inputs that live outside the card's own write:
// measured piece areas and the recipe's piece→fabric assignments.
//
// EMPTY IN, EMPTY OUT — and that is load-bearing. A card with neither measured areas nor piece-bound
// recipe rows returns "", the projection appends nothing, and its stored digest stays byte-identical
// to what its approver signed. Without that, merely deploying this phase would declare every
// approved COSTING sign-off in the database stale, before anybody had changed anything. The
// re-approval wave is then the size of the measuring campaign, not of the rollout.
func DerivedCostInputsDigest(areas map[string]PieceAreaScope, assignments []PieceFabricAssignment) string {
	areaRows := make([][]any, 0, 16)
	for scope, sc := range areas {
		// УСТАРЕВАНИЕ СКОУПА ВХОДИТ В ТОКЕН. Перезалитая выкройка не меняет ни одной сохранённой
		// площади — она меняет ВЕРДИКТ о них: с этого момента Ф1 обязана сказать «выкройки менялись»
		// и отказаться считать по ним. Подпись, не заметившая перехода, осталась бы зелёной над
		// цифрой, которой больше нет.
		areaRows = append(areaRows, []any{scope, "\x00stale", sc.Stale})
		for _, r := range sc.Rows {
			// The measurement CONDITIONS ride along with the number: the same contour measured on the
			// seam line and on the cutting line are two different areas, and a card re-measured under
			// different conditions must not keep a signature stamped under the old ones.
			areaRows = append(areaRows, []any{
				scope,
				strings.ToUpper(strings.TrimSpace(r.PieceLineKey)),
				r.SizeId.Int64, // 0 = ungraded, the same sentinel the wire uses
				r.AreaCm2.String(),
				r.ContourLayer,
				r.SeamAllowanceMm.String(),
				// СОСТОЯНИЯ РЕЗУЛЬТАТА ЗАМЕРА, А НЕ СЛЕДЫ АУДИТА, поэтому они здесь: контур,
				// заменённый выпуклой оболочкой, — это ДРУГАЯ площадь, а неоднозначный выбор
				// кандидата прямо запрещает сравнивать число с чем-либо. Утвердить одно и получить
				// другое — ровно то, от чего подпись защищает.
				r.Hulled,
				r.AmbiguousPick,
			})
		}
	}
	assignRows := make([][]any, 0, len(assignments))
	for _, a := range assignments {
		assignRows = append(assignRows, []any{
			a.ColorwayKey,
			strings.ToUpper(strings.TrimSpace(a.PieceKey)),
			strings.ToUpper(strings.TrimSpace(a.BomKey)),
			a.PiecesPerGarment,
			a.PinnedMaterialId,
		})
	}
	if len(areaRows) == 0 && len(assignRows) == 0 {
		return ""
	}
	// SORTED, because only the SET is the input: both halves are read as maps/joins whose order is
	// the query's business, and a reshuffle must not read as «changed since sign-off».
	sortRows(areaRows)
	sortRows(assignRows)
	blob, err := json.Marshal([]any{areaRows, assignRows})
	if err != nil {
		// Unreachable: every element is a string or an integer. Hashing the error keeps a read from
		// panicking, and the value differs from every real one, so it reads as «changed» rather than
		// as «unchanged» — the safe direction.
		blob = []byte("derived-cost-inputs-encode-error:" + err.Error())
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}

func sortRows(rows [][]any) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		for k := 0; k < len(a) && k < len(b); k++ {
			ka, kb := fmt.Sprint(a[k]), fmt.Sprint(b[k])
			if ka != kb {
				return ka < kb
			}
		}
		return len(a) < len(b)
	})
}
