package dto

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// КРУГ КЛАССА ШВА — ТОТАЛЬНО ПО СЛОВАРЮ, А НЕ ПО СПИСКУ, НАПИСАННОМУ РУКАМИ.
//
// ЗАЧЕМ ЗАВЕДЁН. Замер 2026-08-23: строк с любым классом `ls_*` в базе НОЛЬ на обеих контурах, а
// экран ратификации операций (R7) второй кнопкой главного яруса пишет `ls_lapped` — то есть первую
// такую запись за всю жизнь базы, и сразу на проде. Слово `ls_lapped` при этом не встречалось НИ В
// ОДНОМ тесте: путь «провод → токен → провод» держался на чтении кода и на CHECK'е в схеме, но не
// был закрыт ничем, что покраснеет при поломке.
//
// ПОЧЕМУ ЦИКЛ ПО `entity.SeamClassTokens`, А НЕ ОДИН СЛУЧАЙ. Проверка одного члена доказывает
// ровно один член. Список же — тот самый, из которого собираются обе карты, поэтому цикл по нему
// ловит и будущего члена, добавленного в словарь и забытого в proto: `enumTokenMap` паникует при
// рассинхроне на init, но паника видна только тому, кто запустил хоть один тест этого пакета.
func TestSeamClassRoundTripsForEveryToken(t *testing.T) {
	if len(entity.SeamClassTokens) == 0 {
		t.Fatal("словарь классов шва пуст — цикл ниже не проверил бы ничего")
	}
	for _, token := range entity.SeamClassTokens {
		name := "TECH_CARD_SEAM_CLASS_" + strings.ToUpper(token)
		v, ok := pb_common.TechCardSeamClass_value[name]
		if !ok {
			t.Errorf("токен %q не имеет члена proto %s", token, name)
			continue
		}
		pb := pb_common.TechCardSeamClass(v)

		got, err := parseSeamClass(pb, "test.seam_class")
		if err != nil {
			t.Errorf("%s: разбор отказал: %v", token, err)
			continue
		}
		if !got.Valid || got.String != token {
			t.Errorf("%s: разбор дал %+v, ожидался непустой %q", token, got, token)
			continue
		}
		if back := seamClassTokenToPb[token]; back != pb {
			t.Errorf("%s: обратная карта дала %v, ожидалось %v — класс шва уехал бы на провод "+
				"другим членом, чем приехал", token, back, pb)
		}
	}
	t.Logf("круг замкнулся на %d классах шва, среди них %q", len(entity.SeamClassTokens), "ls_lapped")
}

// ОТДЕЛЬНО ПРО `ls_lapped` — ИМЕННО ЕГО ПИШЕТ ВТОРАЯ КНОПКА ЭКРАНА РАТИФИКАЦИИ.
//
// Тест назван по нему нарочно: когда он покраснеет, из имени будет видно, ЧТО именно сломалось на
// проде, без чтения тела. Здесь же проверяется, что оба правила класса шва его НЕ ЗАДЕВАЮТ —
// то есть шаг с этим классом и без режима отстрочки сохраняем.
func TestLappedSeamClassIsAcceptedAndUntouchedByTopstitchRules(t *testing.T) {
	const token = "ls_lapped"
	if !entity.ValidSeamClasses[entity.TechCardSeamClass(token)] {
		t.Fatalf("%s не входит в entity.ValidSeamClasses — сервер отверг бы запись экрана", token)
	}
	pb := pb_common.TechCardSeamClass(pb_common.TechCardSeamClass_value["TECH_CARD_SEAM_CLASS_LS_LAPPED"])
	got, err := parseSeamClass(pb, "test.seam_class")
	if err != nil || !got.Valid || got.String != token {
		t.Fatalf("разбор %s дал (%+v, %v), ожидался непустой %q", token, got, err, token)
	}
	// Первое правило пары стоит на `os_topstitch`, второе — на непустом topstitch_mode. Ни одно из
	// них не должно срабатывать здесь: шаг внакрой — обычный соединительный шов, и требовать у него
	// «где лежит отделочная строчка» значило бы запереть ровно тот жест, ради которого R7 и сделан.
	if got.String == string(entity.SeamClassTopstitch) {
		t.Fatalf("%s совпал с классом отстрочки — правило пары заперло бы запись экрана", token)
	}
}
