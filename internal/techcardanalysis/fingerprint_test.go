package techcardanalysis

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ── КАНОНИЧЕСКИЙ ВЕКТОР ОТПЕЧАТКА (design §9) ───────────────────────────────────────────────────
//
// ЭТИ ЧИСЛА — КОНТРАКТ, А НЕ СНИМОК ПОВЕДЕНИЯ. Тот же алгоритм будет переписан на TypeScript (T21) и
// сверен с ЭТИМИ ЖЕ значениями: клиент считает отпечаток по форме (o.outputUnitKey + o.inputKeys),
// сервер — по entity после гидрации, и разойтись им нельзя ни в одном байте, иначе каждая карточка
// беты покроется ложным амбером «эта операция изменилась с момента прогона».
//
// Отсюда правило: значение НИКОГДА не «обновляется под новый вывод». Разошёлся тест — сломан
// алгоритм (или сознательно меняется версия payload, и тогда меняется префикс tcfp1).
//
// Значения посчитаны независимо от этого кода:
//
//	printf 'tcfp1\x00Back\x00Back panels bottom\x00Back Panels Upper' | shasum -a 256 | cut -c1-8
var fingerprintVectors = []struct {
	name   string
	out    string
	inputs []string
	want   string
}{
	{
		// ДЖОЙН: два входа-узла. Он же оп 30 карточки 8 — см. TestFingerprintsOfCard8 ниже.
		name:   "join with two unit inputs",
		out:    "Back",
		inputs: []string{"Back panels bottom", "Back Panels Upper"},
		want:   "1bd85c4d",
	},
	{
		// ОБРАБОТКА: пустой выход сериализуется ПУСТОЙ СТРОКОЙ, а не пропускается.
		name:   "processing step: empty output",
		out:    "",
		inputs: []string{"blazer"},
		want:   "5c14ea94",
	},
	{
		// ПОГЛОЩЕНИЕ: собственный ключ шага стоит и на выходе, и среди входов; ULID детали идёт
		// сырым, без префикса вида — пространство имён едино (правило 6).
		name:   "absorbing step: piece ULID plus its own unit key",
		out:    "pocket base",
		inputs: []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "pocket base"},
		want:   "9bd4f24a",
	},
	{
		// Вырожденный шаг: ни выхода, ни входов. Payload не пуст — он «tcfp1» и один разделитель.
		name:   "no output and no inputs",
		out:    "",
		inputs: nil,
		want:   "2b1f7919",
	},
	{
		// UTF-8 и регистр: кириллица уезжает байтами, «Плечевая» ≠ «плечевая».
		name:   "utf-8 keys, case preserved",
		out:    "Плечевая",
		inputs: []string{"плечевая"},
		want:   "46e2eab4",
	},
	{
		// Пробел внутри ключа — часть ключа. Тримминг здесь дал бы клиенту другой отпечаток.
		name:   "leading space is part of the key",
		out:    "Base",
		inputs: []string{" x"},
		want:   "4e795fdf",
	},
	{
		name:   "same step without the space is a different fingerprint",
		out:    "Base",
		inputs: []string{"x"},
		want:   "dc4d7be7",
	},
	{
		// Регистр выходного ключа различает узлы: «Base» (оп 270) и «base» (оп 450) — разные узлы,
		// и на этом стоит проверка A1.
		name:   "output key case matters",
		out:    "base",
		inputs: []string{" x"},
		want:   "e31589b1",
	},
	{
		// Порядок входов — факт шага. Перестановка входов вектора 1 даёт ДРУГОЙ отпечаток; всякая
		// сортировка «для стабильности» стёрла бы это различие.
		name:   "input order is not sorted away",
		out:    "Back",
		inputs: []string{"Back Panels Upper", "Back panels bottom"},
		want:   "75569dd3",
	},
}

func TestOperationFingerprintCanonicalVectors(t *testing.T) {
	for _, tc := range fingerprintVectors {
		t.Run(tc.name, func(t *testing.T) {
			if got := OperationFingerprint(tc.out, tc.inputs); got != tc.want {
				t.Fatalf("OperationFingerprint(%q, %q) = %q, want %q — отпечаток это КОНТРАКТ с "+
					"TS-портом (§9), а не снимок вывода: значение не обновляют под новый результат",
					tc.out, tc.inputs, got, tc.want)
			}
		})
	}
}

// TestFingerprintVectorsAreDistinct guards the vectors themselves: a table where two rows
// accidentally share an expectation would pass while proving nothing about the pairs it exists to
// separate (регистр, пробел, порядок).
func TestFingerprintVectorsAreDistinct(t *testing.T) {
	seen := make(map[string]string, len(fingerprintVectors))
	for _, tc := range fingerprintVectors {
		if prev, dup := seen[tc.want]; dup {
			t.Fatalf("векторы %q и %q ждут один отпечаток %s — таблица перестала различать то, "+
				"ради чего заведена", prev, tc.name, tc.want)
		}
		seen[tc.want] = tc.name
	}
}

func TestFingerprintsOfCard8(t *testing.T) {
	fps := Fingerprints(card8())

	if len(fps) != len(card8Ops) {
		t.Fatalf("отпечатков %d, операций %d — карта отпечатков обязана покрывать все шаги с номером",
			len(fps), len(card8Ops))
	}

	// Оп 30 — тот же шаг, что канонический вектор №1: фикстура и вектор сходятся в одной точке, и
	// расхождение между ними становится видимым сразу, а не через голден T5.
	if got, want := fps[30], "1bd85c4d"; got != want {
		t.Errorf("fp(op 30) = %q, want %q (канонический вектор «join with two unit inputs»)", got, want)
	}
	// Оп 470 — вектор №2.
	if got, want := fps[470], "5c14ea94"; got != want {
		t.Errorf("fp(op 470) = %q, want %q (канонический вектор «processing step»)", got, want)
	}

	// 470 и 480 побайтно одинаковы как ДАННЫЕ шага (обработка блейзера без выхода), и отпечаток у
	// них один. Это не дефект: отпечаток удостоверяет СОДЕРЖИМОЕ шага, а не его личность —
	// личность несёт operation_number. Клиенту это важно знать: совпадение отпечатков двух разных
	// номеров ничего не значит.
	if fps[470] != fps[480] {
		t.Errorf("fp(470)=%q fp(480)=%q: шаги побайтно одинаковы, отпечаток обязан совпасть",
			fps[470], fps[480])
	}

	// А вот 270 и 450 различаются ТОЛЬКО регистром выходного ключа («Base» против «base») при
	// разных входах — и это должно быть видно.
	if fps[270] == fps[450] {
		t.Errorf("fp(270) == fp(450) == %q: разные шаги получили один отпечаток", fps[270])
	}

	for _, op := range card8Ops {
		if fps[op.num] == "" {
			t.Errorf("у операции #%d нет отпечатка", op.num)
		}
		if len(fps[op.num]) != 8 {
			t.Errorf("fp(op %d) = %q: длина %d, ожидается 8 hex-символов",
				op.num, fps[op.num], len(fps[op.num]))
		}
	}
}

// TestFingerprintsSkipUnnumberedOperation pins the one degradation the function is allowed: a
// legacy row with no operation_number has no anchor to be filed under, so it is skipped rather than
// crowding the map onto key 0 (где оно затёрло бы соседа).
func TestFingerprintsSkipUnnumberedOperation(t *testing.T) {
	c := card8()
	c.Operations = append(c.Operations, entity.TechCardOperation{
		OperationNumber: sql.NullInt32{}, // легаси-строка без номера
		OperationType:   "machine",
		Zone:            "other",
		AssemblyInputs:  []entity.OperationInput{{Kind: entity.AssemblyInputUnit, Key: "blazer"}},
	})
	fps := Fingerprints(c)
	if len(fps) != len(card8Ops) {
		t.Fatalf("отпечатков %d, ожидается %d: строка без номера не должна попадать в карту",
			len(fps), len(card8Ops))
	}
	if _, has := fps[0]; has {
		t.Fatal("строка без номера легла под ключ 0 — там она затрёт соседа при второй такой строке")
	}
}

// TestFingerprintsAreStableAcrossRuns is the property the whole mechanism rests on: the same card
// hashes the same way twice. Карта строится обходом среза, а не карты, поэтому случайный порядок
// итерации в неё не протекает — но проверить это дешевле, чем однажды объяснять плавающий амбер.
func TestFingerprintsAreStableAcrossRuns(t *testing.T) {
	a, b := Fingerprints(card8()), Fingerprints(card8())
	for num, fp := range a {
		if b[num] != fp {
			t.Fatalf("fp(op %d) плавает между прогонами: %q против %q", num, fp, b[num])
		}
	}
}

// TestFingerprintReadsCanonicalInputs pins WHICH field the payload comes from. AssemblyInputs —
// каноническая форма входа (её кладёт гидрация и на ней стоит вся запись); InputKeys — сырое эхо
// провода, которое НЕ персистится. Считать по второму значило бы хешировать на записи одно, а на
// чтении другое — и подпись рождалась бы протухшей молча.
func TestFingerprintReadsCanonicalInputs(t *testing.T) {
	c := card8()
	op := card8OpByNumber(c, 30)
	before := Fingerprints(c)[30]

	// Сырое эхо расходится с канонической формой — отпечаток обязан не заметить.
	op.InputKeys = []string{"нечто совсем другое"}
	if got := Fingerprints(c)[30]; got != before {
		t.Fatalf("правка InputKeys сдвинула отпечаток (%q → %q): payload обязан читаться из "+
			"AssemblyInputs", before, got)
	}

	// А правка канонической формы — обязана заметить.
	op.AssemblyInputs[0].Key = "Back panels bottom "
	if got := Fingerprints(c)[30]; got == before {
		t.Fatal("пробел в конце ключа не сдвинул отпечаток — где-то появился тримминг")
	}
}
