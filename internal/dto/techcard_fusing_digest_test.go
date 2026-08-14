package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

func fusingCard(pieces ...entity.TechCardPiece) *entity.TechCardInsert {
	return &entity.TechCardInsert{Pieces: pieces}
}

func fusingPiece() entity.TechCardPiece {
	return entity.TechCardPiece{
		LineKey: "01DGSTPIECEFRONT000000C1", Name: "полочка", PiecesPerGarment: 1,
		Grainline: "lengthwise", Fused: true,
	}
}

// ВЫКАТКА НЕ ОБЪЯВЛЯЕТ УСТАРЕВШИМ НИЧЕГО. Проекция деталей кодируется ПОЗИЦИОННО, поэтому
// безусловный элемент — пусть даже пустой — сдвинул бы отпечаток КАЖДОЙ карточки в базе и пометил
// все утверждённые подписи CONSTRUCTION «изменилось с момента утверждения» в момент деплоя, до того
// как кто-либо что-либо разметил. Это утверждение, а не пожелание: карточка без разметки обязана
// хешироваться ровно так же, как хешировалась бы без 0304 вовсе.
func TestUnmarkedFusingDoesNotMoveTheDigest(t *testing.T) {
	// Эталон снят с детали, у которой поля 0304 не заполнены НИКАК — то есть с любой живой карточки.
	unmarked := TechCardSectionDigests(fusingCard(fusingPiece()))[entity.SignoffConstruction]

	// Та же карточка, прошедшая через нормализацию (галка снята — разметка погашена): отпечаток
	// обязан совпасть, иначе снятие галки само по себе устаревало бы подпись.
	p := fusingPiece()
	p.Fused = false
	p.FusingMode = sql.NullString{String: string(entity.PieceFusingModeStrip), Valid: true}
	p.FusingWidthMm = decimal.NullDecimal{Decimal: decimal.RequireFromString("25"), Valid: true}
	p.NormalizeFusing()
	p.Fused = true // вернуть галку, чтобы сравнивать с эталоном ровно по разметке
	if got := TechCardSectionDigests(fusingCard(p))[entity.SignoffConstruction]; got != unmarked {
		t.Fatal("погашенная разметка сдвинула отпечаток — снятие галки устаревало бы подпись CONSTRUCTION")
	}
}

// РАЗМЕТКА ВХОДИТ В ПОДПИСАННОЕ СОДЕРЖАНИЕ. «Эта деталь дублируется полосой 25 мм» описывает
// физическую деталь, которая выйдет из цеха. Утвердить карточку, потом сменить дублирование целиком
// на полосу и не сдвинуть подпись — значит подписать одно, а отдать в цех другое, причём с разницей
// в разы по клеевой.
func TestFusingMarkingMovesTheDigest(t *testing.T) {
	unmarked := TechCardSectionDigests(fusingCard(fusingPiece()))[entity.SignoffConstruction]

	full := fusingPiece()
	full.FusingMode = sql.NullString{String: string(entity.PieceFusingModeFull), Valid: true}
	fullDigest := TechCardSectionDigests(fusingCard(full))[entity.SignoffConstruction]
	if fullDigest == unmarked {
		t.Error("ответ «целиком» не сдвинул отпечаток — а это указание цеху, а не отсутствие указания")
	}

	strip := fusingPiece()
	strip.FusingMode = sql.NullString{String: string(entity.PieceFusingModeStrip), Valid: true}
	strip.FusingWidthMm = decimal.NullDecimal{Decimal: decimal.RequireFromString("25"), Valid: true}
	stripDigest := TechCardSectionDigests(fusingCard(strip))[entity.SignoffConstruction]
	if stripDigest == fullDigest {
		t.Error("«полосой» и «целиком» дали один отпечаток")
	}

	// ШИРИНА — ЧАСТЬ УТВЕРЖДЕНИЯ. 10 мм и 25 мм — разное количество клеевой и разный физический край.
	wider := strip
	wider.FusingWidthMm = decimal.NullDecimal{Decimal: decimal.RequireFromString("10"), Valid: true}
	if TechCardSectionDigests(fusingCard(wider))[entity.SignoffConstruction] == stripDigest {
		t.Error("смена ширины полосы не сдвинула отпечаток")
	}
}

// ХВОСТ ОСТАЁТСЯ ОДНОЗНАЧНЫМ. Шапка constructionProjection предупреждает: два условных элемента
// ОДНОГО типа сливаются, и ["mirrored"] перестаёт отвечать на вопрос, чей он. Режим — тоже строка,
// поэтому он дописывается парой «имя, значение». Проверяется ровно то, что тревожило: карточка с
// размеченным дублированием и БЕЗ разметки кроя не должна совпасть с карточкой наоборот.
func TestFusingTailDoesNotCollideWithCutSymmetry(t *testing.T) {
	onlyFusing := fusingPiece()
	onlyFusing.FusingMode = sql.NullString{String: string(entity.PieceFusingModeFull), Valid: true}

	onlyCut := fusingPiece()
	onlyCut.CutSymmetry = sql.NullString{String: string(entity.PieceCutSymmetryFold), Valid: true}

	a := TechCardSectionDigests(fusingCard(onlyFusing))[entity.SignoffConstruction]
	b := TechCardSectionDigests(fusingCard(onlyCut))[entity.SignoffConstruction]
	if a == b {
		t.Fatal("разметка дублирования и разметка кроя дали один отпечаток — хвост потерял различитель")
	}

	// И обе разом отличаются от каждой по отдельности: слот кроя занят, пара дописана следом.
	both := onlyFusing
	both.CutSymmetry = onlyCut.CutSymmetry
	c := TechCardSectionDigests(fusingCard(both))[entity.SignoffConstruction]
	if c == a || c == b {
		t.Fatal("карточка с обеими разметками совпала с карточкой с одной")
	}
}
