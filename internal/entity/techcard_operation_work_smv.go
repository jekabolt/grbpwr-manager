package entity

import (
	"time"

	"github.com/shopspring/decimal"
)

// ПОДСКАЗКА НОРМЫ ВРЕМЕНИ — «В ПРОШЛЫЙ РАЗ БЫЛО СТОЛЬКО-ТО» (R3, пункт 6 решений фазы).
//
// ЧТО ЭТО И ЧЕМ ОНО НЕ ЯВЛЯЕТСЯ. SMV — единственное поле времени шага (`smv`, минуты), и оно
// зависит от ИЗДЕЛИЯ, а не только от работы: та же «отстрочка» на джерси и на пальтовой ткани
// нормируется по-разному. Поэтому подсказка — ГОУСТ в пустом поле («last: 1.2, ПАЛЬТО»), а НЕ
// значение и НЕ дефолт: в operation_work_default норма времени не кладётся никогда, и жеста
// «запомнить норму как дефолт» нет вовсе (реестр её не содержит — см. techcard_operation_work_default.go).
//
// ⚠️ «ПОСЛЕДНИЙ» СЧИТАЕТСЯ ЗДЕСЬ, А НЕ В SQL, И ЭТО РЕШЕНИЕ. Правило «подсказка берётся из
// ПОСЛЕДНЕГО шага с этой работой» — единственное содержательное во всей подсказке, и в SQL оно было
// бы непроверяемым: тесты хранилища этого проекта ходят в живую базу и запускаться не могут.
// Чистая функция над срезом проверяется таблицей клеток, включая ту, ради которой правило и
// написано: два шага одной работы на разных карточках → побеждает свежий.
//
// ЧТО ТАКОЕ «СВЕЖИЙ». У tech_card_operation НЕТ своего updated_at — строки шагов переписываются
// ПОЛНОЙ ЗАМЕНОЙ на каждом сохранении карточки, поэтому возраст строки это возраст КАРТОЧКИ
// (tech_card.updated_at). Внутри одной карточки все строки написаны одним сохранением, и различает
// их только auto_increment id — он и стоит вторым ключом сравнения, чтобы порядок был полным, а
// ответ — не зависящим от порядка, в котором строки пришли из базы.

// OperationWorkSmvSample is ONE stored step that named a work and carried a time norm. The store
// reads these narrow rows; the rule below turns them into one hint per work.
type OperationWorkSmvSample struct {
	WorkToken string          `db:"work_token"`
	SMV       decimal.Decimal `db:"smv"`
	CardName  string          `db:"card_name"`
	// CardUpdatedAt — возраст СТРОКИ ШАГА, потому что у шага своего возраста нет (см. шапку).
	CardUpdatedAt time.Time `db:"card_updated_at"`
	// OperationID разводит строки ОДНОЙ карточки: они написаны одним сохранением и по времени
	// неразличимы. Больший id = позже вставленная строка.
	OperationID int64 `db:"operation_id"`
}

// OperationWorkSmvHint is what the client shows as a ghost in an empty SMV field.
type OperationWorkSmvHint struct {
	WorkToken string
	LastSMV   decimal.Decimal
	CardName  string
}

// LatestOperationWorkSmv reduces the samples to at most one hint per work — the most recent step
// that named that work.
//
// ПОРЯДОК ВХОДА НЕ ЗНАЧИМ. Функция сравнивает сама и выигрывает у любого порядка, в котором строки
// пришли: ORDER BY в запросе существует ради LIMIT (он обязан отрезать САМЫЕ СТАРЫЕ строки, а не
// случайные), а не ради ответа. Пропади ORDER BY — ответ остался бы правильным, и это специально:
// правило не должно зависеть от того, чего тест увидеть не может.
func LatestOperationWorkSmv(samples []OperationWorkSmvSample) []OperationWorkSmvHint {
	if len(samples) == 0 {
		return nil
	}
	best := make(map[string]OperationWorkSmvSample, len(samples))
	// order сохраняет порядок ПЕРВОГО появления работы, чтобы ответ не зависел от обхода карты.
	order := make([]string, 0, len(samples))
	for _, s := range samples {
		if s.WorkToken == "" {
			continue
		}
		cur, seen := best[s.WorkToken]
		if !seen {
			best[s.WorkToken] = s
			order = append(order, s.WorkToken)
			continue
		}
		if newerOperationWorkSample(s, cur) {
			best[s.WorkToken] = s
		}
	}
	out := make([]OperationWorkSmvHint, 0, len(order))
	for _, token := range order {
		s := best[token]
		out = append(out, OperationWorkSmvHint{WorkToken: token, LastSMV: s.SMV, CardName: s.CardName})
	}
	return out
}

// newerOperationWorkSample reports whether a is more recent than b: card age first, then the row's
// own auto_increment id inside one card.
func newerOperationWorkSample(a, b OperationWorkSmvSample) bool {
	if !a.CardUpdatedAt.Equal(b.CardUpdatedAt) {
		return a.CardUpdatedAt.After(b.CardUpdatedAt)
	}
	return a.OperationID > b.OperationID
}
