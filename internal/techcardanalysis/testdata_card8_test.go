package techcardanalysis

import (
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ── ФИКСТУРА: ТЕХ-КАРТА 8 (SS26-008 Blazer), 48 операций ────────────────────────────────────────
//
// Собрана по ПРОДОВОМУ ДАМПУ карточки (plans/techcard-analysis/02-fixture-constr-dump.txt) и сверена
// с рендером §7.2 дизайна, который авторитетен по входам, выходам, нотам и зонам. Это единственная
// карточка, на которой измеряется весь машинный слой, поэтому расхождение фикстуры с дампом
// обесценивает КАЖДЫЙ тест пакета, а не один.
//
// БИЛДЕР, А НЕ testdata-JSON. Типы entity растут по колонке в неделю (0324 добавила тридцать две).
// JSON-фикстура пережила бы такую волну молча, продолжая описывать карточку, которой больше не
// бывает; Go-билдер перестаёт компилироваться в тот же день. Ровно та же причина, по которой
// сборочные факты живут структурами, а не картой строк.
//
// ЧЕГО В ФИКСТУРЕ НЕТ, И ЭТО ФАКТ КАРТОЧКИ, А НЕ ЛЕНЬ: костинга (B6/B7 обязаны молчать «данных
// нет»), лейблов, упаковки, реестра issues, профилей оборудования (0 на 4 типа машин),
// technical-медиа, БАЗОВОГО РАЗМЕРА. Ни одну из этих пустот не заполнять «чтобы тест был полнее» —
// на них стоят приёмки T4.
//
// РАЗМЕРНЫЙ РЯД СНЯТ НЕ С ДАМПА. Дамп секции размеров не печатает вовсе, и первая редакция фикстуры
// молча ставила здесь nil — то есть УТВЕРЖДАЛА «размерного ряда нет», чего никто не измерял. Правда
// снята с прод-БД read-only 2026-08-24: `tech_card_size` карточки 8 — четыре строки, size_id 3, 4,
// 5, 6 (s, m, l, xl), а `tech_card.base_sample_size_id` — NULL. Второе тоже ФАКТ ПРОДА, а не
// пропуск: карточка 8 базового размера не несёт, и C2 обязана считать это пятой пустотой печатного
// пакета (см. абзац «для T4/T5» в 06-PROGRESS.md — пример §3.3 перечисляет только четыре).

// card8PieceKeyPrefix + %05d даёт синтетический 26-символьный ключ формата ULID (алфавит Крокфорда,
// заглавные, без I/L/O/U), СТАБИЛЬНЫЙ между прогонами и прослеживаемый до id детали в дампе.
//
// Синтетический потому, что дамп line_key деталей не печатает: у отпечатка §9 длина ключа значения
// не имеет, а вот его стабильность имеет — канонический вектор отпечатка проверяется отдельно, на
// собственных литералах, и не зависит от этой формулы.
const card8PieceKeyPrefix = "01M0TCP80000000000000"

func card8PieceKey(pieceID int) string {
	return fmt.Sprintf("%s%05d", card8PieceKeyPrefix, pieceID)
}

// BOM line keys — ДОСЛОВНО из дампа.
const (
	card8BomLining      = "01M082JJC5J0MFRKA32TM4GMHJ" // подкладка,      1.0000 EUR/m
	card8BomMain        = "01M082JZFJZPV2KSM5HJAEFAZY" // основная ткань, 55.0000 PLN/m
	card8BomInterlining = "01M082KWYFHQ9T4PNW2NP9184D" // Плечевая,       36.0000 PLN/m
	card8BomPocketing   = "01M082MJ51BJWQQWGXEGSZT0WY" // Карманка,       60.0000 PLN/m
)

// card8SizeIds — объявленный размерный ряд карточки 8: size_id 3, 4, 5, 6 (s, m, l, xl). Снято с
// прод-БД, потому что дамп секцию размеров не содержит (см. шапку файла).
var card8SizeIds = []int{3, 4, 5, 6}

// text stores a value that EXISTS in the column, empty string included. Дамп печатает NULL словом
// «NULL», а пустую строку — пробелами, и 43 ноты карточки 8 это именно пустые строки; свернуть их в
// NULL значило бы тихо починить фикстуру под удобное чтение.
func text(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// nullText is the column that holds NULL.
func nullText() sql.NullString { return sql.NullString{} }

// dec parses a stored DECIMAL literal.
func dec(s string) decimal.NullDecimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic("card8 fixture: bad decimal " + s + ": " + err.Error())
	}
	return decimal.NullDecimal{Decimal: d, Valid: true}
}

// card8Piece is one row of the pieces table of the dump.
type card8Piece struct {
	id       int
	name     string
	ungraded bool
	scope    string // ведро ткани из дампа; в entity колонки нет — резолвится через рецепт
}

// card8Pieces — 48 строк дампа в том же порядке (по scope, внутри — по имени), потому что порядок
// объявления деталей это порядок, в котором правило 4 перечисляет сирот.
var card8Pieces = []card8Piece{
	{36, "BP_L", false, "main"},
	{37, "BP_R", false, "main"},
	{38, "BPU_L", false, "main"},
	{39, "BPU_R", false, "main"},
	{40, "CLR_INS", false, "main"},
	{41, "CLR_INS_1", false, "main"},
	{42, "CLR_MAIN", false, "main"},
	{43, "CLR_MAIN_1", false, "main"},
	{44, "INS_D_PCK", false, "main"},
	{45, "INS_U_PCK", false, "main"},
	{46, "MP_L", false, "main"},
	{47, "MP_R", false, "main"},
	{48, "P_L", false, "main"},
	{49, "P_LIN_L_2", false, "main"},
	{50, "P_LIN_R_2", false, "main"},
	{51, "P_R", false, "main"},
	{52, "PCK_MAIN_L_1", false, "main"},
	{53, "PCK_MAIN_R_1", false, "main"},
	{54, "SL_INS_L", false, "main"},
	{55, "SL_INS_R", false, "main"},
	{56, "SL_LIN_L", false, "main"},
	{57, "SL_LIN_R", false, "main"},
	{58, "SL_OUT_L", false, "main"},
	{59, "SL_OUT_R", false, "main"},
	{60, "BP_LIN_L_2", false, "lining"},
	{61, "BP_LIN_R_1", false, "lining"},
	{62, "MP_LIN_L_1", false, "lining"},
	{63, "MP_LIN_R_1", false, "lining"},
	{64, "PCK_LOCKER", false, "lining"},
	{65, "PD_LIN_L_2", false, "lining"},
	{66, "PD_LIN_R_2", false, "lining"},
	{67, "PU_LIN_L", false, "lining"},
	{68, "PU_LIN_R", false, "lining"},
	{69, "SL_INS_LIN_L", false, "lining"},
	{70, "SL_INS_LIN_R", false, "lining"},
	{71, "SL_OUT_LIN_L", false, "lining"},
	{72, "SL_OUT_LIN_R", false, "lining"},
	{73, "PCK_INS_S", true, "pocketing"},
	{74, "PCK_MAIN_INS_S", true, "pocketing"},
	{75, "PCK_MAIN_L_S", true, "pocketing"},
	{76, "PCK_MAIN_R_S", true, "pocketing"},
	{77, "PCK_OUT_L_S", true, "pocketing"},
	{78, "PCK_OUT_R_S", true, "pocketing"},
	{79, "PCK_UP_INS_S", true, "pocketing"},
	{80, "SHLD_1_L", false, "other"},
	{81, "SHLD_1_R", false, "other"},
	{82, "SHLD_L", false, "other"},
	{83, "SHLD_R", false, "other"},
}

// card8ScopeToBom maps the dump's scope column to the BOM line that carries that назначение. Ведро
// НЕ хранится на детали: в entity такой колонки нет — привязка живёт в строке рецепта колорвея
// (usage.piece_id), и ведро резолвится из назначения строки BOM (entity.FabricScopeIdentity).
var card8ScopeToBom = map[string]string{
	"main":      card8BomMain,
	"lining":    card8BomLining,
	"pocketing": card8BomPocketing,
	"other":     card8BomInterlining, // «Плечевая», purpose other («Плечи»)
}

// pc / un spell one operation input: a cut piece BY NAME, or a unit BY KEY. Явные конструкторы, а
// не догадка по виду строки: ключ узла карточки 8 («base», «Base») формой от имени детали не
// отличается ничем, и однажды отличаться перестанет вовсе.
func pc(pieceName string) string { return "p:" + pieceName }
func un(unitKey string) string   { return "u:" + unitKey }

// card8Op is one row of the operations table of the dump.
type card8Op struct {
	num     int32
	work    string // "" = NULL: вид работы не назначен (законное состояние, каталог 0329)
	otype   entity.TechCardOperationType
	zone    entity.TechCardGarmentZone
	machine string   // "" = NULL
	out     string   // "" = ОБРАБОТКА (пустой output_unit_key)
	note    string   // хранимая строка; в дампе она пуста у 43 шагов и НЕ NULL
	inputs  []string // pc(...) / un(...) в порядке display_order
	bom     []string // line_key строк BOM, привязанных к шагу
}

// card8Ops — 48 операций дампа в порядке display_order. Номер = (i+1)*10, ровно как его штампует
// запись.
var card8Ops = []card8Op{
	{num: 10, otype: "machine", zone: "back", machine: "lockstitch", out: "Back Panels Upper",
		inputs: []string{pc("BPU_L"), pc("BPU_R")}},
	{num: 20, otype: "machine", zone: "back", machine: "lockstitch", out: "Back panels bottom",
		inputs: []string{pc("BP_L"), pc("BP_R")}},
	{num: 30, otype: "machine", zone: "back", machine: "lockstitch", out: "Back",
		inputs: []string{un("Back panels bottom"), un("Back Panels Upper")}},
	{num: 40, otype: "machine", zone: "back", machine: "lockstitch", note: "Low thread tension stich",
		inputs: []string{un("Back")}},
	{num: 50, work: "press_open", otype: "press_open", zone: "back",
		inputs: []string{un("Back")}},
	{num: 60, otype: "machine", zone: "pocket", machine: "lockstitch", out: "right main pocket detail",
		inputs: []string{pc("PCK_MAIN_R_S"), pc("PCK_MAIN_R_1")}},
	{num: 70, work: "press_open", otype: "press_open", zone: "pocket",
		inputs: []string{un("right main pocket detail")}},
	{num: 80, otype: "machine", zone: "front", machine: "lockstitch", out: "right front panel with pocket detail",
		inputs: []string{pc("PCK_OUT_R_S"), pc("P_R")}},
	{num: 90, otype: "machine", zone: "pocket", machine: "lockstitch", out: "left main pocket detail",
		inputs: []string{pc("PCK_MAIN_L_S"), pc("PCK_MAIN_L_1")}},
	{num: 100, otype: "press", zone: "pocket",
		inputs: []string{un("left main pocket detail")}},
	{num: 110, otype: "machine", zone: "sleeve", machine: "lockstitch", out: "left sleeve", note: "front seam",
		inputs: []string{pc("SL_INS_L"), pc("SL_OUT_L")}},
	{num: 120, otype: "machine", zone: "sleeve", machine: "lockstitch", out: "right sleeve", note: "front seam",
		inputs: []string{pc("SL_OUT_R"), pc("SL_INS_R")}},
	{num: 130, otype: "machine", zone: "collar", machine: "lockstitch", out: "Collar inner",
		inputs: []string{pc("CLR_MAIN_1"), pc("CLR_INS_1")}},
	{num: 140, otype: "machine", zone: "collar", machine: "lockstitch", out: "collar outer",
		inputs: []string{pc("CLR_MAIN"), pc("CLR_INS")}},
	{num: 150, otype: "machine", zone: "front", machine: "lockstitch", out: "left front panel with pocket detail",
		inputs: []string{pc("PCK_OUT_L_S"), pc("P_L")}},
	{num: 160, work: "press_open", otype: "press_open", zone: "collar",
		inputs: []string{un("collar outer")}},
	{num: 170, otype: "machine", zone: "outer", machine: "lockstitch", out: "Right side with pocket detail",
		inputs: []string{un("right main pocket detail"), pc("MP_R")}},
	{num: 180, otype: "machine", zone: "outer", machine: "lockstitch", out: "Left side with pocket detail",
		inputs: []string{un("left main pocket detail"), pc("MP_L")}},
	{num: 190, otype: "machine", zone: "front", machine: "lockstitch", out: "Right front panel with pockets",
		inputs: []string{un("right front panel with pocket detail"), un("Right side with pocket detail")}},
	{num: 200, otype: "machine", zone: "front", machine: "lockstitch", out: "LEft front panel with pockets",
		inputs: []string{un("left front panel with pocket detail"), un("Left side with pocket detail")}},
	{num: 210, otype: "machine", zone: "pocket", machine: "overlock", note: "join pockets",
		inputs: []string{un("Right front panel with pockets")}},
	{num: 220, otype: "machine", zone: "pocket", machine: "overlock", note: "join pockets",
		inputs: []string{un("LEft front panel with pockets")}},
	{num: 230, otype: "machine", zone: "pocket", machine: "lockstitch", out: "Pocket detail inside",
		inputs: []string{pc("PCK_INS_S"), pc("INS_D_PCK")}},
	{num: 240, otype: "machine", zone: "pocket", machine: "lockstitch", out: "Pocket piece with flap",
		inputs: []string{pc("PCK_LOCKER"), pc("PCK_UP_INS_S"), pc("INS_U_PCK")}},
	{num: 250, otype: "machine", zone: "pocket", machine: "lockstitch", out: "pocket base",
		inputs: []string{un("Pocket detail inside"), un("Pocket piece with flap")}},
	// 260 — ПОГЛОЩЕНИЕ: тот же ключ на выходе, что и на входе-узле, плюс догруженная деталь.
	{num: 260, otype: "machine", zone: "pocket", machine: "lockstitch", out: "pocket base",
		inputs: []string{pc("PCK_MAIN_INS_S"), un("pocket base")}},
	{num: 270, otype: "machine", zone: "outer", machine: "lockstitch", out: "Base",
		inputs: []string{un("Back"), un("Right front panel with pockets"), un("LEft front panel with pockets")}},
	{num: 280, otype: "machine", zone: "outer", machine: "lockstitch", out: "base with collar",
		inputs: []string{un("collar outer"), un("Base")}},
	{num: 290, otype: "machine", zone: "pocket", machine: "lockstitch", out: "left lining detail with pocket",
		inputs: []string{un("pocket base"), pc("P_LIN_L_2")}},
	{num: 300, otype: "machine", zone: "lining", machine: "lockstitch", out: "left lining with pocket",
		inputs: []string{pc("PD_LIN_L_2"), pc("PU_LIN_L"), un("left lining detail with pocket")}},
	{num: 310, otype: "machine", zone: "lining", machine: "lockstitch", out: "right lining",
		inputs: []string{pc("PU_LIN_R"), pc("P_LIN_R_2"), pc("PD_LIN_R_2")}},
	{num: 320, otype: "machine", zone: "lining", machine: "lockstitch", out: "left lining",
		inputs: []string{pc("MP_LIN_L_1"), pc("BP_LIN_L_2")}},
	{num: 330, otype: "machine", zone: "lining", machine: "lockstitch", out: "right lining panels",
		inputs: []string{pc("BP_LIN_R_1"), pc("MP_LIN_R_1")}},
	{num: 340, otype: "machine", zone: "lining", machine: "lockstitch", out: "lining back",
		inputs: []string{un("left lining"), un("right lining panels")}},
	{num: 350, otype: "machine", zone: "lining", machine: "lockstitch", out: "lining base",
		inputs: []string{un("left lining with pocket"), un("right lining"), un("lining back")}},
	{num: 360, otype: "machine", zone: "lining", machine: "lockstitch", out: "left sleeve lining",
		inputs: []string{pc("SL_INS_LIN_L"), pc("SL_OUT_LIN_L")}},
	{num: 370, otype: "machine", zone: "lining", machine: "lockstitch", out: "Right sleeve lining",
		inputs: []string{pc("SL_INS_LIN_R"), pc("SL_OUT_LIN_R")}},
	{num: 380, otype: "machine", zone: "lining", machine: "lockstitch", out: "right sleeve lining main",
		inputs: []string{un("Right sleeve lining"), pc("SL_LIN_R")}},
	{num: 390, otype: "machine", zone: "lining", machine: "lockstitch", out: "left sleeve lining main",
		inputs: []string{un("left sleeve lining"), pc("SL_LIN_L")}},
	{num: 400, otype: "machine", zone: "lining", machine: "lockstitch", out: "lining with sleeves",
		inputs: []string{un("left sleeve lining main"), un("right sleeve lining main"), un("lining base")}},
	{num: 410, otype: "machine", zone: "lining", machine: "lockstitch", out: "lining",
		inputs: []string{un("Collar inner"), un("lining with sleeves")}},
	{num: 420, otype: "machine", zone: "interlining", machine: "lockstitch", out: "underlining left",
		inputs: []string{pc("SHLD_1_L"), pc("SHLD_L")}, bom: []string{card8BomInterlining}},
	{num: 430, otype: "machine", zone: "interlining", machine: "lockstitch", out: "underlining right",
		inputs: []string{pc("SHLD_1_R"), pc("SHLD_R")}, bom: []string{card8BomInterlining}},
	{num: 440, otype: "machine", zone: "outer", machine: "lockstitch", out: "base with underlining",
		inputs: []string{un("underlining left"), un("underlining right"), un("base with collar")}},
	{num: 450, otype: "machine", zone: "outer", machine: "lockstitch", out: "base",
		inputs: []string{un("left sleeve"), un("right sleeve"), un("base with underlining")}},
	{num: 460, otype: "machine", zone: "outer", machine: "lockstitch", out: "blazer",
		inputs: []string{un("lining"), un("base")}},
	{num: 470, work: "buttonhole", otype: "machine", zone: "front", machine: "buttonhole",
		inputs: []string{un("blazer")}},
	{num: 480, work: "button_attach", otype: "machine", zone: "front", machine: "button_attach",
		inputs: []string{un("blazer")}},
}

// card8 builds the fixture card as GetTechCardById hands it over: hydrated, canonical, read-side.
//
// Каждый вызов строит НОВУЮ карточку: тесты мутируют фикстуру (это и есть fire-сторона приёмки), и
// общий указатель протёк бы правкой одного теста в другой.
func card8() *entity.TechCard {
	pieceIDByName := make(map[string]int, len(card8Pieces))
	pieces := make([]entity.TechCardPiece, 0, len(card8Pieces))
	for _, p := range card8Pieces {
		pieceIDByName[p.name] = p.id
		pieces = append(pieces, entity.TechCardPiece{
			Id:               p.id,
			Name:             p.name,
			LineKey:          card8PieceKey(p.id),
			PiecesPerGarment: 1,
			CutSymmetry:      text(string(entity.PieceCutSymmetryIdentical)),
			Ungraded:         p.ungraded,
			Grainline:        "lengthwise",
			Fused:            false,
			CalloutNumber:    sql.NullInt32{},
			Note:             nullText(),
		})
	}

	bom := []entity.TechCardBomItem{
		{
			Id: 21, LineKey: card8BomLining, Section: entity.BomSectionLining,
			Purpose: text(string(entity.BomPurposeLining)), PurposeNote: nullText(),
			Name: "подкладка", Unit: text("m"), UnitPrice: dec("1.0000"), Currency: text("EUR"),
		},
		{
			Id: 22, LineKey: card8BomMain, Section: entity.BomSectionFabric,
			Purpose: text(string(entity.BomPurposeMain)), PurposeNote: nullText(),
			Name: "основная ткань", Unit: text("m"), UnitPrice: dec("55.0000"), Currency: text("PLN"),
		},
		{
			Id: 23, LineKey: card8BomInterlining, Section: entity.BomSectionInterlining,
			Purpose: text(string(entity.BomPurposeOther)), PurposeNote: text("Плечи"),
			Name: "Плечевая", Unit: text("m"), UnitPrice: dec("36.0000"), Currency: text("PLN"),
		},
		{
			Id: 24, LineKey: card8BomPocketing, Section: entity.BomSectionFabric,
			Purpose: text(string(entity.BomPurposePocketing)), PurposeNote: nullText(),
			Name: "Карманка", Unit: text("m"), UnitPrice: dec("60.0000"), Currency: text("PLN"),
		},
	}
	bomIDByKey := make(map[string]int, len(bom))
	for i := range bom {
		bomIDByKey[bom[i].LineKey] = bom[i].Id
	}

	ops := make([]entity.TechCardOperation, 0, len(card8Ops))
	for i, o := range card8Ops {
		if want := int32((i + 1) * 10); o.num != want {
			panic(fmt.Sprintf("card8 fixture: operation at display_order %d is numbered %d, want %d",
				i, o.num, want))
		}
		op := entity.TechCardOperation{
			OperationNumber: sql.NullInt32{Int32: o.num, Valid: true},
			OperationType:   o.otype,
			Zone:            o.zone,
			Note:            text(o.note),
			// SMV, seam class, allowance и плотность НЕ ЗАДАНЫ ни у одного шага карточки 8: все 48
			// наследуют дефолты карточки, и это одна из несущих находок золотого ревью.
			SMV:           decimal.NullDecimal{},
			SeamClass:     nullText(),
			StitchesPerCm: decimal.NullDecimal{},
			CalloutNumber: sql.NullInt32{},
		}
		if o.work != "" {
			op.Work = text(o.work)
		}
		if o.machine != "" {
			op.MachineType = text(o.machine)
		}
		if o.out != "" {
			op.OutputUnitKey = text(o.out)
			// Имя узла в дампе отдельной колонкой не печатается; на карточке оно совпадает с
			// ключом. Имя в дайджест секции не входит и фактом цеха не является (entity), так что
			// равенство здесь ничего не утверждает сверх дампа.
			op.OutputUnitName = text(o.out)
		}
		for _, in := range o.inputs {
			kind, key := in[:2], in[2:]
			switch kind {
			case "p:":
				id, ok := pieceIDByName[key]
				if !ok {
					panic("card8 fixture: op input names an unknown piece: " + key)
				}
				lineKey := card8PieceKey(id)
				op.AssemblyInputs = append(op.AssemblyInputs,
					entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: lineKey})
				op.InputKeys = append(op.InputKeys, lineKey)
				op.PieceIds = append(op.PieceIds, id)
				op.PieceLineKeys = append(op.PieceLineKeys, lineKey)
			case "u:":
				op.AssemblyInputs = append(op.AssemblyInputs,
					entity.OperationInput{Kind: entity.AssemblyInputUnit, Key: key})
				op.InputKeys = append(op.InputKeys, key)
			default:
				panic("card8 fixture: op input has no kind prefix: " + in)
			}
		}
		for _, lineKey := range o.bom {
			id, ok := bomIDByKey[lineKey]
			if !ok {
				panic("card8 fixture: op names an unknown BOM line: " + lineKey)
			}
			op.BomLineKeys = append(op.BomLineKeys, lineKey)
			op.BomIds = append(op.BomIds, id)
		}
		ops = append(ops, op)
	}

	// Рецепт колорвея — единственное место, где деталь названа тканью (usage.piece_id): в
	// tech_card_piece_material на проде почти пусто, и ведро в дампе посчитано именно отсюда. Ни
	// нормы (consumption), ни счёта (quantity) карточка 8 не несёт.
	usages := make([]entity.TechCardColorwayUsage, 0, len(card8Pieces))
	for _, p := range card8Pieces {
		bomID, ok := bomIDByKey[card8ScopeToBom[p.scope]]
		if !ok {
			panic("card8 fixture: piece scope names no BOM line: " + p.scope)
		}
		usages = append(usages, entity.TechCardColorwayUsage{
			PieceId:   sql.NullInt64{Int64: int64(p.id), Valid: true},
			BomItemId: sql.NullInt64{Int64: int64(bomID), Valid: true},
		})
	}

	return &entity.TechCard{
		Id: 8,
		TechCardInsert: entity.TechCardInsert{
			StyleNumber:             text("SS26-008"),
			Name:                    "Blazer",
			Stage:                   entity.TechCardStageProto,
			ApprovalState:           entity.TechCardApprovalDraft,
			Purpose:                 entity.TechCardPurposeSellable,
			TargetGender:            text("male"),
			MeasurementUnit:         entity.TechCardUnitMm,
			RequiredSeamAllowanceMm: dec("10.0"),
			Construction: &entity.TechCardConstruction{
				HemFinish:            nullText(),
				Notes:                nullText(),
				DefaultSeamClass:     text("ss_plain"),
				DefaultStitchesPerCm: dec("3.00"),
				// nil, а не пустая структура: так его оставляет ЧТЕНИЕ стора на карточке без
				// профилей (production.go). Пустая структура — смысл записи, «полная замена».
				EquipmentDefaults: nil,
			},
			BomItems:   bom,
			Pieces:     pieces,
			Operations: ops,
			Colorways: []entity.TechCardColorway{{
				Id: 8, Name: "black", ColorCode: "BLACK", Usages: usages,
			}},
			// Размерный ряд карточки 8 с прода: s, m, l, xl. C1 («пол под всем») на этой карточке
			// молчит в том числе поэтому.
			SizeIds: card8SizeIds,
			// BaseSampleSizeId НЕ ЗАДАН намеренно: на проде он NULL. Это пятая пустота печатного
			// пакета, которую C2 обязана назвать.
			BaseSampleSizeId: sql.NullInt32{},
			// Пусто на карточке 8 — и на этом стоят приёмки C2/C3/C4/C5 и B6/B7.
			Labels:    nil,
			Packaging: nil,
			Costing:   nil,
			Issues:    nil,
			Media:     nil,
		},
	}
}

// ── ХЕЛПЕРЫ МУТАЦИЙ ─────────────────────────────────────────────────────────────────────────────
//
// Проверка обязана ЛОВИТЬ, а не только молчать: тест, который лишь исполняет код, зелен и на
// сторожевой проверке у мёртвой ветки. Мутаторы ниже — общий инструмент fire-стороны для T3/T4.

// card8OpByNumber returns a pointer to the operation with that number, or fails loudly: молчаливый
// nil превратил бы мутацию в no-op, а тест — в ложную зелень.
func card8OpByNumber(c *entity.TechCard, number int32) *entity.TechCardOperation {
	for i := range c.Operations {
		if c.Operations[i].OperationNumber.Int32 == number {
			return &c.Operations[i]
		}
	}
	panic(fmt.Sprintf("card8 fixture: no operation #%d", number))
}

// card8PieceByName returns a pointer to the piece with that name, or fails loudly.
func card8PieceByName(c *entity.TechCard, name string) *entity.TechCardPiece {
	for i := range c.Pieces {
		if c.Pieces[i].Name == name {
			return &c.Pieces[i]
		}
	}
	panic("card8 fixture: no piece named " + name)
}

// card8BomByKey returns a pointer to the BOM line with that line_key, or fails loudly.
func card8BomByKey(c *entity.TechCard, lineKey string) *entity.TechCardBomItem {
	for i := range c.BomItems {
		if c.BomItems[i].LineKey == lineKey {
			return &c.BomItems[i]
		}
	}
	panic("card8 fixture: no BOM line " + lineKey)
}

// card8DropOperation removes an operation by number and RENUMBERS the rest the way the write path
// does — (i+1)*10 — so the mutated card is one the server could actually have saved.
func card8DropOperation(c *entity.TechCard, number int32) {
	out := make([]entity.TechCardOperation, 0, len(c.Operations))
	for i := range c.Operations {
		if c.Operations[i].OperationNumber.Int32 == number {
			continue
		}
		out = append(out, c.Operations[i])
	}
	if len(out) == len(c.Operations) {
		panic(fmt.Sprintf("card8 fixture: no operation #%d to drop", number))
	}
	for i := range out {
		out[i].OperationNumber = sql.NullInt32{Int32: int32((i + 1) * 10), Valid: true}
	}
	c.Operations = out
}
