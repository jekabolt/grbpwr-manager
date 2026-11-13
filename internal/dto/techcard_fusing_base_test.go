package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func fusingBaseZonePiece(base *pb_common.TechCardPieceFusingBase) *pb_common.TechCardPiece {
	return &pb_common.TechCardPiece{
		Name: "Body", LineKey: "BODY", Fused: true,
		FusingMode: pb_common.TechCardPieceFusingMode_TECH_CARD_PIECE_FUSING_MODE_ZONE.Enum(),
		FusingZone: pbSplineZone(
			pbSplinePoint("0", "0", true), pbSplinePoint("20", "0", true),
			pbSplinePoint("20", "10", true), pbSplinePoint("0", "10", true),
		),
		FusingBase: base,
	}
}

// The key survives parse → entity → read unchanged, and a piece without one reads with no key.
func TestTechCardPieceFusingBaseRoundTrip(t *testing.T) {
	in := baseTechCardWithPieces([]*pb_common.TechCardPiece{
		fusingBaseZonePiece(&pb_common.TechCardPieceFusingBase{BaseSizeId: 4, ContourSignature: "c1:9f2a-77b0"}),
		fusingBaseZonePiece(nil),
	})
	in.Pieces[1].LineKey, in.Pieces[1].Name = "BACK", "BACK"
	got, err := ConvertPbTechCardInsertToEntity(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	body := got.Pieces[0]
	if body.FusingBaseOmitted || body.FusingBaseSizeId != (sql.NullInt32{Int32: 4, Valid: true}) ||
		body.FusingBaseSignature != (sql.NullString{String: "c1:9f2a-77b0", Valid: true}) {
		t.Fatalf("entity key = %v / %v omitted=%v", body.FusingBaseSizeId, body.FusingBaseSignature, body.FusingBaseOmitted)
	}
	if back := got.Pieces[1]; !back.FusingBaseOmitted || back.FusingBaseSizeId.Valid || back.FusingBaseSignature.Valid {
		t.Fatalf("absent key must parse as omitted and empty, got %v / %v omitted=%v",
			back.FusingBaseSizeId, back.FusingBaseSignature, back.FusingBaseOmitted)
	}

	out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *got}, CostingFx{})
	if base := out.TechCard.Pieces[0].GetFusingBase(); base.GetBaseSizeId() != 4 || base.GetContourSignature() != "c1:9f2a-77b0" {
		t.Fatalf("wire key = %v", base)
	}
	if base := out.TechCard.Pieces[1].GetFusingBase(); base != nil {
		t.Fatalf("a piece with no key must read with no key, got %v", base)
	}
}

// A present empty key is the explicit clear; it is not "omitted".
func TestTechCardPieceFusingBasePresentEmptyClears(t *testing.T) {
	got, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
		fusingBaseZonePiece(&pb_common.TechCardPieceFusingBase{}),
	}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := got.Pieces[0]
	if p.FusingBaseOmitted || p.FusingBaseSizeId.Valid || p.FusingBaseSignature.Valid {
		t.Fatalf("present empty key = %v / %v omitted=%v, want an explicit clear", p.FusingBaseSizeId, p.FusingBaseSignature, p.FusingBaseOmitted)
	}
}

// The parser already drops the key with the markup it describes: unfused, mode without markup, and
// markup explicitly cleared in the same write.
func TestTechCardPieceFusingBaseClearedWithMarkup(t *testing.T) {
	key := func() *pb_common.TechCardPieceFusingBase {
		return &pb_common.TechCardPieceFusingBase{BaseSizeId: 4, ContourSignature: "sig"}
	}
	unfused := fusingBaseZonePiece(key())
	unfused.Fused = false
	unfused.FusingMode = nil
	unfused.FusingZone = nil
	full := fusingBaseZonePiece(key())
	full.LineKey, full.Name = "FULL", "FULL"
	full.FusingMode = pb_common.TechCardPieceFusingMode_TECH_CARD_PIECE_FUSING_MODE_FULL.Enum()
	full.FusingZone = nil
	zoneCleared := fusingBaseZonePiece(key())
	zoneCleared.LineKey, zoneCleared.Name = "CLEARED", "CLEARED"
	zoneCleared.FusingZone = &pb_common.TechCardPieceFusingZone{}
	zoneOmitted := fusingBaseZonePiece(key())
	zoneOmitted.LineKey, zoneOmitted.Name = "OMITTED", "OMITTED"
	zoneOmitted.FusingZone = nil

	got, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{unfused, full, zoneCleared, zoneOmitted}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for i, name := range []string{"unfused", "full", "zone cleared"} {
		if p := got.Pieces[i]; p.FusingBaseSizeId.Valid || p.FusingBaseSignature.Valid {
			t.Errorf("%s: key = %v / %v, want cleared", name, p.FusingBaseSizeId, p.FusingBaseSignature)
		}
	}
	// An omitted zone is kept by the store, so its key must survive the parse for the store to keep.
	if p := got.Pieces[3]; !p.FusingBaseSizeId.Valid || !p.FusingBaseSignature.Valid {
		t.Errorf("zone omitted: key = %v / %v, want kept", p.FusingBaseSizeId, p.FusingBaseSignature)
	}
}

func TestTechCardPieceFusingBaseRejectsBadKeys(t *testing.T) {
	for name, base := range map[string]*pb_common.TechCardPieceFusingBase{
		"size without signature": {BaseSizeId: 4},
		"signature without size": {ContourSignature: "sig"},
		"negative size":          {BaseSizeId: -4, ContourSignature: "sig"},
		"signature too long":     {BaseSizeId: 4, ContourSignature: strings.Repeat("a", 65)},
		"signature with space":   {BaseSizeId: 4, ContourSignature: "a b"},
		"signature not ascii":    {BaseSizeId: 4, ContourSignature: "кон"},
	} {
		_, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{fusingBaseZonePiece(base)}))
		if err == nil || !strings.Contains(err.Error(), "fusing_base") {
			t.Errorf("%s: err = %v, want a fusing_base violation", name, err)
		}
	}
	// Half a key is refused even on a piece that is not fused: it is a client bug either way.
	unfused := fusingBaseZonePiece(&pb_common.TechCardPieceFusingBase{BaseSizeId: 4})
	unfused.Fused, unfused.FusingMode, unfused.FusingZone = false, nil, nil
	if _, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{unfused})); err == nil {
		t.Error("half a key on an unfused piece must be refused")
	}
	if _, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
		fusingBaseZonePiece(&pb_common.TechCardPieceFusingBase{BaseSizeId: 4, ContourSignature: strings.Repeat("~", 64)}),
	})); err != nil {
		t.Errorf("a 64-character signature must be accepted: %v", err)
	}
}

// The key is metadata about the markup, not something the shop floor cuts: setting, changing or
// clearing it must not move the CONSTRUCTION digest, or every restamp after a pattern replacement
// would turn an approval stale.
func TestTechCardPieceFusingBaseOutsideConstructionDigest(t *testing.T) {
	got, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
		fusingBaseZonePiece(&pb_common.TechCardPieceFusingBase{BaseSizeId: 4, ContourSignature: "sig-a"}),
	}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	withKey := digestOf(constructionProjection(got))
	got.Pieces[0].FusingBaseSignature = sql.NullString{String: "sig-b", Valid: true}
	got.Pieces[0].FusingBaseSizeId = sql.NullInt32{Int32: 5, Valid: true}
	if changed := digestOf(constructionProjection(got)); changed != withKey {
		t.Fatal("changing the fusing base key moved the CONSTRUCTION digest")
	}
	got.Pieces[0].FusingBaseSignature = sql.NullString{}
	got.Pieces[0].FusingBaseSizeId = sql.NullInt32{}
	if cleared := digestOf(constructionProjection(got)); cleared != withKey {
		t.Fatal("clearing the fusing base key moved the CONSTRUCTION digest")
	}
}

// A release snapshot carries the key with the markup into the cut plan's entity; a snapshot piece
// without markup carries none.
func TestTechCardPieceFusingBaseFromReleaseSnapshot(t *testing.T) {
	noMarkup := fusingBaseZonePiece(&pb_common.TechCardPieceFusingBase{BaseSizeId: 4, ContourSignature: "sig"})
	noMarkup.LineKey, noMarkup.Name = "PLAIN", "PLAIN"
	noMarkup.FusingZone = nil
	card := CutSpecCardFromReleaseSnapshot(&pb_common.TechCard{TechCard: baseTechCardWithPieces([]*pb_common.TechCardPiece{
		fusingBaseZonePiece(&pb_common.TechCardPieceFusingBase{BaseSizeId: 4, ContourSignature: "sig"}),
		noMarkup,
	})})
	if card == nil || len(card.Pieces) != 2 {
		t.Fatalf("snapshot decode = %#v", card)
	}
	if p := card.Pieces[0]; p.FusingBaseSizeId.Int32 != 4 || p.FusingBaseSignature.String != "sig" {
		t.Fatalf("snapshot key = %v / %v", p.FusingBaseSizeId, p.FusingBaseSignature)
	}
	if p := card.Pieces[1]; p.FusingBaseSizeId.Valid || p.FusingBaseSignature.Valid {
		t.Fatalf("a snapshot piece without markup must carry no key, got %v / %v", p.FusingBaseSizeId, p.FusingBaseSignature)
	}
}
