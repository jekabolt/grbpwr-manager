package techcardanalysis

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ── ГОЛДЕН МАШИННОГО СЛОЯ НА КАРТОЧКЕ 8 (T5, design §14, план §4) ───────────────────────────────
//
// Здесь закреплён ВЕСЬ результат прогона `RunAudit(card8(), Fx{Base: "EUR"})`: находки (категория,
// severity, title, якоря, clause), наблюдения §3.4, строки not_checked и все 48 отпечатков. Это
// единственное место, где машинный слой измеряется ЦЕЛИКОМ; пер-проверочные тесты соседних файлов
// измеряют по одной проверке и не видят ни состава, ни агрегации, ни схлопывания.
//
// ХАРАКТЕРНЫЙ ОТКАЗ ГОЛДЕНА — закрепить то, что код СЛУЧАЙНО делает, вместе с его дефектами, и
// сделать их вечными. Поэтому:
//
//  1. Голден НЕ ПЕРЕГЕНЕРИРУЕТСЯ САМ. Ожидаемое лежит текстовыми константами прямо в этом файле;
//     при расхождении тест печатает построчный дифф И полный фактический дамп, а перенос дампа в
//     константу — РУЧНОЙ шаг, который делает человек, прочитавший дифф. Флага `-update` нет
//     намеренно: голден, переписывающий себя, не доказывает ничего.
//  2. Каждая закреплённая находка сверена не с кодом, который её произвёл, а с прод-дампом
//     карточки (plans/techcard-analysis/02-fixture-constr-dump.txt) и с ручным эталоном
//     01-gold-standard.md. Соответствия «машинная находка ↔ пункт эталона» выписаны ниже.
//  3. Молчание закрепляется ПОИМЁННО (TestGoldenCard8Silences): не «остального нет», а «строки,
//     которую выпустила бы вот эта проверка, нет». «Всё остальное отсутствует» зелено и на дохлом
//     реестре.
//
// СООТВЕТСТВИЕ РУЧНОМУ ЭТАЛОНУ (01-gold-standard.md). Машинный слой не воспроизводит эталон и не
// обязан: почти весь эталон — суждение (оп 460 неисполнима одним шагом, рукав не замкнут, нет
// подбортов). Детерминированно закрыты: ошибка 1 (Base/base) → A1; блокер 4 (петли и пуговицы без
// спецификации) → A2 + B1; ошибка 10 (overlock наследует ss_plain) → A4; ошибка 9 и «параметры» →
// A5 и A3; блокер 2 (дублирования нет) → B3 фактами (вывод «пиджаку оно нужно» — не машинный);
// вопрос 6 (Карманка дороже основной) → B5б; «чего не хватает 9» (профилей 0) → C4. Наблюдениями
// §3.4 (НЕ находками) закрыты ошибки 3, 5, 7 и частично 2. Эталонные ошибки 2, 4, 6 и все спорные
// вопросы машине недоступны — их закрывает только LLM-слой, и приёмка §14 стоит отдельно.
//
// ПОРЯДОК. Голден сравнивает НАБОР: строки находок сортируются лексикографически, а не в порядке
// показа (§11 — порядок решает клиент). Детерминированность самого порядка показа закреплена
// отдельно (TestGoldenCard8OrderIsStableAcrossRuns).
//
// FX ФИКСИРОВАН В ТЕСТЕ: база EUR, курсов нет. Так B5в детерминирована и не зависит ни от какой
// среды — три PLN-линии карточки остаются без курса на любой машине.

// gdFx is the golden's currency channel. Тот же контур, что у btFx/rtFx, но объявлен своим именем:
// голден обязан быть читаемым как самостоятельный документ, а не через переменную чужого файла.
var gdFx = Fx{Base: "EUR"}

// gdDump renders a whole AuditResult as the stable, human-legible text the golden compares.
//
// Одна строка на находку — чтобы дифф показывал ИМЕННО ту находку, которая поехала, а не абзац
// вокруг неё. Detail/Suggestion/Evidence в проекцию НЕ входят: формулировки будут шлифоваться, а
// переснимать из-за запятой голден целиком — верный способ перестать его читать. То, что несёт
// ЗАКОН (дробь агрегации «4 of 4», список форм ключа, число пустых секций), живёт в title и
// закреплено.
func gdDump(res AuditResult, sorted bool) string {
	var b strings.Builder

	lines := make([]string, 0, len(res.Findings))
	for _, f := range res.Findings {
		lines = append(lines, gdFindingLine(f))
	}
	if sorted {
		sort.Strings(lines)
	}
	fmt.Fprintf(&b, "FINDINGS (%d)\n", len(lines))
	for _, l := range lines {
		b.WriteString("  " + l + "\n")
	}

	fmt.Fprintf(&b, "OBSERVATIONS (%d)\n", len(res.Observations))
	for _, o := range res.Observations {
		b.WriteString("  " + o + "\n")
	}

	fmt.Fprintf(&b, "NOT CHECKED (%d)\n", len(res.NotChecked))
	for _, n := range res.NotChecked {
		b.WriteString("  " + n + "\n")
	}

	nums := make([]int, 0, len(res.Fingerprints))
	for n := range res.Fingerprints {
		nums = append(nums, int(n))
	}
	sort.Ints(nums)
	fmt.Fprintf(&b, "FINGERPRINTS (%d)\n", len(nums))
	for _, n := range nums {
		fmt.Fprintf(&b, "  op:%d %s\n", n, res.Fingerprints[int32(n)])
	}
	return b.String()
}

// gdFindingLine is the projection of one finding: everything that carries meaning for the client
// and for the prompt, and nothing that is prose.
func gdFindingLine(f Finding) string {
	refs := append([]string(nil), f.Refs...)
	sort.Strings(refs)
	line := fmt.Sprintf("[%s/%s] %s | refs: %s", f.Severity, f.Category, f.Title, strings.Join(refs, ", "))
	if f.Source != SourceMachine {
		line += " | source: " + f.Source
	}
	if f.Confidence != "" {
		line += " | confidence: " + f.Confidence
	}
	if f.Clause != "" {
		line += " | clause: " + f.Clause
	}
	return line
}

// gdAssertGolden compares an actual dump against the expected one and, on a mismatch, prints BOTH a
// line-by-line delta and the full actual text — the second so a deliberate re-pin is a copy, and
// the first so nobody re-pins without seeing what moved.
func gdAssertGolden(t *testing.T, name, want, got string) {
	t.Helper()
	if want == got {
		return
	}
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	var delta strings.Builder
	for _, l := range gdMissing(wantLines, gotLines) {
		delta.WriteString("  - " + l + "\n")
	}
	for _, l := range gdMissing(gotLines, wantLines) {
		delta.WriteString("  + " + l + "\n")
	}
	t.Errorf("%s: the machine layer moved on card 8.\n"+
		"lines lost (-) and gained (+):\n%s\n"+
		"IF THIS IS INTENDED, re-pin BY HAND — read every line above first:\n%s",
		name, delta.String(), got)
}

// gdMissing returns the lines of a that do not appear in b, respecting multiplicity.
func gdMissing(a, b []string) []string {
	count := make(map[string]int, len(b))
	for _, l := range b {
		count[l]++
	}
	out := make([]string, 0, 4)
	for _, l := range a {
		if count[l] > 0 {
			count[l]--
			continue
		}
		out = append(out, l)
	}
	return out
}

// gdFindingsBlock takes the FINDINGS section of a dump — everything up to the observations. Нужен
// там, где сравнивается состав находок, а остальные блоки заведомо разошлись.
func gdFindingsBlock(dump string) string {
	if i := strings.Index(dump, "OBSERVATIONS ("); i >= 0 {
		return dump[:i]
	}
	return dump
}

// gdInReview returns card 8 with approval_state flipped off draft, which is the ONLY difference
// between the two golden forms: on a draft the readiness class collapses (§3.0).
func gdInReview() *entity.TechCard {
	c := card8()
	c.ApprovalState = entity.TechCardApprovalInReview
	return c
}

func TestGoldenCard8Draft(t *testing.T) {
	got := gdDump(RunAudit(card8(), gdFx), true)
	gdAssertGolden(t, "card 8 as saved (draft)", goldenCard8Draft, got)
}

func TestGoldenCard8Expanded(t *testing.T) {
	got := gdDump(RunAudit(gdInReview(), gdFx), true)
	gdAssertGolden(t, "card 8 off draft (in_review)", goldenCard8InReview, got)
}

// goldenCard8Draft — карточка 8 КАК СОХРАНЕНА: stage=proto, approval_state=draft.
// Три readiness-находки (C4, C5, C2) схлопнуты §3.0 в одну со списком клауз.
const goldenCard8Draft = `FINDINGS (16)
  [error/bom_mismatch] The route sets hardware; the BOM has none | refs: op:470, op:480
  [error/parameter] Seam class ss_plain is not producible on overlock | refs: op:210
  [error/parameter] Seam class ss_plain is not producible on overlock | refs: op:220
  [warning/bom_mismatch] 44 sewing operations, zero thread lines | refs: card
  [warning/bom_mismatch] Cutting wastage is not stated on 4 of 4 roll-goods lines | refs: bom:Плечевая, bom:основная ткань, bom:подкладка
  [warning/bom_mismatch] Fusing is stated in 1 of the 3 places that describe it | refs: bom:Плечевая
  [warning/bom_mismatch] PLN has no rate to EUR: 3 lines drop out of the cost total | refs: bom:Карманка, bom:Плечевая, bom:основная ткань
  [warning/naming] Unit keys differ only in case: "Base" and "base" | refs: op:270, op:450, unit:Base, unit:base
  [warning/parameter] As written the garment has exactly one button | refs: op:480
  [warning/parameter] As written the garment has exactly one buttonhole | refs: op:470
  [warning/parameter] Buttonhole unspecified: no style, no cut length | refs: op:470
  [warning/parameter] Instruction lives only in a note: thread tension | refs: op:40
  [warning/parameter] Pressing parameters missing on 4 of 4 pressing operations | refs: op:100, op:50, op:70
  [warning/question] "Карманка" costs more per metre than the main fabric | refs: bom:Карманка, bom:основная ткань
  [warning/question] Is "подкладка" priced or is that a placeholder? | refs: bom:подкладка
  [warning/readiness] Not yet ready for release | refs: card
OBSERVATIONS (8)
  Lexical mirror pairing over unit names suggests left/right twins: 60<->90, 70<->100, 80<->150, 110<->120, 170<->180, 190<->200, 210<->220, 290<->?, 300<->?, 310<->320, 330<->?, 360<->370, 380<->390, 420<->430 (<->? means the pairer found no partner). The pairing is derived from NAMES only; the input lists are the ground truth - correct it freely.
  Method differs inside suspected twins: op 70 is press_open, op 100 is press.
  Suspected twins are split along the route: ops 80 and 150 are separated by ops 90-140.
  Capitalisation differs inside suspected twins: op 190 "Right front panel with pockets" vs op 200 "LEft front panel with pockets"; op 210 "Right front panel with pockets" vs op 220 "LEft front panel with pockets"; op 360 "left sleeve lining" vs op 370 "Right sleeve lining".
  Lexical mirror pairing over cut-piece names matched 18 _L/_R pairs; 2 side-named pieces have no twin under this rule: BP_LIN_L_2, BP_LIN_R_1. The names are the only evidence here - a twin with a different suffix reads as unpaired.
  Suspected typo: irregular capitalisation in "LEft" inside unit key "LEft front panel with pockets" (produced by op 200; consumed by ops 220, 270).
  Suspected typo: unit keys "Back" (op 30) and "Base" (op 270) differ by 2 edit(s).
  Suspected typo: unit keys "lining back" (op 340) and "lining base" (op 350) differ by 2 edit(s).
NOT CHECKED (4)
  sketch (not reviewed: the analysis path is text-only)
  piece geometry (contours are not stored; measured areas are areas, not outlines)
  labour cost (the card carries no tech_card_costing row: neither the CMT figure nor its SMV backing was checked)
  work ↔ machine legality (this process has not loaded the work catalog)
FINGERPRINTS (48)
  op:10 8a6455c6
  op:20 33aa8f05
  op:30 1bd85c4d
  op:40 47c9055b
  op:50 47c9055b
  op:60 98b88838
  op:70 850e2453
  op:80 a8fa31f4
  op:90 754647a8
  op:100 61bb2124
  op:110 63221d6c
  op:120 09e1983c
  op:130 cc5949fb
  op:140 36e53a53
  op:150 53566e05
  op:160 e3296a7f
  op:170 ed271761
  op:180 661399a9
  op:190 c5219541
  op:200 928796bd
  op:210 46a915ad
  op:220 88a5eecd
  op:230 31ecd7a4
  op:240 4fc20f6e
  op:250 38e90fc6
  op:260 da06f66a
  op:270 2170dadc
  op:280 0452369a
  op:290 56530950
  op:300 efb69bb0
  op:310 1c213718
  op:320 a57ea907
  op:330 8607c641
  op:340 c5217785
  op:350 9265d546
  op:360 5677dd2a
  op:370 73be45c1
  op:380 13c6144c
  op:390 afcd4201
  op:400 aa15005a
  op:410 259eaf67
  op:420 fe04db8a
  op:430 da8af811
  op:440 e9ed876b
  op:450 31de6f67
  op:460 b4567e57
  op:470 5c14ea94
  op:480 5c14ea94
`

// goldenCard8InReview — та же карточка с approval_state=in_review и БОЛЬШЕ НИЧЕМ.
// Единственная разница с формой выше — развёрнутый readiness (§3.0): три находки вместо одной.
const goldenCard8InReview = `FINDINGS (21)
  [error/bom_mismatch] The route sets hardware; the BOM has none | refs: op:470, op:480
  [error/parameter] Seam class ss_plain is not producible on overlock | refs: op:210
  [error/parameter] Seam class ss_plain is not producible on overlock | refs: op:220
  [warning/bom_mismatch] 44 sewing operations, zero thread lines | refs: card
  [warning/bom_mismatch] Cutting wastage is not stated on 4 of 4 roll-goods lines | refs: bom:Плечевая, bom:основная ткань, bom:подкладка
  [warning/bom_mismatch] Fusing is stated in 1 of the 3 places that describe it | refs: bom:Плечевая
  [warning/bom_mismatch] PLN has no rate to EUR: 3 lines drop out of the cost total | refs: bom:Карманка, bom:Плечевая, bom:основная ткань
  [warning/naming] Unit keys differ only in case: "Base" and "base" | refs: op:270, op:450, unit:Base, unit:base
  [warning/parameter] As written the garment has exactly one button | refs: op:480
  [warning/parameter] As written the garment has exactly one buttonhole | refs: op:470
  [warning/parameter] Buttonhole unspecified: no style, no cut length | refs: op:470
  [warning/parameter] Instruction lives only in a note: thread tension | refs: op:40
  [warning/parameter] Pressing parameters missing on 4 of 4 pressing operations | refs: op:100, op:50, op:70
  [warning/question] "Карманка" costs more per metre than the main fabric | refs: bom:Карманка, bom:основная ткань
  [warning/question] Is "подкладка" priced or is that a placeholder? | refs: bom:подкладка
  [warning/readiness] No equipment profiles on a card that names 4 machine types | refs: card | clause: no equipment profiles
  [warning/readiness] No standard time on 48 of 48 operations | refs: op:10, op:20, op:30 | clause: SMV 0/48
  [warning/readiness] No work assigned on 43 of 48 operations | refs: op:10, op:20, op:30 | clause: works 5/48
  [warning/readiness] The card carries no technical sketch | refs: card | clause: no technical sketch
  [warning/readiness] The print packet would go out with 5 empty sections | refs: card | clause: print packet has 5 empty sections
  [warning/readiness] The route ends with the last seam and has no finishing block | refs: card | clause: no finishing block
OBSERVATIONS (8)
  Lexical mirror pairing over unit names suggests left/right twins: 60<->90, 70<->100, 80<->150, 110<->120, 170<->180, 190<->200, 210<->220, 290<->?, 300<->?, 310<->320, 330<->?, 360<->370, 380<->390, 420<->430 (<->? means the pairer found no partner). The pairing is derived from NAMES only; the input lists are the ground truth - correct it freely.
  Method differs inside suspected twins: op 70 is press_open, op 100 is press.
  Suspected twins are split along the route: ops 80 and 150 are separated by ops 90-140.
  Capitalisation differs inside suspected twins: op 190 "Right front panel with pockets" vs op 200 "LEft front panel with pockets"; op 210 "Right front panel with pockets" vs op 220 "LEft front panel with pockets"; op 360 "left sleeve lining" vs op 370 "Right sleeve lining".
  Lexical mirror pairing over cut-piece names matched 18 _L/_R pairs; 2 side-named pieces have no twin under this rule: BP_LIN_L_2, BP_LIN_R_1. The names are the only evidence here - a twin with a different suffix reads as unpaired.
  Suspected typo: irregular capitalisation in "LEft" inside unit key "LEft front panel with pockets" (produced by op 200; consumed by ops 220, 270).
  Suspected typo: unit keys "Back" (op 30) and "Base" (op 270) differ by 2 edit(s).
  Suspected typo: unit keys "lining back" (op 340) and "lining base" (op 350) differ by 2 edit(s).
NOT CHECKED (4)
  sketch (not reviewed: the analysis path is text-only)
  piece geometry (contours are not stored; measured areas are areas, not outlines)
  labour cost (the card carries no tech_card_costing row: neither the CMT figure nor its SMV backing was checked)
  work ↔ machine legality (this process has not loaded the work catalog)
FINGERPRINTS (48)
  op:10 8a6455c6
  op:20 33aa8f05
  op:30 1bd85c4d
  op:40 47c9055b
  op:50 47c9055b
  op:60 98b88838
  op:70 850e2453
  op:80 a8fa31f4
  op:90 754647a8
  op:100 61bb2124
  op:110 63221d6c
  op:120 09e1983c
  op:130 cc5949fb
  op:140 36e53a53
  op:150 53566e05
  op:160 e3296a7f
  op:170 ed271761
  op:180 661399a9
  op:190 c5219541
  op:200 928796bd
  op:210 46a915ad
  op:220 88a5eecd
  op:230 31ecd7a4
  op:240 4fc20f6e
  op:250 38e90fc6
  op:260 da06f66a
  op:270 2170dadc
  op:280 0452369a
  op:290 56530950
  op:300 efb69bb0
  op:310 1c213718
  op:320 a57ea907
  op:330 8607c641
  op:340 c5217785
  op:350 9265d546
  op:360 5677dd2a
  op:370 73be45c1
  op:380 13c6144c
  op:390 afcd4201
  op:400 aa15005a
  op:410 259eaf67
  op:420 fe04db8a
  op:430 da8af811
  op:440 e9ed876b
  op:450 31de6f67
  op:460 b4567e57
  op:470 5c14ea94
  op:480 5c14ea94
`

// gdCollapsedDraftDetail is the one prose string the golden pins on purpose: the collapsed draft
// finding of §3.0 IS its enumeration, and a collapse that lost a clause would still look like a
// perfectly good finding in the projection above.
const gdCollapsedDraftDetail = "Not yet ready for release: SMV 0/48 · works 5/48 · " +
	"no equipment profiles · no technical sketch · print packet has 5 empty sections · no finishing block"

func TestGoldenCard8DraftCollapsedText(t *testing.T) {
	res := RunAudit(card8(), gdFx)

	var collapsed []Finding
	for _, f := range res.Findings {
		if f.Category == CategoryReadiness {
			collapsed = append(collapsed, f)
		}
	}
	if len(collapsed) != 1 {
		t.Fatalf("на черновике класс readiness обязан быть ОДНОЙ находкой (§3.0), got %d:\n%s",
			len(collapsed), gdDump(res, true))
	}
	if collapsed[0].Title != collapsedReadinessTitle {
		t.Errorf("title схлопнутой = %q, want %q", collapsed[0].Title, collapsedReadinessTitle)
	}
	if collapsed[0].Detail != gdCollapsedDraftDetail {
		t.Errorf("текст схлопнутой находки поехал:\n got: %q\nwant: %q", collapsed[0].Detail, gdCollapsedDraftDetail)
	}
	if collapsed[0].Clause != "" {
		t.Errorf("схлопнутая находка сама клаузы не несёт (её нечему схлопывать), got %q", collapsed[0].Clause)
	}
}

// TestGoldenCard8OrderIsStableAcrossRuns pins the DETERMINISM of the display order without pinning
// the order itself (§11 — сортировку решает клиент). Слой always-on: список, который тасуется на
// одних и тех же данных, читается технологом как «что-то изменилось» там, где не менялось ничто.
func TestGoldenCard8OrderIsStableAcrossRuns(t *testing.T) {
	first := gdDump(RunAudit(card8(), gdFx), false)
	second := gdDump(RunAudit(card8(), gdFx), false)
	if first != second {
		gdAssertGolden(t, "два прогона на одной карточке разошлись", first, second)
	}
	// Худшее — первым: иначе секция открывается предупреждением о лейблах над ошибкой шва.
	res := RunAudit(card8(), gdFx)
	if len(res.Findings) == 0 || severityRank(res.Findings[0].Severity) != severityRank(SeverityError) {
		t.Errorf("первой находкой обязана идти самая тяжёлая (на карточке 8 — error), got %q",
			gdFindingLine(res.Findings[0]))
	}
}

// ── АГРЕГАЦИЯ: ОДНА НАХОДКА, А НЕ РОССЫПЬ ───────────────────────────────────────────────────────
//
// Дробь в title («4 of 4», «3 line(s)») — не украшение текста, а НЕСУЩАЯ ЧАСТЬ закона §3.0: она
// говорит читателю, что находка одна не потому, что проверка нашла один случай. Поэтому здесь
// закреплены и число находок, и дробь.
func TestGoldenCard8AggregatesInsteadOfSpraying(t *testing.T) {
	res := RunAudit(gdInReview(), gdFx)

	for _, tc := range []struct {
		name, substr string
		want         int
	}{
		// A3: ВТО-шагов четыре (50, 70, 100, 160), находка одна, якорей-образцов три.
		{"A3 pressing", "Pressing parameters missing on 4 of 4 pressing operations", 1},
		// B5в: единица пропуска — ВАЛЮТА. Три PLN-линии лечатся ОДНОЙ строкой курса.
		{"B5в currency", "PLN has no rate to EUR: 3 lines drop out of the cost total", 1},
		// B8: четыре рулонные линии с NULL wastage — одна находка с дробью, а не четыре.
		{"B8 wastage", "Cutting wastage is not stated on 4 of 4 roll-goods lines", 1},
		// C2: ПЯТЬ пустот, а не четыре — пятая базовый размер (base_sample_size_id NULL на проде).
		{"C2 print packet", "The print packet would go out with 5 empty sections", 1},
		// C4: ноль профилей на четыре типа машин.
		{"C4 equipment", "No equipment profiles on a card that names 4 machine types", 1},
		// C7/C8: покрытие считается по ВСЕМУ маршруту, и дробь заголовка называет пропуск, а
		// клауза §3.0 — покрытие («SMV 0/48»). Обе закреплены: они считают РАЗНОЕ намеренно.
		{"C7 standard time", "No standard time on 48 of 48 operations", 1},
		{"C8 works", "No work assigned on 43 of 48 operations", 1},
		// C9 — не покрытие, а факт: финишных шагов не бывает сорок восемь. Одна находка, якорь card.
		{"C9 finishing block", "The route ends with the last seam and has no finishing block", 1},
		// A2 и A4, напротив, агрегации НЕ достигают: по три и по два случая — пер-операционно.
		// Заголовок A2 называет то, чего ровно одна, ПО СВОЕМУ ШАГУ — иначе на петельной операции
		// он говорил бы про пуговицы.
		{"A2 one buttonhole (470)", "As written the garment has exactly one buttonhole", 1},
		{"A2 one button (480)", "As written the garment has exactly one button", 1},
		{"A4 seam class", "Seam class ss_plain is not producible on overlock", 2},
	} {
		got := 0
		for _, f := range res.Findings {
			if f.Title == tc.substr {
				got++
			}
		}
		if got != tc.want {
			t.Errorf("%s: находок с title %q — %d, want %d\n%s", tc.name, tc.substr, got, tc.want,
				gdDump(res, true))
		}
	}

	// Якоря-образцы агрегированных находок капятся ТРЕМЯ (§3.0) — кроме B5в, где якорями идут ВСЕ
	// линии валюты: три PLN-линии чинит одна строка курса, и показать надо все три.
	if f := gdOne(t, res.Findings, "Pressing parameters missing"); len(f.Refs) != 3 {
		t.Errorf("A3: якорей-образцов %d, want 3: %v", len(f.Refs), f.Refs)
	}
	if f := gdOne(t, res.Findings, "Cutting wastage is not stated"); len(f.Refs) != 3 {
		t.Errorf("B8: якорей-образцов %d, want 3 (линий четыре): %v", len(f.Refs), f.Refs)
	}
	if f := gdOne(t, res.Findings, "PLN has no rate"); len(f.Refs) != 3 {
		t.Errorf("B5в: якорей %d, want 3 — ВСЕ PLN-линии: %v", len(f.Refs), f.Refs)
	}
}

// gdOne returns the single finding whose title contains substr, failing loudly otherwise.
func gdOne(t *testing.T, fs []Finding, substr string) Finding {
	t.Helper()
	var hits []Finding
	for _, f := range fs {
		if strings.Contains(f.Title, substr) {
			hits = append(hits, f)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("находок с %q — %d, want 1", substr, len(hits))
	}
	return hits[0]
}

// ── МОЛЧАНИЕ, ЗАКРЕПЛЁННОЕ ПОИМЁННО ─────────────────────────────────────────────────────────────
//
// «Всё остальное отсутствует» — утверждение, которое зелено и на пустом реестре проверок. Поэтому
// здесь перечислены ТЕ САМЫЕ СТРОКИ, которые выпустила бы каждая молчащая проверка, если бы
// заговорила. Список закрывает «молчит где должен» из §14 и плана §4.
func TestGoldenCard8Silences(t *testing.T) {
	// Проверяем на РАЗВЁРНУТОЙ форме: на черновике readiness схлопнут, и молчание C1/C3 было бы
	// неотличимо от их растворения в схлопнутой находке.
	res := RunAudit(gdInReview(), gdFx)

	for _, tc := range []struct{ check, substr string }{
		{"A6 (пакующих шагов в маршруте нет)", "after packing"},
		{"A7 (fusing-операций нет вовсе)", "Fusing applied to"},
		{"A8 (пять назначенных пар легальны)", "does not belong to a"},
		{"A8 (машина работы)", "does not run on a"},
		{"A8 (агрегат)", "disagree with the catalog"},
		{"A9 (легаси-связи в локстепе с 0307)", "Legacy piece links diverge"},
		{"A10 (wet_process в маршруте нет)", "before wet processing"},
		{"B1-обратная (линий фурнитуры нет, но и обратная сторона молчит)", "The BOM buys hardware"},
		{"B4 (счёты петель и пуговиц NULL — работает A2)", "buttonholes against"},
		{"B4 (установка больше закупки)", "set than bought"},
		{"B6 (строки костинга нет — сказано в not_checked)", "Cost estimate is materials-only"},
		{"B7 (cmt_cost не задан — опоры не у чего требовать)", "CMT is quoted with SMV"},
		{"B8 (> 30% — процентов нет вовсе)", "wastage is above 30"},
		{"B8 (> 30%, пер-линейно)", "in the cut — is that right?"},
		{"C1 (операции есть)", "The card has no operations"},
		{"C1 (детали есть)", "The card has no cut pieces"},
		{"C1 (ряд s/m/l/xl с прода)", "The card declares no size range"},
		{"C3 (гейт стадии: proto < sms)", "with no labels anywhere"},
		// Подстрока обязана ловить И пер-спековую форму («The care label spec IS not
		// linked…»), И агрегатную («3 of 5 label specs ARE not linked…»): проба, слепая к
		// агрегату, зелена ровно на той карточке, где находок много.
		{"C3 (спека без линии, обе формы)", "not linked to a BOM line"},
		{"C3 (линия без спеки)", "is a label nothing describes"},
		{"C4-error (мягких ссылок на профили нет)", "profile the card does not have"},
		{"C4-error (агрегат)", "references point at nothing"},
		{"C4-info (профилей ноль — наследовать не из чего)", "could inherit"},
		{"C6 (сборка сходится в один терминал «blazer»)", "does not converge into one garment"},
		{"C6 (все 48 деталей потреблены ровно по разу)", "never sewn into anything"},
		// Топология в сохранённой карточке непредставима (§1): дублей производителя, циклов и
		// ссылок вперёд не бывает, и машинный слой их НЕ ищет. 250/260 — легальное поглощение.
		{"дубль производителя «pocket base» (250/260)", "duplicate producer"},
		{"дубль производителя, второе имя", "produced twice"},
		// Симметрия кроя — пункт бэклога §3.5, и правило там прямо гласит: `identical` на
		// РАЗДЕЛЬНЫХ L/R-рядах КОРРЕКТЕН, так его ставит импорт. Проверки нет и быть не должно.
		{"симметрия кроя на раздельных L/R-рядах", "cut_symmetry"},
		{"симметрия кроя, словом", "symmetry"},
	} {
		for _, f := range res.Findings {
			if strings.Contains(f.Title, tc.substr) || strings.Contains(f.Detail, tc.substr) {
				t.Errorf("%s обязана молчать на карточке 8, а выпустила: %s",
					tc.check, gdFindingLine(f))
			}
		}
	}

	// Ни одна находка не якорится на 250/260: если когда-нибудь кто-то решит, что поглощение —
	// дефект, он споткнётся здесь, а не на бете.
	for _, f := range res.Findings {
		for _, r := range f.Refs {
			if r == RefOp(250) || r == RefOp(260) {
				t.Errorf("оп 250/260 — легальное поглощение «pocket base», а не находка: %s",
					gdFindingLine(f))
			}
		}
	}

	// Категории, которых на этой карточке нет ни одной. sequence — это A6/A7/A10; integrity —
	// A8/A9/C4-error; assembly — C6. Проверка дублирует список выше НАМЕРЕННО: новая проверка тех
	// же классов, добавленная завтра, заговорит здесь, даже если её текст никто не предвидел.
	for _, f := range res.Findings {
		switch f.Category {
		case CategorySequence, CategoryIntegrity, CategoryAssembly,
			CategoryMissingStep, CategoryCoarseStep, CategoryMethod:
			t.Errorf("категория %q на карточке 8 не выпускается ни одной проверкой: %s",
				f.Category, gdFindingLine(f))
		}
	}
}

// TestGoldenCard8SilenceIsGrounded проверяет, что молчание выше — СТРУКТУРНОЕ, а не совпадение.
// Проба «находки нет» зелена и когда проверка мертва; здесь закреплено, ПОЧЕМУ её нет.
func TestGoldenCard8SilenceIsGrounded(t *testing.T) {
	c := card8()

	verbs := map[entity.TechCardOperationType]int{}
	for i := range c.Operations {
		verbs[c.Operations[i].OperationType]++
	}
	for _, absent := range []entity.TechCardOperationType{
		entity.OpTypePack,        // A6 молчит: паковать нечего после
		entity.OpTypeFusing,      // A7 молчит: дублирования в маршруте нет вовсе
		entity.OpTypeWetProcess,  // A10 молчит: мокрых процессов нет
		entity.OpTypeHardwareSet, // B1 читает machine_type, а не этот глагол
	} {
		if verbs[absent] != 0 {
			t.Errorf("карточка 8 не несёт шагов %q, а в фикстуре их %d — молчание проверок блока "+
				"перестало быть структурным", string(absent), verbs[absent])
		}
	}

	// 250 и 260 производят ОДИН И ТОТ ЖЕ ключ узла — и это законная запись: 260 поглощает выход
	// 250 и дозагружает деталь. Именно эту форму §14 требует НЕ флагать.
	gt := ComputeGroundTruth(c)
	absorb, ok := gt.StepByNumber(260)
	if !ok || absorb.Kind != StepAbsorbing {
		t.Fatalf("оп 260 обязана классифицироваться поглощением: %+v (найдена: %v)", absorb, ok)
	}
	produced, ok := gt.StepByNumber(250)
	if !ok || produced.OutputUnitKey != absorb.OutputUnitKey {
		t.Fatalf("250 и 260 обязаны производить один ключ — иначе проба про дубль вакуумна: %q vs %q",
			produced.OutputUnitKey, absorb.OutputUnitKey)
	}
	if len(gt.Violations) != 0 {
		t.Errorf("граф карточки 8 формально чист (эталон, «что при этом в порядке»), got %v", gt.Violations)
	}

	// Симметрия кроя: все детали `identical`, левые и правые — ОТДЕЛЬНЫМИ рядами. Это и есть та
	// форма, которую §3.5 запрещает считать дефектом.
	identical, sided := 0, 0
	for i := range c.Pieces {
		if strings.TrimSpace(c.Pieces[i].CutSymmetry.String) == string(entity.PieceCutSymmetryIdentical) {
			identical++
		}
		if strings.HasSuffix(c.Pieces[i].Name, "_L") || strings.HasSuffix(c.Pieces[i].Name, "_R") {
			sided++
		}
	}
	if identical != len(c.Pieces) || sided == 0 {
		t.Errorf("фикстура обязана нести раздельные L/R-ряды с cut_symmetry='identical': "+
			"identical %d из %d, сторонних имён %d", identical, len(c.Pieces), sided)
	}
}

// TestGoldenCard8HeuristicsStayObservations пинит §3.4: эвристики НАБЛЮДАЮТ, а находок не выпускают.
// На этой самой карточке обе они ОШИБАЮТСЯ (парователь спаривает 310↔320, не близнецов; Левенштейн
// «находит» опечатку между законными узлами «lining back» и «lining base»), и это ровно тот случай,
// ради которого разделение существует: находка, требующая починить ВЕРНОЕ, дороже её отсутствия.
func TestGoldenCard8HeuristicsStayObservations(t *testing.T) {
	res := RunAudit(gdInReview(), gdFx)

	for _, f := range res.Findings {
		if f.Confidence != "" {
			t.Errorf("детерминированная находка не несёт confidence; эвристическая не находка вовсе: %s",
				gdFindingLine(f))
		}
		if f.Source != SourceMachine {
			t.Errorf("в машинном прогоне все находки машинные: %s", gdFindingLine(f))
		}
		if len(f.Refs) == 0 {
			t.Errorf("находка без единого якоря дропается верификатором §8 — машинная такой быть не может: %s",
				gdFindingLine(f))
		}
	}

	// Обе ошибающиеся эвристики закреплены КАК НАБЛЮДЕНИЯ — эталонная неправота (T3).
	gdWantObservation(t, res, "310<->320")
	gdWantObservation(t, res, `unit keys "lining back" (op 340) and "lining base" (op 350)`)
}

func gdWantObservation(t *testing.T, res AuditResult, substr string) {
	t.Helper()
	for _, o := range res.Observations {
		if strings.Contains(o, substr) {
			return
		}
	}
	t.Errorf("в блоке наблюдений нет строки с %q:\n%s", substr, strings.Join(res.Observations, "\n"))
}

// TestGoldenCard8NotCheckedShrinksWithTheCatalog: строка «work ↔ machine legality» в not_checked
// голдена стоит там ПОТОМУ, что процесс теста не публиковал каталог 0329, а не потому, что A8
// нечего сказать. Опубликованный каталог убирает строку и не добавляет находки — обе половины.
func TestGoldenCard8NotCheckedShrinksWithTheCatalog(t *testing.T) {
	rtPublishWorks(t, rtCard8Works)

	res := RunAudit(card8(), gdFx)
	if len(res.NotChecked) != 3 {
		t.Errorf("с загруженным каталогом not_checked обязан быть тремя строками, got %d:\n%s",
			len(res.NotChecked), strings.Join(res.NotChecked, "\n"))
	}
	for _, l := range res.NotChecked {
		if strings.Contains(l, "work catalog") {
			t.Errorf("каталог загружен — строке «не проверено» взяться неоткуда: %q", l)
		}
	}
	// И самое главное: пять назначенных работ карточки 8 легальны, находок нет — состав находок
	// совпадает с голденом ЦЕЛИКОМ (сравниваем только блок находок: not_checked как раз изменился).
	gdAssertGolden(t, "каталог загружен: находки те же",
		gdFindingsBlock(goldenCard8Draft), gdFindingsBlock(gdDump(res, true)))
}

// ── МУТАЦИИ: ГОЛДЕН ОБЯЗАН ДВИГАТЬСЯ ────────────────────────────────────────────────────────────
//
// Голден, который не краснеет, — сторож у мёртвого кода. Каждая мутация ниже меняет ОДИН факт
// карточки и утверждает ТОЧНУЮ дельту вердикта: какие строки исчезли, какие появились. Пер-
// проверочные fire-пробы живут в route_test.go / bom_test.go / readiness_test.go и меряют по одной
// проверке; здесь меряется ВЕСЬ результат — включая сцепления, которых пер-проверочная проба не
// видит (снятая операция 480 уводит и находку A2, и якорь B1).

// gdFindingLinesOf renders the sorted finding projection of one run.
func gdFindingLinesOf(c *entity.TechCard, fx Fx) []string {
	res := RunAudit(c, fx)
	out := make([]string, 0, len(res.Findings))
	for _, f := range res.Findings {
		out = append(out, gdFindingLine(f))
	}
	sort.Strings(out)
	return out
}

// gdDropUnitInput removes one unit input from an operation, the way a technologist un-picking an
// input in the form would — both projections of the input list, or the card would contradict itself.
func gdDropUnitInput(op *entity.TechCardOperation, unitKey string) {
	ins := make([]entity.OperationInput, 0, len(op.AssemblyInputs))
	for _, in := range op.AssemblyInputs {
		if in.Kind == entity.AssemblyInputUnit && in.Key == unitKey {
			continue
		}
		ins = append(ins, in)
	}
	if len(ins) == len(op.AssemblyInputs) {
		panic("card8 fixture: operation has no unit input " + unitKey)
	}
	op.AssemblyInputs = ins
	keys := make([]string, 0, len(op.InputKeys))
	for _, k := range op.InputKeys {
		if k == unitKey {
			continue
		}
		keys = append(keys, k)
	}
	op.InputKeys = keys
}

func TestGoldenCard8MutationsMoveTheVerdict(t *testing.T) {
	base := gdFindingLinesOf(card8(), gdFx)
	baseOffDraft := gdFindingLinesOf(gdInReview(), gdFx)

	for _, tc := range []struct {
		name   string
		mutate func(*entity.TechCard)
		fx     *Fx
		// offDraft — мерить на РАЗВЁРНУТОЙ форме. Нужен для класса readiness: на черновике он
		// схлопнут в одну строку, проекция которой не меняется от того, какая из клауз ушла, и
		// мутация выглядела бы no-op'ом. Клаузы схлопнутой закреплены отдельно
		// (TestGoldenCard8DraftCollapsedText) — это две разные пробы одного факта.
		offDraft     bool
		lost, gained []string
	}{
		{
			name:   "маршрут A5: близнец ноты заполнен — находка исчезает",
			mutate: func(c *entity.TechCard) { card8OpByNumber(c, 40).ThreadTension = text("lower") },
			lost: []string{
				"[warning/parameter] Instruction lives only in a note: thread tension | refs: op:40",
			},
			gained: nil,
		},
		{
			name:   "маршрут A2 + BOM B1: снята оп 480 — уходит находка и уходит якорь",
			mutate: func(c *entity.TechCard) { card8DropOperation(c, 480) },
			lost: []string{
				"[error/bom_mismatch] The route sets hardware; the BOM has none | refs: op:470, op:480",
				"[warning/bom_mismatch] 44 sewing operations, zero thread lines | refs: card",
				"[warning/parameter] As written the garment has exactly one button | refs: op:480",
			},
			gained: []string{
				"[error/bom_mismatch] The route sets hardware; the BOM has none | refs: op:470",
				"[warning/bom_mismatch] 43 sewing operations, zero thread lines | refs: card",
			},
		},
		{
			name:   "BOM B3: одна деталь помечена fused — дробь трёх мест меняется",
			mutate: func(c *entity.TechCard) { card8PieceByName(c, "BP_L").Fused = true },
			lost: []string{
				"[warning/bom_mismatch] Fusing is stated in 1 of the 3 places that describe it | refs: bom:Плечевая",
			},
			gained: []string{
				"[warning/bom_mismatch] Fusing is stated in 2 of the 3 places that describe it | refs: bom:Плечевая, piece:BP_L",
			},
		},
		{
			name:   "BOM B5в: курс PLN заведён — валютная находка молкнет",
			mutate: func(c *entity.TechCard) {},
			fx:     &Fx{Base: "EUR", ToBase: map[string]decimal.Decimal{"PLN": decimal.RequireFromString("0.23")}},
			lost: []string{
				"[warning/bom_mismatch] PLN has no rate to EUR: 3 lines drop out of the cost total | refs: bom:Карманка, bom:Плечевая, bom:основная ткань",
			},
			gained: nil,
		},
		{
			name:   "готовность C1: размерный ряд обнулён — схлопнутая тяжелеет до error",
			mutate: func(c *entity.TechCard) { c.SizeIds = nil },
			lost: []string{
				"[warning/readiness] Not yet ready for release | refs: card",
			},
			gained: []string{
				"[error/readiness] Not yet ready for release | refs: card",
			},
		},
		{
			name:     "готовность C7: маршрут пронормирован — клауза SMV уходит",
			offDraft: true,
			mutate: func(c *entity.TechCard) {
				for i := range c.Operations {
					c.Operations[i].SMV = dec("1.50")
				}
			},
			lost: []string{
				"[warning/readiness] No standard time on 48 of 48 operations | refs: op:10, op:20, op:30 | clause: SMV 0/48",
			},
		},
		{
			name:     "готовность C8: работы назначены — клауза works уходит",
			offDraft: true,
			mutate: func(c *entity.TechCard) {
				for i := range c.Operations {
					if nsEmpty(c.Operations[i].Work) {
						c.Operations[i].Work = text("join")
					}
				}
			},
			lost: []string{
				"[warning/readiness] No work assigned on 43 of 48 operations | refs: op:10, op:20, op:30 | clause: works 5/48",
			},
		},
		{
			name:     "готовность C9: маршрут закрыт упаковкой — клауза финишного блока уходит",
			offDraft: true,
			mutate: func(c *entity.TechCard) {
				rtAppendOp(c, entity.TechCardOperation{
					OperationType:  entity.OpTypePack,
					AssemblyInputs: []entity.OperationInput{rtUnitInput("blazer")},
					InputKeys:      []string{"blazer"},
				})
			},
			// Добавленный шаг двигает и ЗНАМЕНАТЕЛИ покрытий: 48 → 49. Это не шум, а то самое
			// сцепление, ради которого голден меряет ВЕСЬ результат, а не одну проверку.
			lost: []string{
				"[warning/readiness] No standard time on 48 of 48 operations | refs: op:10, op:20, op:30 | clause: SMV 0/48",
				"[warning/readiness] No work assigned on 43 of 48 operations | refs: op:10, op:20, op:30 | clause: works 5/48",
				"[warning/readiness] The route ends with the last seam and has no finishing block | refs: card | clause: no finishing block",
			},
			gained: []string{
				"[warning/readiness] No standard time on 49 of 49 operations | refs: op:10, op:20, op:30 | clause: SMV 0/49",
				"[warning/readiness] No work assigned on 44 of 49 operations | refs: op:10, op:20, op:30 | clause: works 5/49",
			},
		},
		{
			name:   "готовность C6: подкладка не втачана — два терминала, релиз откажет",
			mutate: func(c *entity.TechCard) { gdDropUnitInput(card8OpByNumber(c, 460), "lining") },
			lost:   nil,
			gained: []string{
				"[warning/assembly] Assembly does not converge into one garment (2 terminals) | refs: unit:base, unit:lining",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := card8()
			from := base
			if tc.offDraft {
				c = gdInReview()
				from = baseOffDraft
			}
			tc.mutate(c)
			fx := gdFx
			if tc.fx != nil {
				fx = *tc.fx
			}
			got := gdFindingLinesOf(c, fx)

			lost, gained := gdMissing(from, got), gdMissing(got, from)
			if len(lost) == 0 && len(gained) == 0 {
				t.Fatalf("мутация не сдвинула вердикт — либо она no-op, либо проверка мертва")
			}
			gdAssertLines(t, "исчезли", tc.lost, lost)
			gdAssertLines(t, "появились", tc.gained, gained)
		})
	}
}

func gdAssertLines(t *testing.T, what string, want, got []string) {
	t.Helper()
	if strings.Join(want, "\n") == strings.Join(got, "\n") {
		return
	}
	t.Errorf("%s не то, что ожидалось:\n want:\n%s\n got:\n%s", what,
		"  "+strings.Join(want, "\n  "), "  "+strings.Join(got, "\n  "))
}

// TestGoldenCard8FingerprintsRepeatOnRepeatedSHAPES закрепляет СВОЙСТВО отпечатка, а не опечатку в
// голдене: две пары шагов карточки 8 несут ОДИН fp8 — 40/50 (оба без выхода, оба от UNIT<Back>) и
// 470/480 (оба без выхода, оба от UNIT<blazer>).
//
// Так устроен payload §9: «tcfp1 ‖ output_unit_key ‖ входы в display_order» и НИЧЕГО больше — ни
// глагола, ни машины, ни ноты. Отпечаток отвечает на вопрос «изменилась ли СБОРОЧНАЯ ФОРМА шага»,
// а не «тот ли это шаг»: 40 — машинная строчка, 50 — разутюжка, и по отпечатку они неразличимы.
// Для механики §9 (амбер «эта операция изменилась с момента прогона») этого достаточно ровно до
// того дня, когда номер переедет С 40 НА 50: тогда клиент сравнит равные отпечатки и решит, что шаг
// не менялся. Это ЗНАЕМАЯ граница дизайна, замороженная T2 и продублированная TS-портом (T21);
// расширять payload здесь нельзя — расхождение с клиентом даст ложный амбер на каждой карточке.
//
// Тест существует затем, чтобы повторяющийся hex в голдене читался как факт, а не как ошибка копии.
func TestGoldenCard8FingerprintsRepeatOnRepeatedShapes(t *testing.T) {
	fps := Fingerprints(card8())
	if len(fps) != 48 {
		t.Fatalf("отпечатков %d, карточка 8 несёт 48 шагов", len(fps))
	}
	for _, pair := range [][2]int32{{40, 50}, {470, 480}} {
		if fps[pair[0]] != fps[pair[1]] {
			t.Errorf("шаги %d и %d несут одну сборочную форму — отпечаток обязан совпасть: %q vs %q",
				pair[0], pair[1], fps[pair[0]], fps[pair[1]])
		}
	}
	// И наоборот: 48 шагов дают 46 различных отпечатков, ровно две пары совпадают.
	distinct := map[string]bool{}
	for _, fp := range fps {
		distinct[fp] = true
	}
	if len(distinct) != 46 {
		t.Errorf("различных отпечатков %d, want 46 (две пары повторяющихся форм)", len(distinct))
	}
}
