package design

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ПРОБЫ ФОРМЫ ТКАНИ ПАРЫ (0368) — без базы. Половина гарантий, которой нужны строки (ключ пары,
// каскады, три границы карточки), — в asset_binding_db_test.go.

// ЗАПИСЬ СВЯЗКИ — UPSERT ПО КЛЮЧУ ПАРЫ, А НЕ «ПРОЧИТАЙ И ВСТАВЬ».
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: голый INSERT (второй выбор той же пары — 1062, которого клиент откатить не
// умеет); чтение перед записью (гонка двух выборов на тот же 1062); переписывание tech_card_id в
// ветке дубликата (пара принадлежит одной карточке, и оператор не вправе её «переселять»).
func TestAssetBindingWriteIsAnUpsertOnThePair(t *testing.T) {
	up := strings.ToUpper(assetBindingUpsert)
	if !strings.Contains(up, "INSERT INTO DESIGN_ASSET_BINDING") {
		t.Fatal("a binding is written by an INSERT into design_asset_binding")
	}
	at := strings.Index(up, "ON DUPLICATE KEY UPDATE")
	if at < 0 {
		t.Fatal("choosing a pair's fabric again must replace it: ON DUPLICATE KEY UPDATE is missing")
	}
	if strings.Contains(up, "SELECT") {
		t.Fatal("the binding write must not read first: select-then-insert is the race")
	}
	dup := up[at:]
	for _, col := range []string{"ASSET_ID", "SET_BY", "SET_AT"} {
		if !strings.Contains(dup, col) {
			t.Fatalf("%s is not refreshed when the pair is chosen again", col)
		}
	}
	if strings.Contains(dup, "TECH_CARD_ID") {
		t.Fatal("the duplicate branch must not rewrite tech_card_id: the pair already has its card")
	}
}

// ЦВЕТ СВОТЧА ЛОЖИТСЯ В КОЛОНКУ ОБРЕЗАННЫМ ПО КРАЯМ, А ТО, ЧТО КОЛОНКА НЕ ВМЕЩАЕТ, НЕ ПИШЕТСЯ.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: писать сырое значение — 1406 в строгом режиме уронил бы посадку уже
// оплаченной плитки, а обрезанный hex был бы другим цветом.
func TestKeptColourFactFitsTheColumnOrStaysEmpty(t *testing.T) {
	ctx := context.Background()
	if got := keptColourFact(ctx, 1, "colour_hex", "  #C8102E ", designAssetColourHexMax); got != "#C8102E" {
		t.Fatalf("trimmed hex = %q", got)
	}
	if got := keptColourFact(ctx, 1, "colour_hex", "#C8102E00AA", designAssetColourHexMax); got != "" {
		t.Fatalf("a hex wider than the column must be left empty, got %q", got)
	}
	if got := keptColourFact(ctx, 1, "colour_code", "   ", designAssetColourCodeMax); got != "" {
		t.Fatalf("blank is «not stated», got %q", got)
	}
	code := strings.Repeat("я", designAssetColourCodeMax)
	if got := keptColourFact(ctx, 1, "colour_code", code, designAssetColourCodeMax); got != code {
		t.Fatal("the width is counted in characters, as VARCHAR counts it, not in bytes")
	}
}

// НЕГОДНЫЕ ID ОТКАЗЫВАЮТСЯ ДО ТРАНЗАКЦИИ И ОДНИМ СЕНТИНЕЛОМ.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: пропустить ноль колорвея или слота в транзакцию — ноль не бывает id,
// сторожа карточки ответили бы foreign_colorway / foreign_bom_line, и клиент пошёл бы чинить
// карточку вместо запроса.
func TestSetAssetBindingRefusesMalformedIdsBeforeTheTransaction(t *testing.T) {
	s := &Store{txFunc: func(context.Context, func(context.Context, dependency.Repository) error) error {
		t.Fatal("a malformed request must not open a transaction")
		return nil
	}}
	for name, req := range map[string]entity.DesignAssetBindingSet{
		"no card":            {ColorwayId: 1, BomItemId: 2, AssetId: 3},
		"no colourway":       {TechCardId: 1, BomItemId: 2, AssetId: 3},
		"no slot":            {TechCardId: 1, ColorwayId: 2, AssetId: 3},
		"negative asset":     {TechCardId: 1, ColorwayId: 2, BomItemId: 3, AssetId: -1},
		"negative slot":      {TechCardId: 1, ColorwayId: 2, BomItemId: -3, AssetId: 4},
		"negative colourway": {TechCardId: 1, ColorwayId: -2, BomItemId: 3, AssetId: 4},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.SetAssetBinding(context.Background(), req)
			if !errors.Is(err, entity.ErrDesignInvalidArgument) {
				t.Fatalf("got %v, want ErrDesignInvalidArgument", err)
			}
		})
	}
}
