package entity

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func fusingBaseTestPiece(mode TechCardPieceFusingMode) TechCardPiece {
	return TechCardPiece{
		LineKey: "P", Fused: true,
		FusingMode:          sql.NullString{String: string(mode), Valid: true},
		FusingBaseSizeId:    sql.NullInt32{Int32: 4, Valid: true},
		FusingBaseSignature: sql.NullString{String: "sig", Valid: true},
	}
}

func hasFusingBase(p TechCardPiece) bool {
	return p.FusingBaseSizeId.Valid && p.FusingBaseSignature.Valid
}

// The key lives exactly as long as the markup it describes, as far as this write can tell. A markup
// the write omitted is kept by the store, so its key is kept here too.
func TestNormalizeFusingFusingBase(t *testing.T) {
	zone := &TechCardPieceFusingZone{DxfPieceKey: "FUSE-1"}
	edges := []TechCardPieceFusingEdge{{Edge: 1, StartMm: decimal.Zero, EndMm: decimal.Zero}}
	cases := []struct {
		name string
		edit func(*TechCardPiece)
		keep bool
	}{
		{"zone kept", func(p *TechCardPiece) { p.FusingZone = zone }, true},
		{"strip edges kept", func(p *TechCardPiece) {
			p.FusingMode = sql.NullString{String: string(PieceFusingModeStrip), Valid: true}
			p.FusingEdges = edges
		}, true},
		{"unfused clears", func(p *TechCardPiece) { p.FusingZone = zone; p.Fused = false }, false},
		{"unfused clears an omitted key too", func(p *TechCardPiece) {
			p.FusingZone = zone
			p.Fused = false
			p.FusingBaseOmitted = true
		}, false},
		{"full mode clears", func(p *TechCardPiece) {
			p.FusingMode = sql.NullString{String: string(PieceFusingModeFull), Valid: true}
			p.FusingZone = zone // residue the mode switch drops
		}, false},
		{"zone explicitly cleared", func(p *TechCardPiece) {}, false},
		{"strip edges explicitly cleared", func(p *TechCardPiece) {
			p.FusingMode = sql.NullString{String: string(PieceFusingModeStrip), Valid: true}
		}, false},
		{"zone omitted keeps", func(p *TechCardPiece) { p.FusingZoneOmitted = true }, true},
		{"strip edges omitted keeps", func(p *TechCardPiece) {
			p.FusingMode = sql.NullString{String: string(PieceFusingModeStrip), Valid: true}
			p.FusingEdgesOmitted = true
		}, true},
		{"mode omitted keeps", func(p *TechCardPiece) {
			p.FusingMode = sql.NullString{}
			p.FusingOmitted = true
		}, true},
	}
	for _, tc := range cases {
		p := fusingBaseTestPiece(PieceFusingModeZone)
		tc.edit(&p)
		p.NormalizeFusing()
		if got := hasFusingBase(p); got != tc.keep {
			t.Errorf("%s: key kept = %v, want %v", tc.name, got, tc.keep)
		}
		if p.FusingBaseSizeId.Valid != p.FusingBaseSignature.Valid {
			t.Errorf("%s: half a key left behind: %v / %v", tc.name, p.FusingBaseSizeId, p.FusingBaseSignature)
		}
	}
}

func TestValidatePieceFusingBase(t *testing.T) {
	id := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }
	sig := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	if err := ValidatePieceFusingBase("f", sql.NullInt32{}, sql.NullString{}); err != nil {
		t.Errorf("no key: %v", err)
	}
	if err := ValidatePieceFusingBase("f", id(4), sig(strings.Repeat("!", TechCardPieceFusingBaseSignatureMaxLen))); err != nil {
		t.Errorf("full-width key: %v", err)
	}
	for name, c := range map[string]struct {
		id  sql.NullInt32
		sig sql.NullString
	}{
		"size only":      {id(4), sql.NullString{}},
		"signature only": {sql.NullInt32{}, sig("sig")},
		"zero size":      {id(0), sig("sig")},
		"negative size":  {id(-1), sig("sig")},
		"empty sig":      {id(4), sig("")},
		"long sig":       {id(4), sig(strings.Repeat("a", TechCardPieceFusingBaseSignatureMaxLen+1))},
		"tab":            {id(4), sig("a\tb")},
		"space":          {id(4), sig("a b")},
		"non-ascii":      {id(4), sig("é")},
	} {
		if err := ValidatePieceFusingBase("f", c.id, c.sig); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The store's validation path refuses half a key that arrived without going through the wire parser.
func TestValidatePieceFusingDetailsChecksFusingBase(t *testing.T) {
	p := fusingBaseTestPiece(PieceFusingModeZone)
	p.FusingZone = &TechCardPieceFusingZone{DxfPieceKey: "FUSE-1"}
	p.FusingBaseSignature = sql.NullString{}
	err := ValidatePieceFusingDetails("pieces[0]", &p)
	if err == nil || !strings.Contains(err.Error(), "fusing_base") {
		t.Fatalf("err = %v, want a fusing_base violation", err)
	}
}
