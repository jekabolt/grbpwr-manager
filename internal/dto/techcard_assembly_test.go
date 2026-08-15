package dto

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func asmPieces(keys ...string) []entity.TechCardPiece {
	out := make([]entity.TechCardPiece, 0, len(keys))
	for _, k := range keys {
		out = append(out, entity.TechCardPiece{LineKey: k, Name: "деталь " + k})
	}
	return out
}

func asmOp(inputs []string, out string) entity.TechCardOperation {
	return entity.TechCardOperation{
		InputKeys:     inputs,
		OutputUnitKey: nullStringFromPb(out),
	}
}

// TestAssemblyCanonicalizeLeavesUnmarkedCardUntouched — состояние КАЖДОЙ сегодняшней карточки.
// Если проход тронет хоть одно поле, регресс получит вся база разом.
func TestAssemblyCanonicalizeLeavesUnmarkedCardUntouched(t *testing.T) {
	ops := []entity.TechCardOperation{
		{PieceLineKeys: []string{"FR", "BK"}},
		{PieceLineKeys: []string{"FR"}}, // деталь в двух шагах — сегодня законно
		{},
	}
	before := make([]entity.TechCardOperation, len(ops))
	copy(before, ops)

	if verr := canonicalizeAssembly(ops, asmPieces("FR", "BK")); verr != nil {
		t.Fatalf("карточка без узлов обязана проходить вакуумно, получено: %v", verr)
	}
	if !reflect.DeepEqual(ops, before) {
		t.Errorf("проход тронул неразмеченную карточку:\n было %+v\n стало %+v", before, ops)
	}
	for i := range ops {
		if ops[i].AssemblyInputs != nil {
			t.Errorf("шаг %d: AssemblyInputs заполнен на неразмеченной карточке", i)
		}
	}
}

// TestAssemblyCanonicalizeProjection — PieceLineKeys обязан стать ПРОЕКЦИЕЙ объединения, а не
// остаться вторым мнением: он стоит позицией 4 в кортеже дайджеста CONSTRUCTION.
func TestAssemblyCanonicalizeProjection(t *testing.T) {
	ops := []entity.TechCardOperation{
		asmOp([]string{"FR", "BK"}, "SHELL"),
		asmOp([]string{"SHELL", "SL"}, "SHELL"),
	}
	// Клиент прислал устаревшую проекцию — её обязано перезаписать, а не доверить ей.
	ops[0].PieceLineKeys = []string{"МУСОР"}
	ops[1].PieceLineKeys = nil

	if verr := canonicalizeAssembly(ops, asmPieces("FR", "BK", "SL")); verr != nil {
		t.Fatalf("валидная разметка отвергнута: %v", verr)
	}
	if got, want := ops[0].PieceLineKeys, []string{"FR", "BK"}; !reflect.DeepEqual(got, want) {
		t.Errorf("шаг 0 проекция деталей: %v, ожидалась %v", got, want)
	}
	if got, want := ops[1].PieceLineKeys, []string{"SL"}; !reflect.DeepEqual(got, want) {
		t.Errorf("шаг 1 проекция деталей: %v, ожидалась %v (узел SHELL в неё не входит)", got, want)
	}
	if got, want := ops[1].InputKeys, []string{"SHELL", "SL"}; !reflect.DeepEqual(got, want) {
		t.Errorf("шаг 1 объединение: %v, ожидалось %v", got, want)
	}
	if ops[1].AssemblyInputs[0].Kind != entity.AssemblyInputUnit {
		t.Error("шаг 1 вход 0 обязан быть классифицирован как УЗЕЛ")
	}
	if ops[1].AssemblyInputs[1].Kind != entity.AssemblyInputPiece {
		t.Error("шаг 1 вход 1 обязан быть классифицирован как ДЕТАЛЬ")
	}
}

// TestAssemblyCanonicalizeEmptyInputsStayNil — nil, а не пустой срез: json.Marshal их различает,
// и дайджест записи разошёлся бы с дайджестом чтения навсегда.
func TestAssemblyCanonicalizeEmptyInputsStayNil(t *testing.T) {
	ops := []entity.TechCardOperation{
		asmOp([]string{"FR", "BK"}, "SHELL"),
		asmOp(nil, ""), // обработка без входов — законна
	}
	if verr := canonicalizeAssembly(ops, asmPieces("FR", "BK")); verr != nil {
		t.Fatalf("отвергнуто: %v", verr)
	}
	if ops[1].InputKeys != nil {
		t.Errorf("шаг без входов: InputKeys = %#v, ожидался nil", ops[1].InputKeys)
	}
	if ops[1].PieceLineKeys != nil {
		t.Errorf("шаг без входов: PieceLineKeys = %#v, ожидался nil", ops[1].PieceLineKeys)
	}
}

// TestAssemblyCanonicalizeUnitNameOnFirstProducer — имя узла живёт на первом производителе.
// Иначе удаление или перестановка первого шага молча теряют имя, набранное один раз.
func TestAssemblyCanonicalizeUnitNameOnFirstProducer(t *testing.T) {
	ops := []entity.TechCardOperation{
		asmOp([]string{"FR", "BK"}, "SHELL"),           // имени не дал
		asmOp([]string{"SHELL", "SL"}, "SHELL"),        // имя дал поглощающий
	}
	ops[1].OutputUnitName = nullStringFromPb("корпус")

	if verr := canonicalizeAssembly(ops, asmPieces("FR", "BK", "SL")); verr != nil {
		t.Fatalf("отвергнуто: %v", verr)
	}
	if got := ops[0].OutputUnitName.String; got != "корпус" {
		t.Errorf("имя не переехало на первого производителя: %q", got)
	}
	if got := ops[1].OutputUnitName.String; got != "" {
		t.Errorf("поглощающий шаг обязан не хранить имя (второе мнение о том же факте), получено %q", got)
	}
}

// TestAssemblyCanonicalizeLegacyFallback — неосведомлённая запись входов в поле 46 не шлёт;
// объединение берётся из легаси-проекции, и поведение остаётся сегодняшним.
func TestAssemblyCanonicalizeLegacyFallback(t *testing.T) {
	ops := []entity.TechCardOperation{
		{PieceLineKeys: []string{"FR", "BK"}, OutputUnitKey: nullStringFromPb("SHELL")},
		{PieceLineKeys: []string{"SL"}, InputKeys: []string{"SHELL", "SL"}, OutputUnitKey: nullStringFromPb("SHELL")},
	}
	if verr := canonicalizeAssembly(ops, asmPieces("FR", "BK", "SL")); verr != nil {
		t.Fatalf("легаси-путь отвергнут: %v", verr)
	}
	if got, want := ops[0].InputKeys, []string{"FR", "BK"}; !reflect.DeepEqual(got, want) {
		t.Errorf("шаг 0 объединение из легаси-проекции: %v, ожидалось %v", got, want)
	}
}

// TestAssemblyCanonicalizeViolations — по одному отказу на каждую строку таблицы §5.1, с
// проверкой ПУТИ ПОЛЯ: путь и есть то, чем клиент подсвечивает виноватую строку.
func TestAssemblyCanonicalizeViolations(t *testing.T) {
	longKey := ""
	for i := 0; i < 70; i++ {
		longKey += "X"
	}

	cases := []struct {
		name      string
		pieces    []string
		ops       []entity.TechCardOperation
		wantField string
		wantWhy   string
	}{
		{
			name:   "имя узла без ключа",
			pieces: []string{"FR", "BK"},
			ops: []entity.TechCardOperation{{
				InputKeys:      []string{"FR", "BK"},
				OutputUnitName: nullStringFromPb("корпус"),
			}},
			wantField: "operations[0].output_unit_name",
			wantWhy:   string(entity.AssemblyDetailShadowName),
		},
		{
			name:      "ключ узла длиннее колонки",
			pieces:    []string{"FR", "BK"},
			ops:       []entity.TechCardOperation{asmOp([]string{"FR", "BK"}, longKey)},
			wantField: "operations[0].output_unit_key",
			wantWhy:   "too_long",
		},
		{
			name:      "дубль входа внутри шага",
			pieces:    []string{"FR", "BK"},
			ops:       []entity.TechCardOperation{asmOp([]string{"FR", "FR", "BK"}, "SHELL")},
			wantField: "operations[0].input_keys[1]",
			wantWhy:   string(entity.AssemblyDetailDuplicateInput),
		},
		{
			name:      "джойн из одного входа",
			pieces:    []string{"FR"},
			ops:       []entity.TechCardOperation{asmOp([]string{"FR"}, "SHELL")},
			wantField: "operations[0].output_unit_key",
			wantWhy:   string(entity.AssemblyDetailTooFewInputs),
		},
		{
			name:      "ключ узла занят деталью",
			pieces:    []string{"FR", "BK"},
			ops:       []entity.TechCardOperation{asmOp([]string{"FR", "BK"}, "FR")},
			wantField: "operations[0].output_unit_key",
			wantWhy:   string(entity.AssemblyDetailKeyIsPiece),
		},
		{
			name:   "второй производитель того же узла",
			pieces: []string{"FR", "BK", "SL", "HD"},
			ops: []entity.TechCardOperation{
				asmOp([]string{"FR", "BK"}, "SHELL"),
				asmOp([]string{"SL", "HD"}, "SHELL"),
			},
			wantField: "operations[1].output_unit_key",
			wantWhy:   string(entity.AssemblyDetailSecondProducer),
		},
		{
			name:      "вход не существует",
			pieces:    []string{"FR"},
			ops:       []entity.TechCardOperation{asmOp([]string{"FR", "ПРИЗРАК"}, "SHELL")},
			wantField: "operations[0].input_keys[1]",
			wantWhy:   string(entity.AssemblyDetailUnknownKey),
		},
		{
			name:   "вход появится позже",
			pieces: []string{"FR", "BK", "HD"},
			ops: []entity.TechCardOperation{
				asmOp([]string{"HD", "SHELL"}, "GARMENT"),
				asmOp([]string{"FR", "BK"}, "SHELL"),
			},
			wantField: "operations[0].input_keys[1]",
			wantWhy:   string(entity.AssemblyDetailProducedLater),
		},
		{
			name:   "деталь съедена дважды",
			pieces: []string{"FR", "BK", "HD"},
			ops: []entity.TechCardOperation{
				asmOp([]string{"FR", "BK"}, "SHELL"),
				asmOp([]string{"FR", "HD"}, "HOOD"),
			},
			wantField: "operations[1].input_keys[0]",
			wantWhy:   string(entity.AssemblyDetailConsumedEarlier),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verr := canonicalizeAssembly(c.ops, asmPieces(c.pieces...))
			if verr == nil {
				t.Fatal("ожидался отказ, получено принятие")
			}
			if verr.Field != c.wantField {
				t.Errorf("путь поля %q, ожидался %q", verr.Field, c.wantField)
			}
			if verr.Reason != c.wantWhy {
				t.Errorf("reason %q, ожидался %q", verr.Reason, c.wantWhy)
			}
			if verr.HowToFix == "" {
				t.Error("отказ без объяснения — технолог не узнает, что делать")
			}
		})
	}
}

// TestAssemblyOperationNumberAlreadyCanonical фиксирует, что вторую половину §4.5 (согласование
// operation_number с порядком массива) действующий парсер выполняет сам: он присваивает
// (i+1)*10 безусловно. Тест стоит здесь, чтобы правка парсера не увела это молча.
func TestAssemblyOperationNumberAlreadyCanonical(t *testing.T) {
	ops := []entity.TechCardOperation{
		{OperationNumber: sql.NullInt32{Int32: 10, Valid: true}},
		{OperationNumber: sql.NullInt32{Int32: 20, Valid: true}},
		{OperationNumber: sql.NullInt32{Int32: 30, Valid: true}},
	}
	order := entity.AssemblyOperationOrder(ops)
	if !reflect.DeepEqual(order, []int{0, 1, 2}) {
		t.Errorf("порядок по каноническим номерам %v, ожидался [0 1 2]", order)
	}
}
