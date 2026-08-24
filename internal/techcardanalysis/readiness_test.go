package techcardanalysis

import (
	"database/sql"
	"sort"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ── ПРИЁМКА C1–C6 (design §3.3) ─────────────────────────────────────────────────────────────────
//
// Оба направления у каждой из шести, включая три, молчащие на карточке 8 (C1 — пол заполнен, C3 —
// гейт стадии, C6 — сборка сходится): у них fire-сторона построена мутацией фикстуры. Отдельно
// проверяется САМА МЕХАНИКА готовности — что на черновике класс схлопывается, а на не-черновике
// разворачивается, и что ни одна readiness-находка не выходит без `Clause` (пустой Clause молча
// выпал бы из схлопнутого перечисления, то есть находка исчезла бы с черновика совсем).
//
// Хелперы префиксованы `ct*`; общие пробы переиспользуются из route_test.go.

func ctFindings(c *entity.TechCard) []Finding { return RunAudit(c, btFx).Findings }

// ctExpanded runs the audit on a card taken out of draft, so the readiness class is NOT collapsed
// and the individual findings can be asserted one by one.
func ctExpanded(c *entity.TechCard) []Finding {
	c.ApprovalState = entity.TechCardApprovalInReview
	return ctFindings(c)
}

// ctMachineProfile / ctPressProfile build one profile of the card's park.
func ctMachineProfile(key, machine string) entity.TechCardMachineProfile {
	return entity.TechCardMachineProfile{ProfileKey: key, MachineType: machine}
}

func ctPressProfile(key, verb string) entity.TechCardPressProfile {
	p := entity.TechCardPressProfile{ProfileKey: key, PressEquipment: "iron"}
	if verb != "" {
		p.PressOperationType = text(verb)
	}
	return p
}

// ctSetPark replaces the card's equipment park. Указатель НЕПУСТОЙ — на чтении это «профили такие»,
// в отличие от nil, который на карточке 8 значит «профилей нет».
func ctSetPark(c *entity.TechCard, machines []entity.TechCardMachineProfile, presses []entity.TechCardPressProfile) {
	c.Construction.EquipmentDefaults = &entity.TechCardEquipmentDefaults{Machines: machines, Presses: presses}
}

// ── C1 ──────────────────────────────────────────────────────────────────────────────────────────

func TestC1IsSilentOnCard8(t *testing.T) {
	// Пол под всем заполнен: 48 операций, 48 деталей, ряд из четырёх размеров (снят с прода).
	fs := ctExpanded(card8())
	for _, title := range []string{"has no operations", "has no cut pieces", "declares no size range"} {
		rtNone(t, fs, title)
	}
}

func TestC1FiresOnEachMissingFloor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*entity.TechCard)
		title  string
		clause string
	}{
		{"no operations", func(c *entity.TechCard) { c.Operations = nil }, "The card has no operations", "no operations"},
		{"no pieces", func(c *entity.TechCard) { c.Pieces = nil }, "The card has no cut pieces", "no cut pieces"},
		{"no size range", func(c *entity.TechCard) { c.SizeIds = nil }, "The card declares no size range", "no size range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := card8()
			tc.mutate(c)
			f := rtOne(t, ctExpanded(c), tc.title)
			if f.Category != CategoryReadiness || f.Severity != SeverityError {
				t.Errorf("пол под всем — readiness/error, got %s/%s", f.Category, f.Severity)
			}
			if f.Clause != tc.clause {
				t.Errorf("clause: want %q, got %q", tc.clause, f.Clause)
			}
			rtWantRefs(t, f, RefCard)
		})
	}
}

func TestC1CollapsedSeverityIsNotWatered(t *testing.T) {
	// Схлопнутая находка берёт МАКСИМУМ severity: «операций ноль» не имеет права спрятаться за
	// словом warning рядом с «нет эскиза».
	c := card8()
	c.Operations = nil
	f := rtOne(t, ctFindings(c), collapsedReadinessTitle)
	if f.Severity != SeverityError {
		t.Errorf("схлопнутая severity — максимум из схлопнутых, want error, got %s", f.Severity)
	}
}

// ── C2 ──────────────────────────────────────────────────────────────────────────────────────────

func TestC2NamesFiveEmptySectionsOnCard8(t *testing.T) {
	// ПЯТЬ, А НЕ ЧЕТЫРЕ. §3.3 перечисляет базовый размер в правиле, но в примере по карточке 8 о
	// нём забывает; на проде base_sample_size_id этой карточки — NULL при объявленном ряде из
	// четырёх размеров, значит пятая пустота реальна.
	f := rtOne(t, ctExpanded(card8()), "The print packet would go out with")
	if f.Title != "The print packet would go out with 5 empty sections" {
		t.Errorf("C2: %q", f.Title)
	}
	for _, want := range []string{"hem finish", "construction notes", "labels", "packaging", "base sample size"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("C2 обязана назвать пустоту %q, got: %s", want, f.Detail)
		}
	}
	if f.Clause == "" {
		t.Error("readiness-находка без Clause выпадет из схлопнутого перечисления")
	}
}

func TestC2GoesQuietWhenEverySectionIsFilled(t *testing.T) {
	c := card8()
	c.Construction.HemFinish = text("обмётка + подгибка 20 мм")
	c.Construction.Notes = text("см. лист обработки")
	c.Labels = []entity.TechCardLabel{{LabelType: entity.LabelTypeCare, Content: text("30°C")}}
	c.Packaging = &entity.TechCardPackaging{FoldingMethod: text("пополам")}
	c.BaseSampleSizeId = sql.NullInt32{Int32: 4, Valid: true}
	rtNone(t, ctExpanded(c), "The print packet would go out with")
}

func TestC2CountsAnEmptyPackagingRowAsEmpty(t *testing.T) {
	// Присутствие СТРОКИ не равно заполненности: строка, созданная сохранением соседней секции,
	// печатается такой же пустой, как её отсутствие.
	c := card8()
	c.Packaging = &entity.TechCardPackaging{}
	f := rtOne(t, ctExpanded(c), "The print packet would go out with")
	if !strings.Contains(f.Detail, "packaging") {
		t.Errorf("пустая строка упаковки обязана считаться пустотой: %s", f.Detail)
	}
}

func TestC2DoesNotAskAnAuxiliaryCardForLabels(t *testing.T) {
	c := card8()
	c.Purpose = entity.TechCardPurposeAuxiliary
	f := rtOne(t, ctExpanded(c), "The print packet would go out with")
	if strings.Contains(f.Detail, "labels") {
		t.Errorf("у вспомогательной карточки лейблов не бывает по определению (NF-07): %s", f.Detail)
	}
	if !strings.Contains(f.Title, "4 empty") {
		t.Errorf("без лейблов пустот четыре: %q", f.Title)
	}
}

// ── C3 ──────────────────────────────────────────────────────────────────────────────────────────

func TestC3IsSilentBelowSmsEvenWithNoLabelsAnywhere(t *testing.T) {
	// Карточка 8 — sellable-пиджак без единого лейбла где-либо, и всё равно C3 на ней молчит:
	// лейблы заводят к продажному образцу, а не к прототипу. Гейт стадии — это и есть проверка.
	c := card8()
	if c.Stage != entity.TechCardStageProto {
		t.Fatalf("фикстура сменила стадию: %s", c.Stage)
	}
	rtNone(t, ctExpanded(c), "labels")
}

func TestC3FiresAtSmsWithNoLabelsAnywhere(t *testing.T) {
	c := card8()
	c.Stage = entity.TechCardStageSMS
	f := rtOne(t, ctExpanded(c), "with no labels anywhere")
	if f.Category != CategoryReadiness || f.Severity != SeverityWarning {
		t.Errorf("C3 — readiness/warning, got %s/%s", f.Category, f.Severity)
	}
	if !strings.Contains(f.Detail, "care") {
		t.Errorf("C3 обязана назвать обязательный лейбл: %s", f.Detail)
	}
	if f.Clause == "" {
		t.Error("readiness-находка без Clause выпадет из схлопнутого перечисления")
	}
}

func TestC3FiresOnASpecWithNoBomLine(t *testing.T) {
	c := card8()
	c.Stage = entity.TechCardStageSMS
	c.Labels = []entity.TechCardLabel{{LabelType: entity.LabelTypeCare, Content: text("30°C")}}

	fs := ctExpanded(c)
	rtNone(t, fs, "with no labels anywhere")
	f := rtOne(t, fs, "care label spec is not linked to a BOM line")
	rtWantRefs(t, f, RefCard)
}

func TestC3FiresOnABomLineWithNoSpec(t *testing.T) {
	c := card8()
	c.Stage = entity.TechCardStageSMS
	btAddBom(c, entity.TechCardBomItem{Section: entity.BomSectionLabel, Name: "care label"})

	fs := ctExpanded(c)
	rtNone(t, fs, "with no labels anywhere")
	f := rtOne(t, fs, `BOM line "care label" is a label nothing describes`)
	rtWantRefs(t, f, RefBom("care label"))
}

func TestC3IsSilentWhenTheBridgeIsWhole(t *testing.T) {
	c := card8()
	c.Stage = entity.TechCardStageSMS
	line := btAddBom(c, entity.TechCardBomItem{Section: entity.BomSectionLabel, Name: "care label"})
	c.Labels = []entity.TechCardLabel{{
		LabelType: entity.LabelTypeCare,
		Content:   text("30°C"),
		BomItemId: sql.NullInt32{Int32: int32(line.Id), Valid: true},
	}}
	fs := ctExpanded(c)
	rtNone(t, fs, "label")
}

func TestC3TreatsABrokenLinkAsUnlinked(t *testing.T) {
	// Линк ON DELETE SET NULL, то есть рвётся сам и легально; но указатель на удалённую строку —
	// это тоже «не куплено», и молчать на нём нельзя.
	c := card8()
	c.Stage = entity.TechCardStageSMS
	c.Labels = []entity.TechCardLabel{{
		LabelType: entity.LabelTypeCare,
		BomItemId: sql.NullInt32{Int32: 9999, Valid: true},
	}}
	rtOne(t, ctExpanded(c), "care label spec is not linked to a BOM line")
}

func TestC3IsSilentOnAnAuxiliaryCard(t *testing.T) {
	c := card8()
	c.Stage = entity.TechCardStageSMS
	c.Purpose = entity.TechCardPurposeAuxiliary
	rtNone(t, ctExpanded(c), "labels")
}

// ── C4 ──────────────────────────────────────────────────────────────────────────────────────────

func TestC4FiresOnCard8WithNoParkAtAll(t *testing.T) {
	f := rtOne(t, ctExpanded(card8()), "No equipment profiles on a card that names")
	if f.Title != "No equipment profiles on a card that names 4 machine types" {
		t.Errorf("на карточке 8 четыре различных machine_type: %q", f.Title)
	}
	if f.Category != CategoryReadiness || f.Severity != SeverityWarning {
		t.Errorf("парка нет — readiness/warning, got %s/%s", f.Category, f.Severity)
	}
	for _, want := range []string{"lockstitch", "overlock", "buttonhole", "button_attach"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("C4 обязана перечислить машины маршрута, %q нет: %s", want, f.Detail)
		}
	}
	if f.Clause != "no equipment profiles" {
		t.Errorf("clause: %q", f.Clause)
	}
}

func TestC4GoesQuietOnceThereIsAPark(t *testing.T) {
	c := card8()
	ctSetPark(c, []entity.TechCardMachineProfile{ctMachineProfile("MP-1", "lockstitch")}, nil)
	rtNone(t, ctExpanded(c), "No equipment profiles")
}

func TestC4TurnsADanglingProfileKeyIntoAnIntegrityError(t *testing.T) {
	c := card8()
	ctSetPark(c, []entity.TechCardMachineProfile{ctMachineProfile("MP-1", "lockstitch")}, nil)
	card8OpByNumber(c, 10).MachineProfileKey = text("MP-DOES-NOT-EXIST")

	f := rtOne(t, ctExpanded(c), "machine_profile_key points at a machine profile the card does not have")
	if f.Category != CategoryIntegrity {
		t.Errorf("битая мягкая ссылка — integrity, а не readiness: got %s", f.Category)
	}
	if f.Severity != SeverityError {
		t.Errorf("битая ссылка — error, got %s", f.Severity)
	}
	rtWantRefs(t, f, RefOp(10))
}

func TestC4IsSilentWhenTheProfileKeyResolves(t *testing.T) {
	c := card8()
	ctSetPark(c, []entity.TechCardMachineProfile{ctMachineProfile("MP-1", "lockstitch")}, nil)
	card8OpByNumber(c, 10).MachineProfileKey = text("MP-1")
	rtNone(t, ctExpanded(c), "points at a machine profile")
}

func TestC4CatchesADanglingPressKeyToo(t *testing.T) {
	c := card8()
	ctSetPark(c, nil, []entity.TechCardPressProfile{ctPressProfile("PP-1", "")})
	card8OpByNumber(c, 50).PressProfileKey = text("PP-GONE")
	f := rtOne(t, ctExpanded(c), "press_profile_key points at a press profile the card does not have")
	rtWantRefs(t, f, RefOp(50))
}

func TestC4IntegrityErrorSurvivesTheDraftCollapse(t *testing.T) {
	// На черновике readiness схлопывается, а битая ссылка — НЕТ: это такой же дефект, как на
	// релизе, и спрятать его в строке «ещё не готово» значило бы потерять его.
	c := card8()
	ctSetPark(c, []entity.TechCardMachineProfile{ctMachineProfile("MP-1", "lockstitch")}, nil)
	card8OpByNumber(c, 10).MachineProfileKey = text("MP-GONE")
	rtOne(t, ctFindings(c), "machine_profile_key points at a machine profile")
}

func TestC4AsksWhichProfileWhenTwoApply(t *testing.T) {
	c := card8()
	ctSetPark(c, []entity.TechCardMachineProfile{
		ctMachineProfile("MP-1", "lockstitch"),
		ctMachineProfile("MP-2", "lockstitch"),
	}, nil)
	f := rtOne(t, ctExpanded(c), "could inherit from more than one equipment profile")
	if f.Category != CategoryReadiness {
		t.Errorf("неоднозначность наследования — readiness, got %s", f.Category)
	}
	// Применимое множество — шаги, у которых ЕСТЬ из чего выбирать: сорок локстепов карточки 8.
	// Оверлочные, петельный и пуговичный в него не входят — им подходящих профилей ноль, и
	// неоднозначности у них нет.
	if !strings.Contains(f.Title, "40 of 40") {
		t.Errorf("применимое множество — шаги с подходящим профилем: %q", f.Title)
	}
}

func TestC4DoesNotCallItAmbiguousWhenOnlyOneProfileApplies(t *testing.T) {
	// СУЖЕНИЕ ПРОТИВ БУКВЫ §3.3 («≥2 профилей одного kind»), и это не вкусовщина: два профиля
	// РАЗНЫХ машин — не выбор для петельного шага, а один профиль и один посторонний.
	c := card8()
	ctSetPark(c, []entity.TechCardMachineProfile{
		ctMachineProfile("MP-1", "lockstitch"),
		ctMachineProfile("MP-2", "overlock"),
	}, nil)
	rtNone(t, ctExpanded(c), "could inherit from more than one equipment profile")
}

func TestC4IsSilentWhenTheStepHasChosen(t *testing.T) {
	c := card8()
	ctSetPark(c, []entity.TechCardMachineProfile{
		ctMachineProfile("MP-1", "lockstitch"),
		ctMachineProfile("MP-2", "lockstitch"),
	}, nil)
	for i := range c.Operations {
		if machineToken(&c.Operations[i]) == "lockstitch" {
			c.Operations[i].MachineProfileKey = text("MP-1")
		}
	}
	rtNone(t, ctExpanded(c), "could inherit from more than one equipment profile")
}

func TestC4AppliesThePressRuleOfA3(t *testing.T) {
	// Два УНИВЕРСАЛЬНЫХ пресса — выбор для всякого термошага; универсальный плюс названный под
	// чужой глагол — не выбор.
	c := card8()
	ctSetPark(c, nil, []entity.TechCardPressProfile{ctPressProfile("PP-1", ""), ctPressProfile("PP-2", "")})
	rtOne(t, ctExpanded(c), "could inherit from more than one equipment profile")

	c2 := card8()
	ctSetPark(c2, nil, []entity.TechCardPressProfile{ctPressProfile("PP-1", ""), ctPressProfile("PP-2", "fusing")})
	rtNone(t, ctExpanded(c2), "could inherit from more than one equipment profile")
}

// ── C5 ──────────────────────────────────────────────────────────────────────────────────────────

func TestC5FiresOnCard8(t *testing.T) {
	f := rtOne(t, ctExpanded(card8()), "carries no technical sketch")
	if f.Category != CategoryReadiness || f.Severity != SeverityWarning {
		t.Errorf("C5 — readiness/warning, got %s/%s", f.Category, f.Severity)
	}
	if f.Clause != "no technical sketch" {
		t.Errorf("clause: %q", f.Clause)
	}
}

func TestC5IsSilencedByATechnicalMediaRow(t *testing.T) {
	c := card8()
	c.Media = []entity.TechCardMediaItem{{MediaId: 1, Category: entity.TechCardMediaCategoryTechnical}}
	rtNone(t, ctExpanded(c), "carries no technical sketch")
}

func TestC5ReadsTheCategoryNotTheKind(t *testing.T) {
	// 0092: КАТЕГОРИЯ, а не kind. Мудборд — не эскиз, сколько бы его ни было.
	c := card8()
	c.Media = []entity.TechCardMediaItem{
		{MediaId: 1, Category: entity.TechCardMediaCategoryMoodboard},
		{MediaId: 2, Category: entity.TechCardMediaCategoryMoodboard},
	}
	rtOne(t, ctExpanded(c), "carries no technical sketch")
}

func TestC5SaysOutLoudThatSketchesAreNeverChecked(t *testing.T) {
	res := RunAudit(card8(), btFx)
	found := false
	for _, l := range res.NotChecked {
		if strings.Contains(l, "sketch") {
			found = true
		}
	}
	if !found {
		t.Errorf("эскиз не проверяется НИКОГДА, и прогон обязан это говорить: %v", res.NotChecked)
	}
}

// ── C6 ──────────────────────────────────────────────────────────────────────────────────────────

func TestC6IsSilentOnCard8BecauseItConverges(t *testing.T) {
	fs := ctExpanded(card8())
	rtNone(t, fs, "does not converge")
	rtNone(t, fs, "never sewn into anything")
}

func TestC6PredictsTheReleaseRefusalOnTwoTerminals(t *testing.T) {
	// Снимаем финальный джойн (460) вместе с двумя шагами, которые берут его выход входом, —
	// иначе получилась бы карточка с висячей ссылкой, которую запись не приняла бы вовсе (§1).
	c := card8()
	card8DropOperation(c, 480)
	card8DropOperation(c, 470)
	card8DropOperation(c, 460)

	f := rtOne(t, ctExpanded(c), "does not converge into one garment")
	if f.Category != CategoryAssembly {
		t.Errorf("C6 — assembly (§3.3), got %s", f.Category)
	}
	if !strings.Contains(f.Title, "2 terminals") {
		t.Errorf("C6 обязана назвать число терминалов: %q", f.Title)
	}
	if !strings.Contains(f.Detail, "release will refuse this card") {
		t.Errorf("текст обязан называть релизный гейт — это предсказание отказа, а не мнение: %s", f.Detail)
	}
	rtWantRefs(t, f, RefUnit("lining"), RefUnit("base"))
}

func TestC6FiresOnAPieceNobodySews(t *testing.T) {
	c := card8()
	c.Pieces = append(c.Pieces, entity.TechCardPiece{
		Id: 9001, Name: "ORPHAN_1", LineKey: "BT-PIECE-ORPHAN",
		PiecesPerGarment: 1, Grainline: "lengthwise",
	})
	f := rtOne(t, ctExpanded(c), `Cut piece "ORPHAN_1" is never sewn into anything`)
	if f.Category != CategoryAssembly {
		t.Errorf("C6 — assembly, got %s", f.Category)
	}
	if !strings.Contains(f.Detail, "release will refuse this card") {
		t.Errorf("текст обязан называть релизный гейт: %s", f.Detail)
	}
	rtWantRefs(t, f, RefPiece("ORPHAN_1"))
}

func TestC6AggregatesManyOrphanPieces(t *testing.T) {
	c := card8()
	for i := 0; i < 5; i++ {
		c.Pieces = append(c.Pieces, entity.TechCardPiece{
			Id: 9100 + i, Name: "ORPHAN_" + string(rune('A'+i)), LineKey: "BT-PIECE-ORPHAN-" + string(rune('A'+i)),
			PiecesPerGarment: 1, Grainline: "lengthwise",
		})
	}
	f := rtOne(t, ctExpanded(c), "cut pieces are never sewn into anything")
	if !strings.Contains(f.Title, "5 of 53") {
		t.Errorf("закон агрегации §3.0: одна находка с дробью — %q", f.Title)
	}
	if len(f.Refs) != 3 {
		t.Errorf("ровно три якоря-образца, got %v", f.Refs)
	}
}

func TestC6IsSilentOnAnUnmarkedCard(t *testing.T) {
	// Карточка без единого производящего шага проходит релиз ВАКУУМНО (§1): правило 4 на ней не
	// включается, и предсказывать нечего.
	c := card8()
	for i := range c.Operations {
		c.Operations[i].OutputUnitKey = nullText()
		c.Operations[i].OutputUnitName = nullText()
		c.Operations[i].AssemblyInputs = nil
		c.Operations[i].InputKeys = nil
	}
	fs := ctExpanded(c)
	rtNone(t, fs, "does not converge")
	rtNone(t, fs, "never sewn into anything")
}

// ── ПУСТАЯ КАРТОЧКА: ПОИМЁННЫЙ СОСТАВ ───────────────────────────────────────────────────────────

// TestReadinessOnAnEmptyCardIsNamed закрепляет ТО САМОЕ, на что ссылается TestRouteHandlesAnEmptyCard:
// маршрут и BOM на пустой карточке молчат, а готовность заговаривает — и вот ЧЕМ ИМЕННО. Без
// поимённого состава ссылка была бы обещанием: «остальное — readiness» зелено и тогда, когда
// readiness выпускает мусор.
//
// Заодно это единственное место, где закреплено молчание проверок ПОКРЫТИЯ на пустом множестве:
// C7 (SMV), C8 (работы) и C9 (финишный блок) обязаны молчать при нуле операций — «SMV 0/0» было бы
// шумом поверх C1, которая про тот же ноль уже сказала по-человечески.
func TestReadinessOnAnEmptyCardIsNamed(t *testing.T) {
	for _, tc := range []struct {
		name string
		card *entity.TechCard
		want []string
	}{
		{
			name: "не sellable: лейблов от неё никто не ждёт",
			card: &entity.TechCard{},
			want: []string{
				"The card declares no size range",
				"The card has no cut pieces",
				"The card has no operations",
				"The card carries no technical sketch",
				"The print packet would go out with 4 empty sections",
			},
		},
		{
			name: "sellable: лейблы становятся пятой пустотой печатного пакета",
			card: &entity.TechCard{TechCardInsert: entity.TechCardInsert{Purpose: entity.TechCardPurposeSellable}},
			want: []string{
				"The card declares no size range",
				"The card has no cut pieces",
				"The card has no operations",
				"The card carries no technical sketch",
				"The print packet would go out with 5 empty sections",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// ApprovalState пустой строки — это НЕ draft, поэтому класс здесь не схлопывается и
			// состав виден поимённо. Схлопывание проверяется отдельно, на карточке 8.
			fs := ctFindings(tc.card)

			got := make([]string, 0, len(fs))
			for _, f := range fs {
				if f.Category != CategoryReadiness {
					t.Errorf("на пустой карточке заговаривает только готовность, а пришло: %s",
						rtDump([]Finding{f}))
					continue
				}
				got = append(got, f.Title)
			}
			sort.Strings(got)
			wantSorted := append([]string(nil), tc.want...)
			sort.Strings(wantSorted)
			if strings.Join(got, "\n") != strings.Join(wantSorted, "\n") {
				t.Errorf("состав readiness-находок пустой карточки поехал:\n want:\n  %s\n got:\n  %s",
					strings.Join(wantSorted, "\n  "), strings.Join(got, "\n  "))
			}

			// C1 — error, и это не украшение: схлопнутая severity берёт максимум, и warning здесь
			// спрятал бы «операций ноль» за словом «предупреждение».
			for _, f := range fs {
				wantSev := SeverityWarning
				if strings.HasPrefix(f.Title, "The card has no") || strings.HasPrefix(f.Title, "The card declares no") {
					wantSev = SeverityError
				}
				if f.Severity != wantSev {
					t.Errorf("%q: severity %q, want %q", f.Title, f.Severity, wantSev)
				}
			}

			// Проверки покрытия на пустом множестве обязаны молчать, а не печатать «0 of 0».
			for _, silent := range []string{"standard time", "work assigned", "finishing block"} {
				rtNone(t, fs, silent)
			}
		})
	}
}

// ── МЕХАНИКА КЛАССА readiness ───────────────────────────────────────────────────────────────────

func TestReadinessCollapsesOnADraftAndExpandsOffIt(t *testing.T) {
	draft := ctFindings(card8())
	collapsed := rtOne(t, draft, collapsedReadinessTitle)
	for _, want := range []string{"SMV 0/48", "works 5/48", "no equipment profiles", "no technical sketch",
		"print packet has 5 empty sections", "no finishing block"} {
		if !strings.Contains(collapsed.Detail, want) {
			t.Errorf("схлопнутая находка обязана перечислять клаузы, %q нет: %s", want, collapsed.Detail)
		}
	}
	if n := ctCountCategory(draft, CategoryReadiness); n != 1 {
		t.Errorf("на черновике readiness — ровно одна находка, got %d:\n%s", n, rtDump(draft))
	}

	expanded := ctExpanded(card8())
	rtNone(t, expanded, collapsedReadinessTitle)
	// Шесть: C7 (SMV), C8 (работы), C4 (парк), C5 (эскиз), C2 (печатный пакет), C9 (финишный блок).
	if n := ctCountCategory(expanded, CategoryReadiness); n != 6 {
		t.Errorf("на не-черновике readiness разворачивается в шесть находок, got %d:\n%s", n, rtDump(expanded))
	}
}

func TestEveryReadinessFindingCarriesAClause(t *testing.T) {
	// Пустой Clause не ломает ничего видимого — находка просто ИСЧЕЗАЕТ из перечисления
	// схлопнутой, то есть с черновика пропадает совсем. Поэтому это проверяется отдельно и на
	// множестве карточек, где заговаривают все шесть.
	cards := map[string]*entity.TechCard{
		"card8":          card8(),
		"empty floor":    &entity.TechCard{TechCardInsert: entity.TechCardInsert{Purpose: entity.TechCardPurposeSellable}},
		"sms no labels":  ctSmsCard(),
		"costing no cmt": ctCostingCard(),
		"two profiles":   ctTwoProfileCard(),
	}
	for name, c := range cards {
		c.ApprovalState = entity.TechCardApprovalInReview
		fs := ctFindings(c)
		seen := 0
		for _, f := range fs {
			if f.Category != CategoryReadiness {
				continue
			}
			seen++
			if strings.TrimSpace(f.Clause) == "" {
				t.Errorf("%s: readiness-находка %q вышла без Clause", name, f.Title)
			}
		}
		if seen == 0 {
			t.Errorf("%s: ни одной readiness-находки — проба вакуумна", name)
		}
	}
}

func ctSmsCard() *entity.TechCard {
	c := card8()
	c.Stage = entity.TechCardStageSMS
	return c
}

func ctCostingCard() *entity.TechCard {
	c := card8()
	btCosting(c, "")
	return c
}

func ctTwoProfileCard() *entity.TechCard {
	c := card8()
	ctSetPark(c, []entity.TechCardMachineProfile{
		ctMachineProfile("MP-1", "lockstitch"),
		ctMachineProfile("MP-2", "lockstitch"),
	}, nil)
	return c
}

func ctCountCategory(fs []Finding, category string) int {
	n := 0
	for _, f := range fs {
		if f.Category == category {
			n++
		}
	}
	return n
}

// ── ПРИЁМКА C7–C9: ТРИ КЛАУЗЫ, КОТОРЫЕ §3.0 ОБЕЩАЕТ ─────────────────────────────────────────────
//
// Обе стороны у каждой: fire на карточке 8 (SMV нет ни у одного шага, работа у пяти из сорока
// восьми, финишных глаголов ноль) и молчание на карточке, где это заполнено.

// ctFillSmv / ctFillWorks заполняют колонку на ВСЕХ шагах — сторона молчания.
func ctFillSmv(c *entity.TechCard) {
	for i := range c.Operations {
		c.Operations[i].SMV = dec("1.50")
	}
}

func ctFillWorks(c *entity.TechCard) {
	for i := range c.Operations {
		if nsEmpty(c.Operations[i].Work) {
			c.Operations[i].Work = text("join")
		}
	}
}

func TestC7FiresOnCard8AndIsSilentOnceTheRouteIsTimed(t *testing.T) {
	f := rtOne(t, ctExpanded(card8()), "No standard time")
	if f.Title != "No standard time on 48 of 48 operations" {
		t.Errorf("дробь заголовка называет ПРОПУСК: %q", f.Title)
	}
	if f.Category != CategoryReadiness || f.Severity != SeverityWarning {
		t.Errorf("want readiness/warning, got %s/%s", f.Category, f.Severity)
	}
	// Клауза называет ПОКРЫТИЕ, как в образце §3.0 («SMV 0/48»), а не пропуск.
	if f.Clause != "SMV 0/48" {
		t.Errorf("клауза %q, want %q", f.Clause, "SMV 0/48")
	}
	if len(f.Refs) != 3 {
		t.Errorf("закон агрегации §3.0: три якоря-образца, got %v", f.Refs)
	}

	timed := card8()
	ctFillSmv(timed)
	rtNone(t, ctExpanded(timed), "standard time")
}

func TestC7ReportsPerStepBelowTheAggregationThreshold(t *testing.T) {
	// Три пропуска — ветка пер-операционных находок, и клауза там ИМЕНУЕТ ШАГ: три копии «SMV
	// 45/48» схлопнулись бы в перечисление одной и той же дроби трижды.
	c := card8()
	ctFillSmv(c)
	for _, n := range []int32{10, 20, 30} {
		card8OpByNumber(c, n).SMV = decimal.NullDecimal{}
	}
	fs := rtWithTitle(ctExpanded(c), "has no SMV")
	if len(fs) != 3 {
		t.Fatalf("три пропуска — три пер-операционные находки, got %d:\n%s", len(fs), rtDump(fs))
	}
	seen := map[string]bool{}
	for _, f := range fs {
		if seen[f.Clause] {
			t.Errorf("клаузы пер-операционных находок обязаны различаться, %q повторилась", f.Clause)
		}
		seen[f.Clause] = true
	}
}

func TestC8FiresOnCard8AndIsSilentOnceWorksAreAssigned(t *testing.T) {
	f := rtOne(t, ctExpanded(card8()), "No work assigned")
	if f.Title != "No work assigned on 43 of 48 operations" {
		t.Errorf("золотой эталон, ошибка 8: работа не назначена у 43 из 48; got %q", f.Title)
	}
	if f.Clause != "works 5/48" {
		t.Errorf("клауза %q, want %q", f.Clause, "works 5/48")
	}
	if f.Category != CategoryReadiness || f.Severity != SeverityWarning {
		t.Errorf("want readiness/warning, got %s/%s", f.Category, f.Severity)
	}

	assigned := card8()
	ctFillWorks(assigned)
	rtNone(t, ctExpanded(assigned), "work assigned")
}

func TestC9FiresOnCard8AndIsSilentOnceTheRouteCloses(t *testing.T) {
	f := rtOne(t, ctExpanded(card8()), "no finishing block")
	if f.Clause != "no finishing block" {
		t.Errorf("клауза %q", f.Clause)
	}
	if !rtHasRef(f, RefCard) {
		t.Errorf("находка про маршрут целиком якорится на card, got %v", f.Refs)
	}

	// Один pack закрывает проверку: она утверждает ОТСУТСТВИЕ блока, а не его полноту.
	packed := card8()
	rtAppendOp(packed, entity.TechCardOperation{
		OperationType:  entity.OpTypePack,
		AssemblyInputs: []entity.OperationInput{rtUnitInput("blazer")},
		InputKeys:      []string{"blazer"},
	})
	rtNone(t, ctExpanded(packed), "finishing block")
}

func TestC7C8C9CollapseOnTheDraftOfCard8(t *testing.T) {
	// Ради этого они и класса readiness: на черновике три новые клаузы становятся частью ОДНОЙ
	// строки, а не тремя находками поверх и без того длинного списка.
	collapsed := rtOne(t, ctFindings(card8()), collapsedReadinessTitle)
	for _, want := range []string{"SMV 0/48", "works 5/48", "no finishing block"} {
		if !strings.Contains(collapsed.Detail, want) {
			t.Errorf("клаузы %q нет в схлопнутой находке: %s", want, collapsed.Detail)
		}
	}
	if n := ctCountCategory(ctFindings(card8()), CategoryReadiness); n != 1 {
		t.Errorf("на черновике readiness — ровно одна находка, got %d", n)
	}
}
