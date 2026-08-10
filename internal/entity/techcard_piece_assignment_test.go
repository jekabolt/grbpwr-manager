package entity

import (
	"database/sql"
	"testing"
)

// IsPieceMaterialAssignment — ЕДИНСТВЕННОЕ правило «эта строка рецепта привязана к детали» (T8).
// Привязка живёт в ТРЁХ представлениях в зависимости от того, откуда строка приехала: PieceId
// (чтение из стора), PieceLineKey (провод и снапшот релиза, где id нет вовсе), легаси-позиционный
// PieceIndex — и предикат обязан узнавать все три, иначе строка-назначение, пришедшая другой
// дорогой, снова станет «нормой».
func TestIsPieceMaterialAssignment(t *testing.T) {
	cases := []struct {
		name string
		u    TechCardColorwayUsage
		want bool
	}{
		{"голая строка изделия", TechCardColorwayUsage{}, false},
		{"строка изделия с нормой и пином", TechCardColorwayUsage{
			BomItemId:  sql.NullInt64{Int64: 1, Valid: true},
			MaterialId: sql.NullInt64{Int64: 5, Valid: true},
		}, false},
		{"PieceId (чтение из стора)", TechCardColorwayUsage{
			PieceId: sql.NullInt64{Int64: 3, Valid: true},
		}, true},
		{"PieceLineKey (провод / снапшот релиза)", TechCardColorwayUsage{
			PieceLineKey: "PIECE1",
		}, true},
		{"PieceIndex = 0 — настоящая деталь, а не «не задано»", TechCardColorwayUsage{
			PieceIndex: sql.NullInt32{Int32: 0, Valid: true},
		}, true},
	}
	for _, c := range cases {
		if got := c.u.IsPieceMaterialAssignment(); got != c.want {
			t.Errorf("%s: IsPieceMaterialAssignment() = %v, want %v", c.name, got, c.want)
		}
	}
}
