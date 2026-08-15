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
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
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
	assemblyClearReopen bool

	assemblyClearCmd = &cobra.Command{
		Use:   "assembly-clear",
		Short: "Снять разметку узлов сборки с одной тех-карты (аварийный люк)",
		Long: "Снимает output_unit_key/output_unit_name со всех шагов карточки и возвращает входы-узлы\n" +
			"в детали по замыканию. Запись идёт через штатный конвертер (канонизация, релизные\n" +
			"гейты, штамп) — то есть карточка после снятия валидна ровно так же, как после\n" +
			"обычного сохранения.\n\n" +
			"Подписи НЕ пере-заверяются: секция CONSTRUCTION честно станет «изменено после\n" +
			"подписи», как при любой правке содержания. Скрипт не имеет права утверждать\n" +
			"человеческую подпись заново.\n\n" +
			"Люк на случай отката клиента: старый бандл не может послать assembly_aware и упирается\n" +
			"в щит совместимости, то есть размеченная карточка становится нередактируемой для всех.",
		RunE: runAssemblyClear,
	}
)

func init() {
	assemblyClearCmd.Flags().IntVar(&assemblyClearID, "id", 0, "id тех-карты (обязательно)")
	assemblyClearCmd.Flags().BoolVar(&assemblyClearDryRun, "dry-run", false, "показать, что будет снято, и ничего не писать")
	assemblyClearCmd.Flags().BoolVar(&assemblyClearReopen, "reopen-to-draft", false,
		"вернуть RELEASED-карточку в черновик (иначе стор запрещает править выпущенную карточку)")
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
	// RELEASED-карточка заморожена стором, и это ровно тот случай, ради которого люк написан:
	// в аварии отката клиента выпущенная размеченная карточка иначе остаётся нередактируемой
	// ВООБЩЕ — ни старым бандлом (щит), ни этим скриптом (заморозка). Возврат в черновик —
	// решение человека, поэтому он за отдельным флагом, а не подразумевается.
	released := card.ApprovalState == entity.TechCardApprovalReleased
	if released && !assemblyClearReopen {
		return fmt.Errorf("тех-карта %d выпущена (approval_state=released) и заморожена для правок: "+
			"повторите с --reopen-to-draft, если снятие разметки важнее выпущенного состояния", assemblyClearID)
	}
	if released {
		fmt.Fprintln(os.Stdout, "карточка выпущена — будет возвращена в черновик (--reopen-to-draft)")
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
	// Входы-узлы возвращаются В ДЕТАЛИ ПО ЗАМЫКАНИЮ, а не просто отбрасываются.
	//
	// Разница не косметическая. Шаг со входами [SHELL, SL] после наивного отбрасывания остался бы
	// с одним рукавом: полочка и спинка, жившие ВНУТРИ узла SHELL, к шагу не вернулись бы, и
	// карточка после «аварийного восстановления» врала бы о том, что этот шаг сшивает. Замыкание
	// узла (его листья) движок уже считает — берём его.
	steps := make([]entity.AssemblyStep, 0, len(card.Operations))
	order := entity.AssemblyOperationOrder(card.Operations)
	for _, idx := range order {
		op := card.Operations[idx]
		steps = append(steps, entity.AssemblyStep{
			Inputs:         op.AssemblyInputs,
			OutputUnitKey:  op.OutputUnitKey.String,
			OutputUnitName: op.OutputUnitName.String,
		})
	}
	sweepPieces := make([]entity.AssemblyPiece, 0, len(card.Pieces))
	for _, p := range card.Pieces {
		sweepPieces = append(sweepPieces, entity.AssemblyPiece{LineKey: p.LineKey, Name: p.Name})
	}
	sweep := entity.AssemblySweep(sweepPieces, steps)

	pbOps := pbInsert.GetOperations()
	for i, o := range pbOps {
		if o == nil {
			continue
		}
		o.OutputUnitKey = ""
		o.OutputUnitName = ""
		if i >= len(card.Operations) {
			continue
		}
		// Разворачиваем объединение: деталь остаётся собой, узел раскрывается в свои листья.
		//
		// ОГРУБЛЕНИЕ, о котором надо знать: листья берутся ФИНАЛЬНЫЕ. Обработка на шаге k,
		// бравшая узел, который поглотили позже, получит и те детали, которых на шаге k ещё не
		// существовало (ВТО подола раньше пришива подола). Карточка остаётся валидной, но
		// последовательность огрубляется — это аварийный инструмент, а не редактор.
		// Дедуп нужен — два узла могут содержать одну деталь только при нарушении правила 2, но
		// аварийный инструмент обязан пережить и испорченную карточку.
		seen := make(map[string]bool)
		expanded := make([]string, 0, len(card.Operations[i].AssemblyInputs))
		for _, in := range card.Operations[i].AssemblyInputs {
			keys := []string{in.Key}
			if in.Kind == entity.AssemblyInputUnit {
				if u, ok := sweep.Units[in.Key]; ok {
					keys = u.Leaves
				} else {
					keys = nil // висячая ссылка на узел: возвращать нечего
				}
			}
			for _, k := range keys {
				if !seen[k] {
					seen[k] = true
					expanded = append(expanded, k)
				}
			}
		}
		// Осведомлённая запись живёт по полю 46, поэтому заполняем именно его: положиться на то,
		// что конвертер возьмёт 21, больше нельзя — источник входов выбирается по флагу.
		o.InputKeys = expanded
		o.PieceLineKeys = expanded
	}
	// Осведомлённость и НАМЕРЕНИЕ. Без второго флага контентный бекстоп отказал бы этой записи —
	// и правильно бы сделал: снятие разметки без объявленного намерения неотличимо от
	// параллельной вкладки, стирающей её по недоразумению.
	pbInsert.AssemblyAware = true
	pbInsert.AssemblyCleared = true
	if released {
		// DRAFT ставится В PB, до конверсии — зеркало клона сезона: релизные гейты живут в
		// конвертере, и выпущенное состояние надо снять раньше, чем они его увидят.
		pbInsert.ApprovalState = pb_common.TechCardApprovalState_TECH_CARD_APPROVAL_STATE_DRAFT
	}

	insert, err := dto.ConvertPbTechCardInsertToEntity(pbInsert)
	if err != nil {
		return fmt.Errorf("карточка не проходит штатную валидацию после снятия разметки: %w", err)
	}
	if _, err := repo.TechCards().UpdateTechCardAndListOrphanedPatternURLs(ctx, assemblyClearID, insert, card.LockVersion); err != nil {
		return fmt.Errorf("не записать тех-карту %d: %w", assemblyClearID, err)
	}
	fmt.Fprintf(os.Stdout, "разметка снята: узлов %d, входов-узлов %d\n", marked, unitInputs)
	fmt.Fprintln(os.Stdout, "подписи не пере-заверены: CONSTRUCTION станет «изменено после подписи» — это правда, а не дефект")
	return nil
}
