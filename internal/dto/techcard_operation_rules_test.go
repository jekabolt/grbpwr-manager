package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ТРИ ПРАВИЛА БЕЗ ЕДИНОГО СНЯТОГО ЧЛЕНА (F7, F11, F22).
//
// Здесь чинится не словарь, а ВАЛИДАЦИЯ: во всех трёх находках члены остаются, а дефект в том, что
// два поля говорят об одном факте и НЕ СВЯЗАНЫ ничем. Пока связи нет, законны все комбинации,
// включая противоречивые, — и противоречие уезжает в подпись, в релизный снапшот и на печатный лист
// молча, потому что каждое поле по отдельности выглядит правдоподобно.
//
// У каждого правила проверяется ЧЕТЫРЁХ клеток минимум: незаконное отвергается С ИМЕНЕМ ПОЛЯ,
// законное проходит, соседняя законная комбинация НЕ задета, и старая сохранённая форма
// пере-сохраняется без отказа. Тест только на отказы был бы зелен и на сервере, отвергающем всё.

const mtDoubleNeedle = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH_DOUBLE_NEEDLE

// --- F7: двухигольная машина и число игл --------------------------------------------------------

// TestTwinNeedleMachineDemandsTwoNeedles.
//
// «Двухигольная прямострочная» и `needle_count = 2` — ОДИН факт, сказанный дважды: после волны 0324
// у шага появилось число игл, и двухигольность стала выразима им. Связи между токеном и числом не
// было ни на одной стороне — ни в Go, ни в zod клиента, — значит законна была и пара «двухигольная
// машина, игл 1», то есть прямое противоречие в одной строке.
//
// ЧЛЕН СЛОВАРЯ НЕ СНЯТ, и это решение, а не недоделка: он ЕДИНСТВЕННАЯ ЦЕЛЬ канонизации
// замороженного легаси-глагола `double_needle`, и снятие сломало бы чтение старых строк. Из пикера
// он уходит клиентской половиной волны; здесь он перестаёт быть противоречивым.
func TestTwinNeedleMachineDemandsTwoNeedles(t *testing.T) {
	twin := func(count int32) *pb_common.TechCardOperation {
		op := &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtDoubleNeedle,
		}
		if count != 0 {
			op.Stitching = &pb_common.TechCardOperationStitching{NeedleCount: count}
		}
		return op
	}

	t.Run("двухигольная и одна игла — прямое противоречие", func(t *testing.T) {
		ve := kindRefusal(t, twin(1))
		if ve.Field != "operations[0].needle_count" || ve.Reason != "conflicts_with_machine_type" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].needle_count/conflicts_with_machine_type", ve.Field, ve.Reason)
		}
	})
	t.Run("двухигольная и четыре иглы — то же самое", func(t *testing.T) {
		ve := kindRefusal(t, twin(4))
		if ve.Field != "operations[0].needle_count" || ve.Reason != "conflicts_with_machine_type" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].needle_count/conflicts_with_machine_type", ve.Field, ve.Reason)
		}
	})
	t.Run("двухигольная без числа — число обязательно", func(t *testing.T) {
		ve := kindRefusal(t, twin(0))
		if ve.Field != "operations[0].needle_count" || ve.Reason != "required" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].needle_count/required", ve.Field, ve.Reason)
		}
	})
	t.Run("двухигольная и две иглы — законно", func(t *testing.T) {
		if got := kindFacts(kindParse(t, twin(2)).Operations[0])["needle_count"]; got != "2" {
			t.Errorf("needle_count = %q, ожидалось 2", got)
		}
	})
	t.Run("ЛЕГАСИ-ГЛАГОЛ ОСВОБОЖДЁН ОТ ТРЕБОВАНИЯ", func(t *testing.T) {
		// Бандл, присылающий `double_needle` ГЛАГОЛОМ, поля needle_count не знает вовсе — оно
		// родилось волной 0324, — и требовать от него число значило бы превратить компат-путь в
		// стену: канонизация старой строки перестала бы сохраняться. Именно ради этого пути член
		// словаря и оставлен живым, так что правило, ломающее путь, отменило бы собственный довод.
		ins := kindParse(t, &pb_common.TechCardOperation{
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_DOUBLE_NEEDLE,
			Zone:          zoneOuter,
		})
		op := ins.Operations[0]
		if op.MachineType.String != "lockstitch_double_needle" {
			t.Fatalf("легаси-глагол канонизировался в %q — цель канонизации потеряна", op.MachineType.String)
		}
		if op.NeedleCount.Valid {
			t.Errorf("серверу дописали число игл, которого никто не называл: %d", op.NeedleCount.Int32)
		}
	})
	t.Run("но противоречие отвергается и на легаси-пути", func(t *testing.T) {
		// Число, спорящее с машиной, мог прислать только тот, кто про поле знает, — и ему отвечают
		// так же, как всем.
		ve := kindRefusal(t, &pb_common.TechCardOperation{
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_DOUBLE_NEEDLE,
			Zone:          zoneOuter,
			Stitching:     &pb_common.TechCardOperationStitching{NeedleCount: 1},
		})
		if ve.Field != "operations[0].needle_count" || ve.Reason != "conflicts_with_machine_type" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].needle_count/conflicts_with_machine_type", ve.Field, ve.Reason)
		}
	})
	t.Run("обычная прямострочка с двумя иглами не задета", func(t *testing.T) {
		// Правило висит на МАШИНЕ, а не на числе: «прямострочка, игл 2» — законная запись того же
		// факта, и запрещать её значило бы решать за технолога, каким из двух способов он говорит.
		if got := kindFacts(kindParse(t, &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter,
			MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			Stitching:   &pb_common.TechCardOperationStitching{NeedleCount: 2},
		}).Operations[0])["needle_count"]; got != "2" {
			t.Errorf("needle_count = %q, ожидалось 2", got)
		}
	})
}

// --- F11: класс шва и блок отстрочки ------------------------------------------------------------

// TestTopstitchAndSeamClassMustAgree.
//
// ЕДИНСТВЕННАЯ ИЗ ДВАДЦАТИ ЧЕТЫРЁХ НАХОДОК, ГДЕ ОБЕ ПОЛОВИНЫ ЗАПОЛНЕНЫ В ЖИВОЙ БАЗЕ: все строки
// прода с seam_class — это ровно те же строки с topstitch_mode, каждая говорит «это отстрочка»
// дважды. Правила, связывающего их, не было ни на одной стороне.
//
// Вторая половина правила важнее первой. Неназванный seam_class НАСЛЕДУЕТ умолчание карточки, а
// оно на проде `ss_plain`. Значит чисто декоративная отстрочка, у которой заполнили только блок,
// МОЛЧА объявлялась стачным швом — и уезжала такой в подпись и на печатный лист.
func TestTopstitchAndSeamClassMustAgree(t *testing.T) {
	step := func(class pb_common.TechCardSeamClass, mode pb_common.TechCardTopstitchMode) *pb_common.TechCardOperation {
		op := &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock,
			SeamClass: class,
		}
		if mode != pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_UNKNOWN {
			op.Topstitch = &pb_common.TechCardTopstitch{Mode: mode}
		}
		return op
	}
	const (
		classUnset     = pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_UNKNOWN
		classTopstitch = pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_OS_TOPSTITCH
		classPlain     = pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_SS_PLAIN
		modeUnset      = pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_UNKNOWN
		modeEdge       = pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_EDGE
	)

	t.Run("отделочный класс без режима отвергается", func(t *testing.T) {
		ve := kindRefusal(t, step(classTopstitch, modeUnset))
		if ve.Field != "operations[0].topstitch_mode" || ve.Reason != "required" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].topstitch_mode/required", ve.Field, ve.Reason)
		}
	})
	t.Run("режим при НЕНАЗВАННОМ классе отвергается", func(t *testing.T) {
		ve := kindRefusal(t, step(classUnset, modeEdge))
		if ve.Field != "operations[0].seam_class" || ve.Reason != "required" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].seam_class/required", ve.Field, ve.Reason)
		}
	})
	t.Run("отделочный класс с режимом — законно", func(t *testing.T) {
		op := kindParse(t, step(classTopstitch, modeEdge)).Operations[0]
		if op.SeamClass.String != "os_topstitch" || op.TopstitchMode.String != "edge" {
			t.Errorf("пара не доехала: %q / %q", op.SeamClass.String, op.TopstitchMode.String)
		}
	})
	t.Run("НАЗВАННЫЙ соединительный класс с отстрочкой — законно", func(t *testing.T) {
		// Отстроченный стачной шов — обычная работа, и правило не имеет права его запрещать.
		// Запрещается ровно НЕНАЗВАННЫЙ класс, потому что там ответ подставляет карточка.
		op := kindParse(t, step(classPlain, modeEdge)).Operations[0]
		if op.SeamClass.String != "ss_plain" || op.TopstitchMode.String != "edge" {
			t.Errorf("пара не доехала: %q / %q", op.SeamClass.String, op.TopstitchMode.String)
		}
	})
	t.Run("шаг без обеих половин не задет", func(t *testing.T) {
		// Сегодняшняя строка без класса и без отстрочки — большинство базы. Она обязана
		// сохраняться ровно как раньше, иначе правило стало бы ретроактивным.
		op := kindParse(t, step(classUnset, modeUnset)).Operations[0]
		if op.SeamClass.Valid || op.TopstitchMode.Valid {
			t.Errorf("серверу дописали ответ: %+v / %+v", op.SeamClass, op.TopstitchMode)
		}
	})
	t.Run("КРУГ: сохранённая законная строка пере-сохраняется без отказа", func(t *testing.T) {
		// Живые строки прода выглядят именно так — класс `os_topstitch` плюс режим. Правило обязано
		// пропускать их пере-сохранение, иначе владелец не смог бы отредактировать ни одну из них.
		first := kindParse(t, step(classTopstitch, modeEdge))
		if _, err := ConvertPbTechCardInsertToEntity(kindEmit(t, first)); err != nil {
			t.Fatalf("круг pb → entity → pb → entity отказал: %v", err)
		}
	})
}

// --- F22: окантовка ------------------------------------------------------------------------------

// TestBindingStyleFollowsTheSeamClass.
//
// Один приём описывали ЧЕТЫРЕ поля: seam_class = bs_bound, machine_type = binding_taping,
// attachment_kind = binder и binding_style. Собственный факт несёт только последнее — первые три
// говорят «это окантовка» трижды. Ни одна пара не была связана правилом, зато binding_style был
// привязан к САМОМУ СЛАБОМУ из трёх — к машинке. Отсюда следовало, что окантовка на прямострочке
// (кант притачан вручную — обычная работа) своё исполнение назвать НЕ МОГЛА, а бессмысленная пара
// «стачной шов на окантовочной машине с окантовывателем» оставалась законной.
func TestBindingStyleFollowsTheSeamClass(t *testing.T) {
	step := func(class pb_common.TechCardSeamClass, machine pb_common.TechCardMachineType) *pb_common.TechCardOperation {
		return &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneHem, MachineType: machine,
			SeamClass: class,
			Stitching: &pb_common.TechCardOperationStitching{
				BindingStyle: pb_common.TechCardBindingStyle_TECH_CARD_BINDING_STYLE_SINGLE_FOLD,
			},
		}
	}
	const (
		classBound = pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_BS_BOUND
		classPlain = pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_SS_PLAIN
		classUnset = pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_UNKNOWN
	)

	t.Run("ОКАНТОВКА НА ПРЯМОСТРОЧКЕ ТЕПЕРЬ МОЖЕТ НАЗВАТЬ ИСПОЛНЕНИЕ", func(t *testing.T) {
		// Ровно то, что было невозможно до F22, и ровно та комбинация, которую аудит назвал
		// осмысленной: кант притачан вручную, машинка обычная.
		if got := kindFacts(kindParse(t, step(classBound,
			pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH)).Operations[0])["binding_style"]; got != "single_fold" {
			t.Errorf("binding_style = %q, ожидалось single_fold", got)
		}
	})
	t.Run("на окантовочной машине — по-прежнему законно", func(t *testing.T) {
		if got := kindFacts(kindParse(t, step(classBound, mtBinding)).Operations[0])["binding_style"]; got != "single_fold" {
			t.Errorf("binding_style = %q, ожидалось single_fold", got)
		}
	})
	t.Run("на НЕ окантовочном классе отвергается по имени поля", func(t *testing.T) {
		ve := kindRefusal(t, step(classPlain, mtBinding))
		if ve.Field != "operations[0].binding_style" || ve.Reason != "needs_seam_class" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].binding_style/needs_seam_class", ve.Field, ve.Reason)
		}
	})
	t.Run("при НЕНАЗВАННОМ классе отвергается тоже", func(t *testing.T) {
		// Неназванный класс наследует умолчание карточки, то есть ответ подставил бы кто-то другой
		// — тот же довод, что у F11.
		ve := kindRefusal(t, step(classUnset, mtBinding))
		if ve.Field != "operations[0].binding_style" || ve.Reason != "needs_seam_class" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].binding_style/needs_seam_class", ve.Field, ve.Reason)
		}
	})
	t.Run("окантовочная машина БЕЗ исполнения — законна", func(t *testing.T) {
		// machine_type и attachment_kind перестали быть обязательными спутниками, а не стали
		// запрещёнными: «шьём на окантовочной, как сложена бейка — не сказано» это ответ.
		op := kindParse(t, &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneHem, MachineType: mtBinding,
		}).Operations[0]
		if op.BindingStyle.Valid {
			t.Errorf("серверу дописали исполнение бейки: %q", op.BindingStyle.String)
		}
	})
	t.Run("ведущий член словаря найден ПО ИМЕНИ и обязан существовать", func(t *testing.T) {
		// Правило сравнивает с константой, а константа — с живым словарём. Правило, сравнивающее с
		// голым литералом, не ломает сборку при опечатке: оно просто НИКОГДА не совпадает и
		// перестаёт существовать молча.
		if !entity.ValidSeamClasses[entity.SeamClassBound] {
			t.Error("entity.SeamClassBound не член словаря классов шва — правило окантовки матчит ничто")
		}
		if !entity.ValidSeamClasses[entity.SeamClassTopstitch] {
			t.Error("entity.SeamClassTopstitch не член словаря классов шва — правило отстрочки матчит ничто")
		}
	})
}
