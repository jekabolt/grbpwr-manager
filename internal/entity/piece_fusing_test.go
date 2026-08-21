package entity

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
)

func fusingDec(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
}

func fusingMode(v TechCardPieceFusingMode) sql.NullString {
	return sql.NullString{String: string(v), Valid: true}
}

// НЕРАЗМЕЧЕННАЯ ДЕТАЛЬ СЧИТАЕТСЯ КАК РАНЬШЕ. Весь код до 0304 читал голую галку единственным
// возможным способом — «клеевая по тому же лекалу», — и разворот NULL в full сохраняет ровно это.
// Если однажды здесь окажется что-то другое, каждая существующая карточка молча переоценит клеевую.
func TestUnmarkedPieceReadsAsFullFusing(t *testing.T) {
	if got := PieceFusingModeOrFull(sql.NullString{}); got != PieceFusingModeFull {
		t.Fatalf("неразмеченная деталь развернулась в %q, ожидалось %q", got, PieceFusingModeFull)
	}
	// Мусор в колонке тоже разворачивается в full, а не роняет расчёт: значение туда попасть не
	// может (CHECK + словарь), но расчёт денег — не то место, где стоит паниковать из-за строки.
	if got := PieceFusingModeOrFull(sql.NullString{String: "нечто", Valid: true}); got != PieceFusingModeFull {
		t.Fatalf("неизвестный режим развернулся в %q, ожидалось %q", got, PieceFusingModeFull)
	}
}

// СНЯТАЯ ГАЛКА ГАСИТ РАЗМЕТКУ. Уцелевший режим при fused=false — это строка, которая на экране
// говорит «не дублируется», а расчёту отдаёт полосу; в базе такую пару не пускает chk_tcp_fusing_mode,
// и оператор увидел бы 3819 с именем колонки, которую не трогал.
func TestNormalizeFusingClearsMarkingOnAnUnfusedPiece(t *testing.T) {
	p := TechCardPiece{
		Fused:         false,
		FusingMode:    fusingMode(PieceFusingModeStrip),
		FusingWidthMm: fusingDec("25"),
	}
	p.NormalizeFusing()
	if p.FusingMode.Valid || p.FusingWidthMm.Valid {
		t.Fatalf("разметка пережила снятую галку: mode=%v width=%v", p.FusingMode, p.FusingWidthMm)
	}
}

// ШИРИНА ЖИВЁТ ТОЛЬКО У ПОЛОСЫ. Число рядом с «дублируется целиком» описывает ничто: на экране
// виден полный контур, а в расчёт ушёл бы остаток от прошлой правки.
func TestNormalizeFusingDropsWidthOutsideStrip(t *testing.T) {
	for _, mode := range []TechCardPieceFusingMode{PieceFusingModeFull} {
		p := TechCardPiece{Fused: true, FusingMode: fusingMode(mode), FusingWidthMm: fusingDec("25")}
		p.NormalizeFusing()
		if p.FusingWidthMm.Valid {
			t.Errorf("режим %q сохранил свою ширину", mode)
		}
		if !p.FusingMode.Valid {
			t.Errorf("режим %q потерялся вместе с шириной", mode)
		}
	}
	p := TechCardPiece{Fused: true, FusingMode: fusingMode(PieceFusingModeStrip), FusingWidthMm: fusingDec("25")}
	p.NormalizeFusing()
	if !p.FusingWidthMm.Valid {
		t.Fatal("полоса потеряла свою ширину — считать её стало нечем")
	}
}

// ПОЛОСА БЕЗ ЧИСЛА — ЗАКОННЫЙ ОТВЕТ «ПО ЭТАЛОНУ ПРИПУСКА» (0328), И ЭТО РАЗВОРОТ ПРЕЖНЕГО ПРАВИЛА.
//
// Раньше здесь стоял отказ, а «ширину берём из эталона» говорилось ОТДЕЛЬНЫМ режимом
// `seam_allowance` — то есть один приём был записан двумя значениями, различавшимися лишь тем,
// названо ли число. Довод прежнего отказа («подставить припуск значило бы молча превратить
// „полосой“ в „по припуску“») держался ровно на существовании второго режима: без него подставлять
// нечего во что, потому что «полосой» и «по припуску» — теперь одно и то же утверждение с
// заполненным и незаполненным числом.
//
// Ничего не выдумывается за технолога и сейчас: пустая ширина ОСТАЁТСЯ пустой в колонке, а
// разворачивает её в число только читатель, которому число нужно (dto.fusingGeometryFor), и по
// каскаду эталона 0277.
func TestValidatePieceFusingAcceptsAStripWithNoWidth(t *testing.T) {
	if err := ValidatePieceFusing("pieces[0].fusing_mode", true, fusingMode(PieceFusingModeStrip), decimal.NullDecimal{}); err != nil {
		t.Fatalf("полоса без ширины отклонена: %v — сказать «по эталону припуска» стало нечем", err)
	}
	// НЕГАТИВНЫЙ КОНТРОЛЬ: снятый режим не должен вернуться другим путём. Он не член словаря, и
	// проверка обязана называть его именно неизвестным, а не молча принимать.
	if err := ValidatePieceFusing("pieces[0].fusing_mode", true, fusingMode("seam_allowance"), decimal.NullDecimal{}); err == nil {
		t.Fatal("режим seam_allowance принят — 0328 снял его, и полоса по эталону выражается пустой шириной у strip")
	}
	// И ширина при «целиком» по-прежнему отвергается: пара атомарна, число там описывает ничто.
	if err := ValidatePieceFusing("pieces[0].fusing_mode", true, fusingMode(PieceFusingModeFull), fusingDec("25")); err == nil {
		t.Fatal("ширина при «целиком» принята — на экране полный контур, а в расчёт ушло бы 25 мм")
	}
}

func TestValidatePieceFusingRefusesUnitSlipAndZero(t *testing.T) {
	// 250 мм = 25 см: это дублирование целиком, набранное в чужой единице.
	if err := ValidatePieceFusing("f", true, fusingMode(PieceFusingModeStrip), fusingDec("250")); err == nil {
		t.Error("ширина 250 мм принята — потолок 0304 не повторён в словах")
	}
	if err := ValidatePieceFusing("f", true, fusingMode(PieceFusingModeStrip), fusingDec("0")); err == nil {
		t.Error("нулевая полоса принята — это «не дублируется», а не полоса")
	}
	if err := ValidatePieceFusing("f", true, fusingMode(PieceFusingModeStrip), fusingDec("25")); err != nil {
		t.Errorf("законная полоса 25 мм отклонена: %v", err)
	}
}

// «НЕ РАЗМЕЧЕНО» — ЗАКОННОЕ СОСТОЯНИЕ, и оно обязано проходить: иначе ни одна карточка, заведённая
// до 0304, не сохранится.
func TestValidatePieceFusingAcceptsUnmarked(t *testing.T) {
	if err := ValidatePieceFusing("f", true, sql.NullString{}, decimal.NullDecimal{}); err != nil {
		t.Fatalf("неразмеченная fused-деталь отклонена: %v", err)
	}
	if err := ValidatePieceFusing("f", false, sql.NullString{}, decimal.NullDecimal{}); err != nil {
		t.Fatalf("обычная недублируемая деталь отклонена: %v", err)
	}
}

// МАСШТАБ КОЛОНКИ. DECIMAL(6,1) округлит 25.25 до 25.3 — но дайджест CONSTRUCTION к тому моменту
// уже посчитан по 25.25, и подпись, поставленная тем же сохранением, разойдётся с первым же
// чтением карточки. Второй сценарий злее: 0.04 округлится до 0.0 и уронит всю карточку в 3819.
func TestValidatePieceFusingRefusesSubMillimetrePrecision(t *testing.T) {
	if err := ValidatePieceFusing("f", true, fusingMode(PieceFusingModeStrip), fusingDec("25.25")); err == nil {
		t.Error("25.25 мм принято — БД сохранит 25.3, и подпись разойдётся с хранимым")
	}
	if err := ValidatePieceFusing("f", true, fusingMode(PieceFusingModeStrip), fusingDec("0.04")); err == nil {
		t.Error("0.04 мм принято — БД округлит до нуля и отобьёт карточку CHECK'ом")
	}
	if err := ValidatePieceFusing("f", true, fusingMode(PieceFusingModeStrip), fusingDec("25.3")); err != nil {
		t.Errorf("законная десятая доля отклонена: %v", err)
	}
}
