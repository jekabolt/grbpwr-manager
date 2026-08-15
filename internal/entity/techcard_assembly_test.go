package entity

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Кейсы движка живут в testdata/assembly_cases.json и читаются ДВУМЯ реализациями — этой и
// TS-портом в админ-клиенте. Поэтому таблица тестов здесь намеренно тупая: она не описывает
// поведение, она сверяет его с общим файлом. Всё, что описывает поведение, обязано лежать в
// JSON, иначе TS-порт этого не увидит и разъедется.

type assemblyCaseFile struct {
	Cases []assemblyCase `json:"cases"`
}

type assemblyCase struct {
	Name   string `json:"name"`
	Why    string `json:"why"`
	Pieces []struct {
		LineKey string `json:"lineKey"`
		Name    string `json:"name"`
	} `json:"pieces"`
	Steps []struct {
		Inputs     []string `json:"inputs"`
		Output     string   `json:"output"`
		OutputName string   `json:"outputName"`
	} `json:"steps"`
	ExpectViolations []struct {
		Rule   int    `json:"rule"`
		Detail string `json:"detail"`
		Step   int    `json:"step"`
		Input  int    `json:"input"`
		Key    string `json:"key"`
	} `json:"expectViolations"`
	ExpectFrontierBefore [][]string `json:"expectFrontierBefore"`
	ExpectFrontier       []string   `json:"expectFrontier"`
	ExpectUnits          []struct {
		Key        string   `json:"key"`
		Name       string   `json:"name"`
		ProducedAt int      `json:"producedAt"`
		AbsorbedAt []int    `json:"absorbedAt"`
		Leaves     []string `json:"leaves"`
	} `json:"expectUnits"`
	ExpectRelease []struct {
		Rule   int    `json:"rule"`
		Detail string `json:"detail"`
	} `json:"expectRelease"`
}

func TestAssemblyCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/assembly_cases.json")
	if err != nil {
		t.Fatalf("не прочитать общий файл кейсов: %v", err)
	}
	var file assemblyCaseFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("общий файл кейсов не разбирается: %v", err)
	}
	if len(file.Cases) == 0 {
		t.Fatal("общий файл кейсов пуст")
	}

	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			pieces := make([]AssemblyPiece, 0, len(c.Pieces))
			pieceKeys := make(map[string]bool, len(c.Pieces))
			for _, p := range c.Pieces {
				pieces = append(pieces, AssemblyPiece{LineKey: p.LineKey, Name: p.Name})
				pieceKeys[p.LineKey] = true
			}

			steps := make([]AssemblyStep, 0, len(c.Steps))
			for _, s := range c.Steps {
				steps = append(steps, AssemblyStep{
					Inputs:         ClassifyAssemblyInputs(pieceKeys, s.Inputs),
					OutputUnitKey:  s.Output,
					OutputUnitName: s.OutputName,
				})
			}

			res := AssemblySweep(pieces, steps)

			if got, want := len(res.Violations), len(c.ExpectViolations); got != want {
				t.Fatalf("нарушений %d, ожидалось %d\nполучено: %s", got, want, formatViolations(res.Violations))
			}
			for i, want := range c.ExpectViolations {
				got := res.Violations[i]
				if int(got.Rule) != want.Rule || got.Step != want.Step || got.Input != want.Input {
					t.Errorf("нарушение %d: получено {правило %d, шаг %d, вход %d}, ожидалось {правило %d, шаг %d, вход %d}\n  сообщение: %s",
						i, got.Rule, got.Step, got.Input, want.Rule, want.Step, want.Input, got.Message)
				}
				// Ветка сверяется ОБЯЗАТЕЛЬНО: у «такого нет» и «появится позже» одинаковые
				// координаты, и без этой строки реализация с мёртвой веткой прошла бы кейс.
				if want.Detail == "" {
					t.Errorf("нарушение %d: в общем файле не указан detail — ветка не запинена", i)
				} else if string(got.Detail) != want.Detail {
					t.Errorf("нарушение %d: ветка %q, ожидалась %q", i, got.Detail, want.Detail)
				}
				if want.Key != "" && got.Key != want.Key {
					t.Errorf("нарушение %d: ключ %q, ожидался %q", i, got.Key, want.Key)
				}
				if got.Message == "" {
					t.Errorf("нарушение %d без сообщения — отказ обязан объяснять себя", i)
				}
			}

			if c.ExpectFrontierBefore != nil {
				if len(res.FrontierBefore) != len(c.ExpectFrontierBefore) {
					t.Fatalf("фронтиров-до %d, ожидалось %d", len(res.FrontierBefore), len(c.ExpectFrontierBefore))
				}
				for i, want := range c.ExpectFrontierBefore {
					if !reflect.DeepEqual(normalizeKeys(res.FrontierBefore[i]), normalizeKeys(want)) {
						t.Errorf("фронтир перед шагом %d: получен %v, ожидался %v", i, res.FrontierBefore[i], want)
					}
				}
			}

			if !reflect.DeepEqual(normalizeKeys(res.Frontier), normalizeKeys(c.ExpectFrontier)) {
				t.Errorf("фронтир: получен %v, ожидался %v", res.Frontier, c.ExpectFrontier)
			}

			// Когда кейс перечисляет узлы — он перечисляет их ВСЕ: иначе лишний созданный узел,
			// съеденный до конца прохода, не всплыл бы ни здесь, ни во фронтире.
			if c.ExpectUnits != nil && len(res.Units) != len(c.ExpectUnits) {
				t.Errorf("узлов создано %d, ожидалось %d", len(res.Units), len(c.ExpectUnits))
			}
			for _, want := range c.ExpectUnits {
				got, ok := res.Units[want.Key]
				if !ok {
					t.Errorf("узел %q не создан", want.Key)
					continue
				}
				if got.Name != want.Name {
					t.Errorf("узел %q: имя %q, ожидалось %q", want.Key, got.Name, want.Name)
				}
				if got.ProducedAt != want.ProducedAt {
					t.Errorf("узел %q: первый производитель — шаг %d, ожидался %d", want.Key, got.ProducedAt, want.ProducedAt)
				}
				if !reflect.DeepEqual(normalizeInts(got.AbsorbedAt), normalizeInts(want.AbsorbedAt)) {
					t.Errorf("узел %q: поглощения %v, ожидались %v", want.Key, got.AbsorbedAt, want.AbsorbedAt)
				}
				if !reflect.DeepEqual(normalizeKeys(got.Leaves), normalizeKeys(want.Leaves)) {
					t.Errorf("узел %q: замыкание %v, ожидалось %v", want.Key, got.Leaves, want.Leaves)
				}
			}

			rel := AssemblyReleaseCheck(pieces, steps, res)
			if got, want := len(rel), len(c.ExpectRelease); got != want {
				t.Fatalf("отказов релиза %d, ожидалось %d\nполучено: %s", got, want, formatViolations(rel))
			}
			for i, want := range c.ExpectRelease {
				if int(rel[i].Rule) != want.Rule {
					t.Errorf("отказ релиза %d: правило %d, ожидалось %d", i, rel[i].Rule, want.Rule)
				}
				if want.Detail == "" {
					t.Errorf("отказ релиза %d: в общем файле не указан detail", i)
				} else if string(rel[i].Detail) != want.Detail {
					t.Errorf("отказ релиза %d: ветка %q, ожидалась %q", i, rel[i].Detail, want.Detail)
				}
				if rel[i].Message == "" {
					t.Errorf("отказ релиза %d без сообщения", i)
				}
			}
		})
	}
}

// TestAssemblyOperationOrder — §4.5: порядок шагов берётся ТОЛЬКО отсюда, потому что
// валидация идёт над payload'ом (порядок = позиция в массиве), а чтение сортирует по
// operation_number. На легаси-карточке это две разные последовательности.
func TestAssemblyOperationOrder(t *testing.T) {
	num := func(n int32) sql.NullInt32 { return sql.NullInt32{Int32: n, Valid: true} }

	t.Run("нумерованные идут по номеру", func(t *testing.T) {
		ops := []TechCardOperation{{OperationNumber: num(30)}, {OperationNumber: num(10)}, {OperationNumber: num(20)}}
		if got := AssemblyOperationOrder(ops); !reflect.DeepEqual(got, []int{1, 2, 0}) {
			t.Errorf("порядок %v, ожидался [1 2 0]", got)
		}
	})

	t.Run("ненумерованные уходят в хвост, сохраняя исходный порядок", func(t *testing.T) {
		ops := []TechCardOperation{{}, {OperationNumber: num(20)}, {}, {OperationNumber: num(10)}}
		if got := AssemblyOperationOrder(ops); !reflect.DeepEqual(got, []int{3, 1, 0, 2}) {
			t.Errorf("порядок %v, ожидался [3 1 0 2]", got)
		}
	})

	t.Run("одинаковые номера разрешаются стабильно по позиции", func(t *testing.T) {
		ops := []TechCardOperation{{OperationNumber: num(10)}, {OperationNumber: num(10)}}
		if got := AssemblyOperationOrder(ops); !reflect.DeepEqual(got, []int{0, 1}) {
			t.Errorf("порядок %v, ожидался [0 1]", got)
		}
	})
}

// TestAssemblyClassifyInputs фиксирует правило классификации, на котором держится согласие
// клиента и сервера: не совпал с деталью — значит ссылка на узел, и висячесть ловит правило 1.
func TestAssemblyClassifyInputs(t *testing.T) {
	keys := map[string]bool{"FR": true}
	got := ClassifyAssemblyInputs(keys, []string{"FR", "SHELL"})
	want := []OperationInput{
		{Kind: AssemblyInputPiece, Key: "FR"},
		{Kind: AssemblyInputUnit, Key: "SHELL"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("классификация %+v, ожидалась %+v", got, want)
	}
	if ClassifyAssemblyInputs(keys, nil) != nil {
		t.Error("пустой список входов обязан оставаться пустым, а не превращаться в срез нулевой длины")
	}
}

func formatViolations(vs []AssemblyViolation) string {
	if len(vs) == 0 {
		return "(нет)"
	}
	var b strings.Builder
	for _, v := range vs {
		fmt.Fprintf(&b, "\n  правило %d шаг %d вход %d: %s", v.Rule, v.Step, v.Input, v.Message)
	}
	return b.String()
}

func normalizeKeys(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func normalizeInts(s []int) []int {
	if len(s) == 0 {
		return nil
	}
	return s
}
