package dto

import (
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ЛОЖНЫЕ РАСЩЕПЛЕНИЯ, СНЯТЫЕ 0327 — ЧТО ИМЕННО ДОКАЗЫВАЕТСЯ ЗДЕСЬ.
//
// Дрейф-тесты (internal/store/migrationlint) сверяют ТРИ СПИСКА между собой и отвечают на вопрос
// «член исчез отовсюду?». Они не отвечают на второй, более важный вопрос: что произойдёт, когда
// старый бандл всё-таки пришлёт снятый номер. А он пришлёт — бэкенд едет РАНЬШЕ клиента, это
// правило, а не случайность, и до выкатки клиента пикер продолжает предлагать снятые строки.
//
// Ответ обязан быть ШУМНЫМ И АДРЕСНЫМ. «Вернулась ошибка» тут не годится: форма подсвечивает
// контрол по ПУТИ ПОЛЯ из ValidationError (см. field-errors.ts клиента), и отказ без точного пути
// вырождается в неатрибутируемый тост «что-то пошло не так». Молчаливый приём был бы хуже вдвойне —
// в колонку легло бы значение, которого нет ни в одном словаре, и CHECK отбил бы всю карточку
// голым 3819 без единого слова.
//
// НОМЕРА ЗДЕСЬ СЫРЫЕ, А НЕ ИМЕНОВАННЫЕ, и это единственный способ такое проверить: именованных
// членов больше нет вовсе, а провод несёт числа.

// Снятые 0327 номера, взятые сырыми. Каждый объявлен `reserved` в proto — и номером, и именем.
const (
	retiredHolePrepProngPierce   = pb_common.TechCardHolePrep(2)
	retiredReinforcementFusible  = pb_common.TechCardReinforcement(2)
	retiredReinforcementStay     = pb_common.TechCardReinforcement(3)
	retiredPeelModeNone          = pb_common.TechCardPeelMode(1)
	retiredZipperSeparatingCF    = pb_common.TechCardZipperApplication(6)
	retiredZipperInSeamPocket    = pb_common.TechCardZipperApplication(7)
	retiredCleaningChalk         = pb_common.TechCardCleaningKind(3)
	retiredCleaningAdhesive      = pb_common.TechCardCleaningKind(4)
	retiredCoverageFirstOutput   = pb_common.TechCardInspectCoverage(4)
	retiredPressTowardSide       = pb_common.TechCardPressToward(12)
	retiredPressActionOpenNumber = pb_common.TechCardPressAction(3)
)

// TestRetiredMembersAreClosedInTheContract — половина «цитаты»: номер закрыт НАВСЕГДА, а не просто
// пуст. Пустой номер и `reserved` в сгенерированном _name map выглядят ОДИНАКОВО — как
// отсутствующий ключ, — поэтому проверяется именно отсутствие живого члена на каждом снятом номере.
// Что там стоит `reserved`, а не обещанная дыра, сторожит поле `retired` waveVocabulary в
// enum_drift_test; здесь — что номер не занят заново.
func TestRetiredMembersAreClosedInTheContract(t *testing.T) {
	for _, tt := range []struct {
		vocab string
		names map[int32]string
		nums  []int32
	}{
		{"TechCardPressAction", pb_common.TechCardPressAction_name, []int32{3}},
		{"TechCardPressToward", pb_common.TechCardPressToward_name, []int32{12}},
		{"TechCardHolePrep", pb_common.TechCardHolePrep_name, []int32{2}},
		{"TechCardReinforcement", pb_common.TechCardReinforcement_name, []int32{2, 3}},
		{"TechCardPeelMode", pb_common.TechCardPeelMode_name, []int32{1}},
		{"TechCardZipperApplication", pb_common.TechCardZipperApplication_name, []int32{6, 7}},
		{"TechCardCleaningKind", pb_common.TechCardCleaningKind_name, []int32{3, 4}},
		{"TechCardInspectCoverage", pb_common.TechCardInspectCoverage_name, []int32{4}},
	} {
		for _, n := range tt.nums {
			if name, taken := tt.names[n]; taken {
				t.Errorf("%s: номер %d снова занят членом %s — он объявлен reserved 0327 и закрыт навсегда; отданный новому смыслу, он читался бы старым клиентом как прежний член, молча и без единой ошибки на проводе",
					tt.vocab, n, name)
			}
		}
	}
	// И ОДНО ДОБАВЛЕНИЕ: reinforcement взамен двух снятых получил `patch`. Без него сужение было бы
	// потерей факта, а не свёрткой двух написаний в одно.
	if _, ok := pb_common.TechCardReinforcement_name[7]; !ok {
		t.Error("TechCardReinforcement: номер 7 (PATCH) обязан существовать — в него 0327 переносит обе прежние подложки")
	}
}

// TestRetiredOperationKindMembersAreRefusedByField — вторая половина: провод отвергает каждый
// снятый номер, называя ПОЛЕ и причину.
func TestRetiredOperationKindMembersAreRefusedByField(t *testing.T) {
	for _, tt := range []struct {
		name  string
		op    *pb_common.TechCardOperation
		field string
	}{
		{"подготовка отверстия — прокол зубцами", &pb_common.TechCardOperation{
			OperationType: opTypeHardware, Zone: zoneOuter,
			Hardware: &pb_common.TechCardOperationHardware{
				AttachMethod: pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_PRONG_CLINCH,
				HolePrep:     retiredHolePrepProngPierce,
			}}, "operations[0].hole_prep"},
		{"усилитель — клеевая заплатка", &pb_common.TechCardOperation{
			OperationType: opTypeHardware, Zone: zoneOuter,
			Hardware: &pb_common.TechCardOperationHardware{
				AttachMethod:  pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_PRONG_CLINCH,
				Reinforcement: retiredReinforcementFusible,
			}}, "operations[0].reinforcement"},
		{"усилитель — тканевая подложка", &pb_common.TechCardOperation{
			OperationType: opTypeHardware, Zone: zoneOuter,
			Hardware: &pb_common.TechCardOperationHardware{
				AttachMethod:  pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_PRONG_CLINCH,
				Reinforcement: retiredReinforcementStay,
			}}, "operations[0].reinforcement"},
		{"съём носителя — «носителя нет»", &pb_common.TechCardOperation{
			OperationType: opTypePrint, Zone: zoneOuter,
			PrintMethod: pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_DTF,
			Print:       &pb_common.TechCardOperationPrint{PeelMode: retiredPeelModeNone},
		}, "operations[0].peel_mode"},
		{"молния — разъёмная по борту", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtZipper,
			Fastening: &pb_common.TechCardOperationFastening{ZipperApplication: retiredZipperSeparatingCF},
		}, "operations[0].zipper_application"},
		{"молния — в шве кармана", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtZipper,
			Fastening: &pb_common.TechCardOperationFastening{ZipperApplication: retiredZipperInSeamPocket},
		}, "operations[0].zipper_application"},
		{"чистка — следы мела", &pb_common.TechCardOperation{
			OperationType: opTypeClean, Zone: zoneOther,
			Clean: &pb_common.TechCardOperationClean{Kind: retiredCleaningChalk},
		}, "operations[0].cleaning_kind"},
		{"чистка — следы клея", &pb_common.TechCardOperation{
			OperationType: opTypeClean, Zone: zoneOther,
			Clean: &pb_common.TechCardOperationClean{Kind: retiredCleaningAdhesive},
		}, "operations[0].cleaning_kind"},
		{"контроль — первая единица прогона", &pb_common.TechCardOperation{
			OperationType: opTypeInspect, Zone: zoneOther,
			Inspect: &pb_common.TechCardOperationInspect{CoverageMode: retiredCoverageFirstOutput},
		}, "operations[0].coverage_mode"},
		{"направление припуска — к боку", &pb_common.TechCardOperation{
			OperationType: opTypePress, Zone: zoneOuter,
			PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON,
			Press: &pb_common.TechCardOperationPress{
				Action: pb_common.TechCardPressAction_TECH_CARD_PRESS_ACTION_TO_ONE_SIDE,
				Toward: retiredPressTowardSide,
			}}, "operations[0].press_toward"},
		{"под-глагол ВТО — разутюжить", &pb_common.TechCardOperation{
			OperationType: opTypePress, Zone: zoneOuter,
			PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON,
			Press:          &pb_common.TechCardOperationPress{Action: retiredPressActionOpenNumber},
		}, "operations[0].press_action"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ve := kindRefusal(t, tt.op)
			if ve.Field != tt.field || ve.Reason != "unknown_value" {
				t.Errorf("отказ назвал %q/%q, ожидалось %q/unknown_value", ve.Field, ve.Reason, tt.field)
			}
		})
	}
}

// TestSurvivingNeighboursStillSave — НЕГАТИВНЫЙ КОНТРОЛЬ к таблице выше, и без него она ничего не
// стоит. Тест, который проверяет только отказы, зелен и на сервере, отвергающем ВСЁ подряд. Здесь
// проходят ровно те соседи по словарю, которые обязаны были уцелеть, — включая те, чей смысл
// снятые члены и дублировали.
func TestSurvivingNeighboursStillSave(t *testing.T) {
	for _, tt := range []struct {
		name string
		op   *pb_common.TechCardOperation
		col  string
		want string
	}{
		{"отверстие не готовим — единственное написание", &pb_common.TechCardOperation{
			OperationType: opTypeHardware, Zone: zoneOuter,
			Hardware: &pb_common.TechCardOperationHardware{
				AttachMethod: pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_PRONG_CLINCH,
				HolePrep:     pb_common.TechCardHolePrep_TECH_CARD_HOLE_PREP_NONE,
			}}, "hole_prep", "none"},
		{"подложка — один член вместо двух", &pb_common.TechCardOperation{
			OperationType: opTypeHardware, Zone: zoneOuter,
			Hardware: &pb_common.TechCardOperationHardware{
				AttachMethod:  pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_PRONG_CLINCH,
				Reinforcement: pb_common.TechCardReinforcement_TECH_CARD_REINFORCEMENT_PATCH,
			}}, "reinforcement", "patch"},
		{"молния — гульфик уцелел", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtZipper,
			Fastening: &pb_common.TechCardOperationFastening{
				ZipperApplication: pb_common.TechCardZipperApplication_TECH_CARD_ZIPPER_APPLICATION_FLY,
			}}, "zipper_application", "fly"},
		{"чистка — родовой ответ уцелел", &pb_common.TechCardOperation{
			OperationType: opTypeClean, Zone: zoneOther,
			Clean: &pb_common.TechCardOperationClean{
				Kind: pb_common.TechCardCleaningKind_TECH_CARD_CLEANING_KIND_SPOT_CLEAN,
			}}, "cleaning_kind", "spot_clean"},
		{"контроль — сплошной уцелел", &pb_common.TechCardOperation{
			OperationType: opTypeInspect, Zone: zoneOther,
			Inspect: &pb_common.TechCardOperationInspect{
				CoverageMode: pb_common.TechCardInspectCoverage_TECH_CARD_INSPECT_COVERAGE_EACH_UNIT,
			}}, "coverage_mode", "each_unit"},
		{"направление — «от центра» уцелело", &pb_common.TechCardOperation{
			OperationType: opTypePress, Zone: zoneOuter,
			PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON,
			Press: &pb_common.TechCardOperationPress{
				Action: pb_common.TechCardPressAction_TECH_CARD_PRESS_ACTION_TO_ONE_SIDE,
				Toward: pb_common.TechCardPressToward_TECH_CARD_PRESS_TOWARD_AWAY_FROM_CENTER,
			}}, "press_toward", "away_from_center"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := kindFacts(kindParse(t, tt.op).Operations[0])[tt.col]; got != tt.want {
				t.Errorf("%s = %q, ожидалось %q", tt.col, got, tt.want)
			}
		})
	}
}

// TestPeelModeIsNotApplicableOnScreenPrint — F10 целиком: снятие члена `none` без этого правила
// было бы потерей, а не починкой.
//
// Раньше «носителя нет» говорилось ЗНАЧЕНИЕМ, и на шелкографии оно было истинно ОДНОВРЕМЕННО с
// «не указано» — два правдивых ответа на один вопрос, выбор наугад. Теперь это правило, и
// отвергается ровно ОДНО поле: у шелкографии, в отличие от лазера, прижим бывает, поэтому весь
// ВТО-блок шага при ней остаётся законным. Проверяется и то, и другое.
func TestPeelModeIsNotApplicableOnScreenPrint(t *testing.T) {
	screen := func(p *pb_common.TechCardOperationPrint) *pb_common.TechCardOperation {
		return &pb_common.TechCardOperation{
			OperationType: opTypePrint, Zone: zoneOuter,
			PrintMethod:       pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_SCREEN,
			Print:             p,
			PressEquipment:    pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_PRESS,
			PressTemperatureC: 160,
		}
	}
	t.Run("съём носителя на шелкографии отвергается по имени поля", func(t *testing.T) {
		ve := kindRefusal(t, screen(&pb_common.TechCardOperationPrint{
			PeelMode: pb_common.TechCardPeelMode_TECH_CARD_PEEL_MODE_HOT,
		}))
		if ve.Field != "operations[0].peel_mode" || ve.Reason != "not_applicable" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].peel_mode/not_applicable", ve.Field, ve.Reason)
		}
	})
	t.Run("прижим на шелкографии ЗАКОНЕН — отвергается ровно одно поле", func(t *testing.T) {
		ins := kindParse(t, screen(&pb_common.TechCardOperationPrint{SecondPressSec: 6}))
		f := kindFacts(ins.Operations[0])
		if f["print_method"] != "screen" || f["second_press_sec"] != "6" {
			t.Errorf("шаг шелкографии с прижимом не сохранился: %q / %q", f["print_method"], f["second_press_sec"])
		}
		if ins.Operations[0].PressTemperatureC.Int32 != 160 {
			t.Errorf("ВТО-блок шелкографии потерян: press_temperature_c = %d", ins.Operations[0].PressTemperatureC.Int32)
		}
	})
	t.Run("на плёночном методе съём носителя по-прежнему сохраняется", func(t *testing.T) {
		f := kindFacts(kindParse(t, &pb_common.TechCardOperation{
			OperationType: opTypePrint, Zone: zoneOuter,
			PrintMethod: pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_DTF,
			Print: &pb_common.TechCardOperationPrint{
				PeelMode: pb_common.TechCardPeelMode_TECH_CARD_PEEL_MODE_COLD,
			},
		}).Operations[0])
		if f["peel_mode"] != "cold" {
			t.Errorf("peel_mode = %q, ожидалось cold — правило обязано касаться ТОЛЬКО шелкографии", f["peel_mode"])
		}
	})
}

// TestRetiredKindFieldsDoNotMoveAnExistingFingerprint — §3.10 плана: существующая строка обязана
// давать ПРЕЖНИЙ отпечаток, если её содержание не менялось.
//
// Довод, почему это так: все девять снятых фактов уезжают в дайджест ХВОСТОВЫМИ ПАРАМИ «имя
// колонки, значение», а пара рождается только у ЗАПОЛНЕННОГО поля. Все девять колонок пусты у всех
// до единой сохранённых строк, значит ни одна строка их пар не эмитила и снятие байты не двигает.
//
// НО ЭТО НАДО ПОКАЗАТЬ, А НЕ ПРЕДПОЛОЖИТЬ, и показать вместе с НЕГАТИВНЫМ КОНТРОЛЕМ: тест
// «пустое поле не меняет хеш» зелен и тогда, когда хвост не эмитится ВООБЩЕ НИКОГДА, то есть когда
// заполненное поле в подпись не попадает — а это дефект куда хуже сдвига.
func TestRetiredKindFieldsDoNotMoveAnExistingFingerprint(t *testing.T) {
	blank := entity.TechCardOperation{
		OperationNumber: opGoldI32(10),
		OperationType:   entity.OpTypeMachine,
		Zone:            entity.ZoneOuter,
		MachineType:     opGoldStr("lockstitch"),
	}
	base := digestOf(constructionProjection(&entity.TechCardInsert{
		Operations: []entity.TechCardOperation{blank},
	}))
	for _, tt := range []struct {
		col string
		set func(*entity.TechCardOperation)
	}{
		{"hole_prep", func(o *entity.TechCardOperation) { o.HolePrep = opGoldStr("none") }},
		{"reinforcement", func(o *entity.TechCardOperation) { o.Reinforcement = opGoldStr("patch") }},
		{"peel_mode", func(o *entity.TechCardOperation) { o.PeelMode = opGoldStr("hot") }},
		{"zipper_application", func(o *entity.TechCardOperation) { o.ZipperApplication = opGoldStr("fly") }},
		{"cleaning_kind", func(o *entity.TechCardOperation) { o.CleaningKind = opGoldStr("spot_clean") }},
		{"coverage_mode", func(o *entity.TechCardOperation) { o.CoverageMode = opGoldStr("each_unit") }},
		{"press_action", func(o *entity.TechCardOperation) { o.PressAction = opGoldStr("press_flat") }},
		{"press_toward", func(o *entity.TechCardOperation) { o.PressToward = opGoldStr("front") }},
	} {
		t.Run(tt.col, func(t *testing.T) {
			filled := blank
			tt.set(&filled)
			got := digestOf(constructionProjection(&entity.TechCardInsert{
				Operations: []entity.TechCardOperation{filled},
			}))
			if got == base {
				t.Errorf("заполненный %s дал ТОТ ЖЕ отпечаток, что пустой шаг — значит хвостовая пара этой колонки не эмитится вовсе, и «снятие пустого поля байты не двигает» доказано пустотой", tt.col)
			}
		})
	}
}

// --- 0328: ТРИ СЛОВАРЯ НА ПРОД-ЖИВЫХ КОЛОНКАХ ---------------------------------------------------

// Снятые 0328 номера, взятые сырыми, — по тому же доводу, что у соседей выше.
const (
	retiredMachineHardwareAttach = pb_common.TechCardMachineType(14)
	retiredThreadTensionOther    = pb_common.TechCardThreadTension(4)
	retiredFusingSeamAllowance   = pb_common.TechCardPieceFusingMode(2)
)

// TestRetiredProdLiveMembersAreRefusedByField — провод отвергает каждый снятый 0328 номер, называя
// поле. Отличие от 0327 здесь не в механике, а в цене ошибки: эти три колонки живут на проде
// годами, machine_type заполнен у 92 из 105 строк, и «ноль у снимаемого токена» — ЗАМЕР, а не
// свойство схемы.
func TestRetiredProdLiveMembersAreRefusedByField(t *testing.T) {
	t.Run("машина «пришивание фурнитуры» на шаге", func(t *testing.T) {
		ve := kindRefusal(t, &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter,
			MachineType: retiredMachineHardwareAttach,
		})
		if ve.Field != "operations[0].machine_type" || ve.Reason != "unknown_value" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].machine_type/unknown_value", ve.Field, ve.Reason)
		}
	})
	t.Run("натяжение «другое» на шаге", func(t *testing.T) {
		ve := kindRefusal(t, &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock,
			ThreadTension: retiredThreadTensionOther,
		})
		if ve.Field != "operations[0].thread_tension" || ve.Reason != "unknown_value" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].thread_tension/unknown_value", ve.Field, ve.Reason)
		}
	})
}

// TestHardwareSetCanNameItsMachine — ВТОРАЯ ПОЛОВИНА F6, и без неё снятие члена было бы потерей.
//
// Пока `hardware_attach` жил в списке машин, «пришивная кнопка» была выразима двумя НЕПОЛНЫМИ
// способами: HARDWARE_SET + attach_method = sew не мог назвать машину (machine_type отвергался на
// любом не-MACHINE глаголе), а MACHINE + machine_type = hardware_attach не мог назвать способ
// (attach_method на MACHINE отвергается и сейчас). Технолог обязан был выбрать, что потерять.
// Снять член, не открыв вторую ось на глаголе, значило бы отнять вторую половину у обоих.
func TestHardwareSetCanNameItsMachine(t *testing.T) {
	t.Run("глагол называет и способ, и машину", func(t *testing.T) {
		ins := kindParse(t, &pb_common.TechCardOperation{
			OperationType: opTypeHardware, Zone: zoneOuter,
			MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BUTTON_ATTACH,
			Hardware: &pb_common.TechCardOperationHardware{
				AttachMethod: pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_SEW,
			},
		})
		op := ins.Operations[0]
		if op.MachineType.String != "button_attach" {
			t.Errorf("машина на hardware_set потеряна: %q", op.MachineType.String)
		}
		if kindFacts(op)["attach_method"] != "sew" {
			t.Errorf("способ на hardware_set потерян: %q", kindFacts(op)["attach_method"])
		}
	})
	t.Run("машина на hardware_set НЕОБЯЗАТЕЛЬНА", func(t *testing.T) {
		// Ретроактивной обязательности не возникает: сегодняшние строки hardware_set машины не
		// несут, и требовать её значило бы сделать их несохраняемыми.
		op := kindParse(t, &pb_common.TechCardOperation{
			OperationType: opTypeHardware, Zone: zoneOuter,
			Hardware: &pb_common.TechCardOperationHardware{
				AttachMethod: pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_SEW,
			},
		}).Operations[0]
		if op.MachineType.Valid {
			t.Errorf("серверу дописали машину, которую никто не называл: %q", op.MachineType.String)
		}
	})
	t.Run("СТРОЧКА на hardware_set по-прежнему отвергается", func(t *testing.T) {
		// Граница осталась на месте: открыт ровно machine_type, а не весь машинный блок. Нитки и
		// иглы описывают СТРОЧКУ, которой у шага установки фурнитуры как факта карточки нет.
		ve := kindRefusal(t, &pb_common.TechCardOperation{
			OperationType: opTypeHardware, Zone: zoneOuter,
			MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BUTTON_ATTACH,
			ThreadCount: 2,
			Hardware: &pb_common.TechCardOperationHardware{
				AttachMethod: pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_SEW,
			},
		})
		if ve.Field != "operations[0].thread_count" || ve.Reason != "not_applicable" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].thread_count/not_applicable", ve.Field, ve.Reason)
		}
	})
	t.Run("на прочих глаголах машина по-прежнему отвергается", func(t *testing.T) {
		ve := kindRefusal(t, &pb_common.TechCardOperation{
			OperationType: opTypeClean, Zone: zoneOther,
			MachineType: mtOverlock,
			Clean:       &pb_common.TechCardOperationClean{Kind: pb_common.TechCardCleaningKind_TECH_CARD_CLEANING_KIND_SPOT_CLEAN},
		})
		if ve.Field != "operations[0].machine_type" || ve.Reason != "not_applicable" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].machine_type/not_applicable", ve.Field, ve.Reason)
		}
	})
}

// TestRetiredFusingSeamAllowanceIsRefusedByField — F8 на проводе. Путь поля здесь `pieces[N]`, а не
// `operations[N]`, и это существенно: подсветить контрол формы можно только по нему.
func TestRetiredFusingSeamAllowanceIsRefusedByField(t *testing.T) {
	if _, ok := pb_common.TechCardPieceFusingMode_name[2]; ok {
		t.Fatal("номер 2 снова занят членом enum'а — он объявлен reserved 0328 и закрыт навсегда")
	}
	card := kindCard()
	card.Pieces = []*pb_common.TechCardPiece{{
		Name: "полочка", LineKey: "FRONT", PiecesPerGarment: 1,
		Fused:      true,
		FusingMode: pbPtr(retiredFusingSeamAllowance),
	}}
	_, err := ConvertPbTechCardInsertToEntity(card)
	if err == nil {
		t.Fatal("снятый режим дублирования принят — в колонку легло бы значение, которого нет в словаре, и CHECK отбил бы всю карточку голым 3819")
	}
	var ve *entity.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("отказ не назвал поле вовсе: %v", err)
	}
	if ve.Field != "pieces[0].fusing_mode" {
		t.Errorf("отказ назвал %q, ожидалось pieces[0].fusing_mode", ve.Field)
	}
}
