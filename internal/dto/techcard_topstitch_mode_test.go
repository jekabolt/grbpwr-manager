package dto

import (
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// ШИРИНА И РЯДЫ ОТСТРОЧКИ БЕЗ РЕЖИМА — ОТКАЗ ПО ИМЕНИ, А НЕ ТИХИЙ ДРОП (Ф3).
//
// До этого правила parseTopstitch на `mode == UNKNOWN` возвращал три пустых значения И nil-ошибку,
// не различая двух причин: «блока нет вовсе» и «блок есть, режим не назван». Во второй присланные
// width/rows исчезали МОЛЧА — сохранение проходило, число не доезжало ни до колонки, ни до отказа,
// и следующее чтение карточки показывало пустое поле там, где человек оставил число.
//
// ПРАВИЛО НЕ РЕТРОАКТИВНО, и это замерено, а не предположено: запрос №10 замера 2026-08-21 («ширина
// или ряды отстрочки без режима») дал НОЛЬ строк на обеих базах — и на проде `grbpwr` (126 операций
// на 7 карточках, миграция 0328 доехала), и на бете `grbpwr_beta`. Поэтому вариант строгий: отказ,
// как только присланы ширина ИЛИ ряды, без оговорки «режим прислан UNKNOWN явно».
//
// ЧЕТЫРЕ КЛЕТКИ ТОПСТИЧА, которые это правило описывает целиком (матрица шва Ф3↔Ф4):
//
//	режим пуст,      ширина пуста  -> блок не едет / пустая обёртка законна, отказа нет
//	режим 'edge',    ширина пуста  -> законно (0326: у EDGE ширина опциональна)
//	режим 'in_ditch',ширина '4'    -> отказ .topstitch_width_mm / not_applicable (уже был)
//	режим ПУСТ,      ширина '4'    -> отказ .topstitch_mode / required   <- ЭТО ПРАВИЛО
//
// Сегодня последняя клетка с живого экрана недостижима: клиентский маппер не строит обёртку при
// незаданном режиме. Она станет достижимой после клиентской половины (Ф4), и правило обязано быть
// на бэкенде РАНЬШЕ — иначе откроется окно, в котором клиент шлёт ширину без режима, а сервер её
// молча выбрасывает: та же потеря, просто перенесённая с клиента на сервер.
//
// ЧТЕНИЕ НАЗАД НЕ ЧИНИТСЯ И ЭТО ОСОЗНАННО: topstitchToPb (techcard_production.go:882-891) без
// режима возвращает nil, то есть строка «ширина без режима» не доехала бы и до клиента. При №10 = 0
// это молчание ВАКУУМНО — таких строк нет ни одной, — и трогать чтение значило бы менять проекцию
// без единой строки, которой это помогло бы. Названо здесь, чтобы ревью не приняло за дыру.
//
// МУТАЦИЯ (прогнана и откачена 2026-08-22): возврат прежнего молчаливого выхода — то есть
// объединение обеих причин обратно в одну строку
//
//	if t == nil || t.Mode == …UNKNOWN { return mode, width, rows, nil }
//
// красит ровно три подтеста — «ширина без режима», «ряды без режима» и «ширина и ряды разом»
// (kindRefusal падает с «ожидался отказ, разбор прошёл»), — и оставляет зелёными все четыре
// регрессных. Тест, который мутация не красит, не удостоверяет ничего.
func TestTopstitchWidthWithoutModeIsRefusedNotDropped(t *testing.T) {
	const (
		modeUnset   = pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_UNKNOWN
		modeEdge    = pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_EDGE
		classPlain  = pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_SS_PLAIN
		classUnset  = pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_UNKNOWN
		wantedField = "operations[0].topstitch_mode"
	)
	// Класс шва называется ЯВНО везде, где режим задан: F11 требует названного класса рядом с
	// отстрочкой, и без него зелёный/красный этого теста рассказывал бы про чужое правило.
	step := func(class pb_common.TechCardSeamClass, ts *pb_common.TechCardTopstitch) *pb_common.TechCardOperation {
		return &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock,
			SeamClass: class, Topstitch: ts,
		}
	}

	t.Run("ЦИТАТА: ширина без режима отвергается по имени поля", func(t *testing.T) {
		ve := kindRefusal(t, step(classUnset, &pb_common.TechCardTopstitch{
			Mode: modeUnset, WidthMm: &pb_decimal.Decimal{Value: "4"},
		}))
		if ve.Field != wantedField || ve.Reason != "required" {
			t.Errorf("отказ назвал %q/%q, ожидалось %s/required", ve.Field, ve.Reason, wantedField)
		}
	})
	t.Run("ЦИТАТА: ряды без режима — тот же отказ", func(t *testing.T) {
		ve := kindRefusal(t, step(classUnset, &pb_common.TechCardTopstitch{Mode: modeUnset, Rows: 2}))
		if ve.Field != wantedField || ve.Reason != "required" {
			t.Errorf("отказ назвал %q/%q, ожидалось %s/required", ve.Field, ve.Reason, wantedField)
		}
	})
	t.Run("ЦИТАТА: ширина и ряды разом — отказ один и тот же", func(t *testing.T) {
		ve := kindRefusal(t, step(classUnset, &pb_common.TechCardTopstitch{
			Mode: modeUnset, WidthMm: &pb_decimal.Decimal{Value: "4"}, Rows: 2,
		}))
		if ve.Field != wantedField || ve.Reason != "required" {
			t.Errorf("отказ назвал %q/%q, ожидалось %s/required", ve.Field, ve.Reason, wantedField)
		}
	})

	t.Run("РЕГРЕСС: блока нет вовсе — не ошибка", func(t *testing.T) {
		// Большинство базы выглядит именно так. Отказ здесь сделал бы правило ретроактивным на
		// каждой сохранённой строке разом.
		op := kindParse(t, step(classUnset, nil)).Operations[0]
		if op.TopstitchMode.Valid || op.TopstitchWidthMm.Valid || op.TopstitchRows.Valid {
			t.Errorf("серверу дописали отстрочку: %+v / %+v / %+v",
				op.TopstitchMode, op.TopstitchWidthMm, op.TopstitchRows)
		}
	})
	t.Run("РЕГРЕСС: пустая обёртка законна", func(t *testing.T) {
		// Клиент её не строит, но curl и серверный round-trip клона сезона — могут. Пустая обёртка
		// не несёт ни одного факта, терять в ней нечего, и отказ был бы отказом ни на чём.
		op := kindParse(t, step(classUnset, &pb_common.TechCardTopstitch{Mode: modeUnset})).Operations[0]
		if op.TopstitchMode.Valid || op.TopstitchWidthMm.Valid || op.TopstitchRows.Valid {
			t.Errorf("серверу дописали отстрочку: %+v / %+v / %+v",
				op.TopstitchMode, op.TopstitchWidthMm, op.TopstitchRows)
		}
	})
	t.Run("РЕГРЕСС: ОЧИЩЕННЫЙ контроль ширины — не значение", func(t *testing.T) {
		// `{value: ""}` — то, как админка шлёт стёртое децимал-поле (прецедент: material_test.go
		// строит им «cleared»). Проверка присланности обязана быть по СОДЕРЖИМОМУ, а не по
		// не-nil указателю, иначе очистка ширины начала бы требовать режим, которого нет.
		op := kindParse(t, step(classUnset, &pb_common.TechCardTopstitch{
			Mode: modeUnset, WidthMm: &pb_decimal.Decimal{Value: "  "},
		})).Operations[0]
		if op.TopstitchMode.Valid || op.TopstitchWidthMm.Valid {
			t.Errorf("пустая ширина проехала значением: %+v / %+v", op.TopstitchMode, op.TopstitchWidthMm)
		}
	})
	t.Run("РЕГРЕСС 0326: режим задан, ширина пуста — законно", func(t *testing.T) {
		op := kindParse(t, step(classPlain, &pb_common.TechCardTopstitch{Mode: modeEdge})).Operations[0]
		if op.TopstitchMode.String != "edge" || op.TopstitchWidthMm.Valid {
			t.Errorf("пара не доехала: %+v / %+v", op.TopstitchMode, op.TopstitchWidthMm)
		}
	})
	t.Run("РЕГРЕСС: режим и ширина вместе доезжают оба", func(t *testing.T) {
		op := kindParse(t, step(classPlain, &pb_common.TechCardTopstitch{
			Mode: modeEdge, WidthMm: &pb_decimal.Decimal{Value: "4"}, Rows: 2,
		})).Operations[0]
		if op.TopstitchMode.String != "edge" || !op.TopstitchWidthMm.Valid ||
			op.TopstitchWidthMm.Decimal.String() != "4" || op.TopstitchRows.Int32 != 2 {
			t.Errorf("тройка не доехала: %+v / %+v / %+v",
				op.TopstitchMode, op.TopstitchWidthMm, op.TopstitchRows)
		}
	})
}
