package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/spf13/cobra"
)

// assembly-clear снимает разметку узлов сборки с ОДНОЙ тех-карты.
//
// ЗАЧЕМ ОНА СУЩЕСТВУЕТ. Разметка узлов — самый дорогой ручной ввод на карточке, и её нельзя
// снять ничем, кроме клиента. Но клиент откатывается: откат Ф2-бандла делает размеченные
// карточки нередактируемыми для ВСЕХ (старый бандл не может послать assembly_aware и упирается
// в щит), и в этот момент починить карточку становится нечем. Это единственный аварийный люк, и
// он написан СЕЙЧАС, пока знание про дайджесты живое, а не в момент пожара.
//
// КАК ОНА ЭТО ДЕЛАЕТ — и это главное свойство. Она НЕ пишет в базу «своим» упрощённым путём.
// Она читает карточку, снимает сборочные поля с protobuf-формы и отправляет её обратно ЧЕРЕЗ ТОТ
// ЖЕ ЧОКПОИНТ, что и штатное сохранение (ConvertPbTechCardInsertToEntity): канонизация,
// релизные гейты, штамп подписей. Своя запись в обход конвертера породила бы карточку, которую
// сервер сам же считает невалидной, и следующее штатное сохранение отказало бы технологу
// ссылкой на строку, которой он не трогал.
//
// Одна карточка за раз, по id. Никаких «по всем»: массовое снятие разметки — не аварийная
// операция, а решение, и принимать его должен человек, глядя на конкретную карточку.
var (
	assemblyClearID     int
	assemblyClearDryRun bool

	assemblyClearCmd = &cobra.Command{
		Use:   "assembly-clear",
		Short: "Снять разметку узлов сборки с одной тех-карты (аварийный люк)",
		Long: "Снимает output_unit_key/output_unit_name со всех шагов карточки и возвращает входы-узлы\n" +
			"в детали. Запись идёт через штатный конвертер, поэтому отпечатки секций\n" +
			"переставляются той же проекцией, что и при обычном сохранении.\n\n" +
			"Люк на случай отката клиента: старый бандл не может послать assembly_aware и упирается\n" +
			"в щит совместимости, то есть размеченная карточка становится нередактируемой для всех.",
		RunE: runAssemblyClear,
	}
)

func init() {
	assemblyClearCmd.Flags().IntVar(&assemblyClearID, "id", 0, "id тех-карты (обязательно)")
	assemblyClearCmd.Flags().BoolVar(&assemblyClearDryRun, "dry-run", false, "показать, что будет снято, и ничего не писать")
	rootCmd.AddCommand(assemblyClearCmd)
}

func runAssemblyClear(cmd *cobra.Command, args []string) error {
	if assemblyClearID <= 0 {
		return fmt.Errorf("нужен --id тех-карты (положительное число)")
	}
	cfg, err := config.LoadConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("не прочитать конфигурацию: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dbCfg := cfg.DB
	// Выключено принудительно, что бы ни говорило окружение: и бета, и прод ставят
	// MYSQL_AUTOMIGRATE=true, а аварийный инструмент не должен быть тем, кто применяет миграцию.
	// Тот же довод, что у costbasis-report.
	dbCfg.Automigrate = false

	repo, err := store.NewForTest(ctx, dbCfg)
	if err != nil {
		return fmt.Errorf("не открыть базу: %w", err)
	}
	defer repo.Close()

	card, err := repo.TechCards().GetTechCardByIdConsistent(ctx, assemblyClearID)
	if err != nil {
		return fmt.Errorf("не прочитать тех-карту %d: %w", assemblyClearID, err)
	}
	if card == nil {
		return fmt.Errorf("тех-карты %d не существует", assemblyClearID)
	}

	marked := 0
	unitInputs := 0
	for i := range card.Operations {
		if card.Operations[i].OutputUnitKey.String != "" {
			marked++
		}
		for _, in := range card.Operations[i].AssemblyInputs {
			if in.Kind == entity.AssemblyInputUnit {
				unitInputs++
			}
		}
	}
	fmt.Fprintf(os.Stdout, "тех-карта %d: шагов %d, из них производящих узел %d; входов-узлов %d\n",
		assemblyClearID, len(card.Operations), marked, unitInputs)
	if marked == 0 && unitInputs == 0 {
		fmt.Fprintln(os.Stdout, "разметки нет — снимать нечего")
		return nil
	}
	if assemblyClearDryRun {
		fmt.Fprintln(os.Stdout, "--dry-run: ничего не записано")
		return nil
	}

	// Обратный путь через protobuf — ровно тот, которым ходит клон сезона: карточка переводится в
	// wire-форму, правится и возвращается через штатный конвертер.
	// Пустой CostingFx: команда снимает разметку сборки, а не пересчитывает деньги. Курсы нужны
	// только производным costing-полям read-модели, которые обратно в insert не едут.
	full := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{})
	pbInsert := full.GetTechCard()
	if pbInsert == nil {
		return fmt.Errorf("не собрать wire-форму тех-карты %d", assemblyClearID)
	}
	for _, o := range pbInsert.GetOperations() {
		if o == nil {
			continue
		}
		o.OutputUnitKey = ""
		o.OutputUnitName = ""
		// Входы возвращаются к деталь-проекции: piece_line_keys карточка несёт всегда, и это
		// ровно замыкание входов по деталям, посчитанное чтением.
		o.InputKeys = nil
	}
	// Осведомлённость и НАМЕРЕНИЕ. Без второго флага контентный бекстоп отказал бы этой записи —
	// и правильно бы сделал: снятие разметки без объявленного намерения неотличимо от
	// параллельной вкладки, стирающей её по недоразумению.
	pbInsert.AssemblyAware = true
	pbInsert.AssemblyCleared = true

	insert, err := dto.ConvertPbTechCardInsertToEntity(pbInsert)
	if err != nil {
		return fmt.Errorf("карточка не проходит штатную валидацию после снятия разметки: %w", err)
	}
	if _, err := repo.TechCards().UpdateTechCardAndListOrphanedPatternURLs(ctx, assemblyClearID, insert, card.LockVersion); err != nil {
		return fmt.Errorf("не записать тех-карту %d: %w", assemblyClearID, err)
	}
	fmt.Fprintf(os.Stdout, "разметка снята: узлов %d, входов-узлов %d; отпечатки секций переставлены\n",
		marked, unitInputs)
	return nil
}
