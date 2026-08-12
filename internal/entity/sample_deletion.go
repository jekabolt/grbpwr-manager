package entity

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// УДАЛЕНИЕ СЕМПЛА — и почему одного «нельзя» было недостаточно.
//
// Семпл удалялся ровно до тех пор, пока за ним не было НИ ОДНОГО движения материала: стор считал
// COUNT(*) по material_stock_movement и отказывал на первом же. Довод был верный (удалить семпл со
// списанной тканью значит осиротить её стоимость: sample_id уходит в NULL по ON DELETE SET NULL, а
// сводка стиля считает расход на сэмплирование ДЖОЙНОМ по sample.id — деньги просто исчезли бы из
// отчёта), но следствие оказалось непроходимым: мастер создания семпла списывает ткань по BOM сразу,
// в том же жесте. То есть почти каждый семпл рождался неудаляемым, а «удалить» на экране было
// кнопкой, которая никогда не срабатывает.
//
// ГРАНИЦА ВЛАДЕЛЬЦА. Удаляем семпл, за которым НЕ ОСТАЛОСЬ ничего живого:
//   - материал ВОЗВРАЩЁН на склад (чистый расход по каждому материалу = 0) — возврат делает
//     оператор РУКАМИ, отдельным жестом, и он виден в ленте склада. Удаление само на склад не
//     ходит: тихо оприходовать ткань за оператора значит написать в учёте факт, которого никто не
//     совершал, — а именно на этой ленте стоит оценка запасов.
//   - на семпле нет ПРИМЕРОК. Схема их бы пережила (fitting.sample_id — SET NULL), но примерка без
//     семпла — это вердикт, снятый ни с чего. Отказ называет их и просит удалить первыми.
//
// Всё остальное — не блокеры, а последствия, и диалог обязан их назвать ДО подтверждения: своё
// (медиа, замены материалов) уходит каскадом, чужое (dev-расходы, задачи, следующие раунды,
// НУЛЕВЫЕ движения склада) переживает удаление и теряет семпл.
//
// Классификация живёт здесь, а не в сторе, ровно потому, что это единственная часть удаления,
// которую можно проверить без базы: стор в этом репозитории тестируется только интеграционно.

// ErrSampleNotDeletable — вердикт «удалять нельзя». Причины едут в SampleDeletionVerdict, который
// стор возвращает РЯДОМ с этой ошибкой, поэтому API-слой раскладывает блокеры по одному field
// violation на категорию, а не сводит их в одну строку. Отображается в FailedPrecondition.
var ErrSampleNotDeletable = errors.New("sample cannot be deleted")

// Коды причин. Строка, а не enum, — по тому же соглашению, что у вердикта удаления колорвея: код
// стабилен, живёт в одном месте и не заводит нулевого значения, которое читалось бы как «причины нет».
const (
	// Блокеры — граница владельца.
	SampleBlockerMaterialOutstanding = "material_outstanding"   // материал списан на семпл и не возвращён
	SampleBlockerMaterialOverReturn  = "material_over_returned" // возвращено БОЛЬШЕ, чем выдавалось
	SampleBlockerFitting             = "fitting"                // на семпле есть примерки
	SampleBlockerReferenced          = "referenced"             // RESTRICT, которого нет в перечислении: схема ушла вперёд

	// Каскад — собственность семпла, уходит вместе с ним (ON DELETE CASCADE).
	SampleCascadeMedia        = "media"        // sample_media
	SampleCascadeSubstitution = "substitution" // sample_substitution

	// Сироты — ON DELETE SET NULL: запись переживёт удаление и потеряет семпл.
	SampleOrphanMaterialMovement = "orphan_material_movement" // material_stock_movement.sample_id
	SampleOrphanDevExpense       = "orphan_dev_expense"       // tech_card_dev_expense.sample_id
	SampleOrphanTask             = "orphan_task"              // task.sample_id
	SampleOrphanNextRound        = "orphan_next_round"        // sample.previous_sample_id
)

// Как выйти из блокера. Выходы РАЗНЫЕ — в отличие от колорвея, где на все блокеры был один ответ
// «архивируйте». Здесь каждый блокер снимается своим действием, и назвать его — половина ответа.
const (
	sampleFixReturnMaterial = "верните материал на склад из панели МАТЕРИАЛЫ этого семпла, затем удаляйте"
	// Возврат списанному семплу запрещён на входе в склад (checkSampleOpen): статус scrapped
	// означает «материал уничтожен вместе с семплом», и оприходовать его обратно нельзя. Выход
	// поэтому начинается со смены статуса — либо семпл остаётся как запись о съеденной ткани.
	sampleFixReturnScrapped = "снимите статус «списан» (списанному семплу возврат материала запрещён), верните материал на склад, затем удаляйте"
	sampleFixOverReturn     = "лента склада по этому семплу разошлась: возвращено больше, чем выдавалось — исправьте движения, прежде чем удалять семпл"
	sampleFixDeleteFittings = "удалите примерки этого семпла, затем удаляйте сам семпл"
	sampleFixReferenced     = "снимите названную связь; если её не видно на экране — это дефект перечисления на сервере"
)

// SampleOutstandingMaterial — один материал и его ЧИСТЫЙ расход на семпле: выдано минус возвращено.
// Ноль значит «всё вернулось» и удалению не мешает; плюс — ткань всё ещё на семпле; минус — лента
// разошлась (вернули больше, чем брали), и это тоже отказ, но с другим разговором.
type SampleOutstandingMaterial struct {
	MaterialID int             `db:"material_id"`
	Name       string          `db:"name"`
	Unit       string          `db:"unit"`
	Qty        decimal.Decimal `db:"qty"`
	// CostedValue — та же разность в деньгах (база), но ТОЛЬКО по костированным движениям. В
	// вердикт не входит и ничего не блокирует: остаток по деньгам при нулевом остатке по количеству
	// возникает из некостированной выдачи, которую оператор задним числом не оценит. Считается
	// затем, чтобы сирота-движение могло честно сказать, что в ленте осталась неснятая стоимость.
	CostedValue decimal.Decimal `db:"costed_value"`
}

// Outstanding — материал ещё на семпле.
func (m SampleOutstandingMaterial) Outstanding() bool { return m.Qty.IsPositive() }

// OverReturned — вернули больше, чем выдавали.
func (m SampleOutstandingMaterial) OverReturned() bool { return m.Qty.IsNegative() }

// SampleCascadeCounts — сколько собственных строк семпла уйдёт вместе с ним.
type SampleCascadeCounts struct {
	Media         int // sample_media
	Substitutions int // sample_substitution
}

// SampleOrphanCounts — сколько ЧУЖИХ записей переживут удаление и потеряют семпл.
type SampleOrphanCounts struct {
	MaterialMovements int // material_stock_movement.sample_id (только уже сошедшиеся в ноль)
	DevExpenses       int // tech_card_dev_expense.sample_id
	Tasks             int // task.sample_id
	NextRounds        int // sample.previous_sample_id — следующие раунды теряют звено цепочки
}

// SampleDeletionFacts — сырые факты о семпле одним снимком. Стор читает их и ПОВТОРНО внутри
// транзакции удаления: предикат, доказанный вне транзакции, — это гонка, а эта гонка удаляет.
type SampleDeletionFacts struct {
	SampleID int
	// Label — как называть семпл в предложении: «сэмпл #3», а при отсутствующем номере — по id.
	Label string
	// Scrapped — статус «списан». Меняет не вердикт, а ВЫХОД из него: складу запрещено принимать
	// возврат по списанному семплу, поэтому совет «верните материал» без снятия статуса
	// невыполним, и оператор бился бы в отказ склада, следуя совету сервера.
	Scrapped bool

	Materials []SampleOutstandingMaterial
	Fittings  int

	Cascade SampleCascadeCounts
	Orphans SampleOrphanCounts
}

// SampleDeletionEntry — одна запись вердикта: стабильный код, готовая фраза, количество. Фраза
// собирается на сервере по тому же доводу, что и у колорвея: формулировка одна на систему, второй
// перевод на клиенте разошёлся бы с ней в первый же день.
type SampleDeletionEntry struct {
	Reason string
	Text   string
	Count  int
}

// SampleDeletionVerdict — ответ на вопрос «можно ли удалить», одинаковый для сухого прогона и для
// настоящего удаления. Три списка: что держит, что умрёт вместе, что осиротеет.
type SampleDeletionVerdict struct {
	SampleID  int
	Label     string
	Deletable bool
	Blockers  []SampleDeletionEntry
	Cascade   []SampleDeletionEntry
	Orphans   []SampleDeletionEntry
}

// ClassifySampleDeletion превращает факты в вердикт. Единственное место, где живёт предикат
// удаляемости: и сухой прогон, и удаление применяют ОДНО И ТО ЖЕ правило. Совпадение ОТВЕТОВ это не
// гарантирует и гарантировать не может — если между прогоном и подтверждением на семпл записали
// примерку или списали ткань, транзакция обязана решить иначе, и ровно ради этого читает факты
// заново. Одинаково здесь правило, а не результат.
//
// Пустые категории в списки НЕ попадают: «0 задач осиротеет» — шум, который оператор научится
// пролистывать вместе с настоящими строками.
func ClassifySampleDeletion(f SampleDeletionFacts) SampleDeletionVerdict {
	v := SampleDeletionVerdict{SampleID: f.SampleID, Label: f.Label}

	// --- Блокер: материал не вернулся на склад ------------------------------------------------
	//
	// ОДНА запись на все материалы, а не строка на каждый: это ОДИН отказ («ткань не возвращена») с
	// перечислением, и снимается он одним походом в панель материалов. Количество в записи — число
	// материалов, а не метры: метры разные по единицам и не складываются.
	var outstanding, over []SampleOutstandingMaterial
	for _, m := range f.Materials {
		switch {
		case m.Outstanding():
			outstanding = append(outstanding, m)
		case m.OverReturned():
			over = append(over, m)
		}
	}
	if len(outstanding) > 0 {
		v.Blockers = append(v.Blockers, SampleDeletionEntry{
			Reason: SampleBlockerMaterialOutstanding,
			Count:  len(outstanding),
			Text:   fmt.Sprintf("не возвращено на склад: %s", joinNames(materialQtyNames(outstanding))),
		})
	}
	if len(over) > 0 {
		// Возвращено больше, чем выдавалось. Удалять такой семпл нельзя не из осторожности: его
		// движения — единственное, что объясняет лишний приход на складе, и, потеряв семпл, этот
		// приход становится необъяснимым навсегда.
		v.Blockers = append(v.Blockers, SampleDeletionEntry{
			Reason: SampleBlockerMaterialOverReturn,
			Count:  len(over),
			Text:   fmt.Sprintf("возвращено больше, чем выдавалось: %s", joinNames(materialQtyNames(over))),
		})
	}

	// --- Блокер: примерки ----------------------------------------------------------------------
	if f.Fittings > 0 {
		v.Blockers = append(v.Blockers, SampleDeletionEntry{
			Reason: SampleBlockerFitting,
			Count:  f.Fittings,
			Text: fmt.Sprintf("на нём записано %s",
				pluralRU(f.Fittings, "%d примерка", "%d примерки", "%d примерок")),
		})
	}

	v.Deletable = len(v.Blockers) == 0

	// --- Каскад --------------------------------------------------------------------------------
	v.Cascade = appendSampleEntry(v.Cascade, SampleCascadeMedia, f.Cascade.Media,
		"%d фотография семпла", "%d фотографии семпла", "%d фотографий семпла")
	v.Cascade = appendSampleEntry(v.Cascade, SampleCascadeSubstitution, f.Cascade.Substitutions,
		"%d замена материала", "%d замены материала", "%d замен материала")

	// --- Сироты --------------------------------------------------------------------------------
	//
	// Движения склада переживают удаление намеренно: лента приход-расход — это учёт, и стирать из
	// неё строки задним числом значит переписывать остатки прошлых дней. Сюда они попадают уже
	// сошедшимися в ноль (иначе выше стоял бы блокер), поэтому расход стиля на сэмплирование от
	// потери семпла не меняется — но в самой ленте пара строк останется без владельца, и оператор
	// обязан узнать это ДО подтверждения.
	o := f.Orphans
	v.Orphans = appendSampleEntry(v.Orphans, SampleOrphanMaterialMovement, o.MaterialMovements,
		"%d движение материала останется в ленте склада без семпла",
		"%d движения материала останутся в ленте склада без семпла",
		"%d движений материала останутся в ленте склада без семпла")
	// Dev-расходы — ДЕНЬГИ, и они остаются в расходах карточки; теряется только адрес «за какой
	// именно семпл заплатили». Удалять их вместе с семплом было бы хуже: потраченное не исчезает
	// оттого, что запись о прототипе стёрли.
	v.Orphans = appendSampleEntry(v.Orphans, SampleOrphanDevExpense, o.DevExpenses,
		"%d dev-расход останется на карточке и потеряет семпл",
		"%d dev-расхода останутся на карточке и потеряют семпл",
		"%d dev-расходов останутся на карточке и потеряют семпл")
	v.Orphans = appendSampleEntry(v.Orphans, SampleOrphanTask, o.Tasks,
		"%d задача потеряет семпл", "%d задачи потеряют семпл", "%d задач потеряют семпл")
	v.Orphans = appendSampleEntry(v.Orphans, SampleOrphanNextRound, o.NextRounds,
		"%d следующий раунд потеряет звено цепочки",
		"%d следующих раунда потеряют звено цепочки",
		"%d следующих раундов потеряют звено цепочки")

	return v
}

// FieldViolations раскладывает блокеры в field-tagged отказы для apierr.FailedPreconditionMany — по
// одному на КАТЕГОРИЮ. Оператор, у которого и ткань не возвращена, и примерки записаны, должен
// увидеть обе причины за один заход: узнавать вторую после того, как снял первую, — это два круга
// там, где сервер знал ответ целиком с первого раза.
//
// Совет по каждому блокеру СВОЙ — и он единственное место, где вердикт учитывает статус «списан»:
// «верните материал» без снятия статуса невыполним, потому что склад откажет.
func (v SampleDeletionVerdict) FieldViolations(scrapped bool) []*ValidationError {
	out := make([]*ValidationError, 0, len(v.Blockers))
	for _, b := range v.Blockers {
		out = append(out, NewFieldViolation("sample_id", b.Reason, b.Text, SampleBlockerHowToFix(b.Reason, scrapped)))
	}
	return out
}

// SampleBlockerHowToFix — выход из блокера одной фразой. Экспортирован потому, что его печатают ДВА
// пути: field violation настоящего отказа и сухой прогон, который читает диалог подтверждения. Один
// источник — иначе диалог и ошибка начали бы советовать разное по одному и тому же факту.
func SampleBlockerHowToFix(reason string, scrapped bool) string {
	switch reason {
	case SampleBlockerMaterialOutstanding:
		if scrapped {
			return sampleFixReturnScrapped
		}
		return sampleFixReturnMaterial
	case SampleBlockerMaterialOverReturn:
		return sampleFixOverReturn
	case SampleBlockerFitting:
		return sampleFixDeleteFittings
	default:
		return sampleFixReferenced
	}
}

// BlockerSummary — одна строка для сообщения статуса (клиент, читающий только message).
func (v SampleDeletionVerdict) BlockerSummary() string {
	if len(v.Blockers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(v.Blockers))
	for _, b := range v.Blockers {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "; ")
}

func appendSampleEntry(dst []SampleDeletionEntry, reason string, n int, one, few, many string) []SampleDeletionEntry {
	if n <= 0 {
		return dst
	}
	return append(dst, SampleDeletionEntry{Reason: reason, Count: n, Text: pluralRU(n, one, few, many)})
}

// materialQtyNames — «2.4 m «Wool Melton 340»». Количество печатается БЕЗ хвостовых нулей: 2.400
// это то, как число лежит в DECIMAL(12,3), а не то, сколько оператор отмерил. Единица может быть
// пустой (в справочнике материалов она необязательна) — тогда её просто нет, а не «шт» по догадке.
func materialQtyNames(ms []SampleOutstandingMaterial) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		qty := m.Qty.Abs().String()
		name := m.Name
		if name == "" {
			name = fmt.Sprintf("материал #%d", m.MaterialID)
		}
		if unit := strings.TrimSpace(m.Unit); unit != "" {
			out = append(out, fmt.Sprintf("%s %s «%s»", qty, unit, name))
			continue
		}
		out = append(out, fmt.Sprintf("%s «%s»", qty, name))
	}
	return out
}
