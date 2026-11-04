package dto

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

const (
	cutDigestLine  = "01JABCDEFGHJKMNPQRSTVWXYL1"
	cutDigestStrip = "01JABCDEFGHJKMNPQRSTVWXYS1"
)

func cutDigestCard(strips ...entity.TechCardCutStrip) *entity.TechCardInsert {
	card := asmDigestCard(asmPlainOp("FR", "BK"))
	card.BomItems = []entity.TechCardBomItem{{
		LineKey: cutDigestLine, Name: "rubber", Section: entity.BomSectionHardware,
		Unit: sql.NullString{String: "m", Valid: true},
	}}
	card.CutStrips = strips
	return card
}

func cutDigestStripRow() entity.TechCardCutStrip {
	return entity.TechCardCutStrip{
		LineKey: cutDigestStrip, BomLineKey: cutDigestLine, LengthMode: entity.CutStripModeFromSeam,
		FitPct: decimal.RequireFromString("90"), OverlapMm: decimal.RequireFromString("20"),
		WidthMm: decimal.RequireFromString("30"), Qty: 1,
		Sizes: []entity.TechCardCutStripSize{
			{SizeId: 1, SeamMm: nd("700")},
			{SizeId: 2, SeamMm: nd("800"), ManualCutMm: sql.NullInt32{Int32: 900, Valid: true}},
		},
	}
}

// No existing card's signature may move: a card with no strips hashes exactly as it did, whether or not
// the client says it knows about strips.
func TestCutStripDigestLeavesACardWithNoStripsAlone(t *testing.T) {
	plain := TechCardSectionDigests(cutDigestCard())
	aware := cutDigestCard()
	aware.CutStripsAware = true
	require.Equal(t, plain, TechCardSectionDigests(aware), "the flag speaks about the sender, not the card")
}

func TestCutStripDigestMovesMaterialsOnly(t *testing.T) {
	base := TechCardSectionDigests(cutDigestCard())
	with := TechCardSectionDigests(cutDigestCard(cutDigestStripRow()))
	require.NotEqual(t, base[entity.SignoffMaterials], with[entity.SignoffMaterials])
	require.Equal(t, base[entity.SignoffConstruction], with[entity.SignoffConstruction], "a strip no step uses says nothing about construction")

	for name, change := range map[string]func(*entity.TechCardCutStrip){
		"fit":        func(s *entity.TechCardCutStrip) { s.FitPct = decimal.RequireFromString("95") },
		"overlap":    func(s *entity.TechCardCutStrip) { s.OverlapMm = decimal.RequireFromString("25") },
		"width":      func(s *entity.TechCardCutStrip) { s.WidthMm = decimal.RequireFromString("40") },
		"qty":        func(s *entity.TechCardCutStrip) { s.Qty = 2 },
		"mode":       func(s *entity.TechCardCutStrip) { s.LengthMode = entity.CutStripModePerSize },
		"seam key":   func(s *entity.TechCardCutStrip) { s.SeamKey = sql.NullString{String: "SEAM", Valid: true} },
		"one length": func(s *entity.TechCardCutStrip) { s.OneLengthMm = sql.NullInt32{Int32: 500, Valid: true} },
		"seam":       func(s *entity.TechCardCutStrip) { s.Sizes[0].SeamMm = nd("701") },
		"manual":     func(s *entity.TechCardCutStrip) { s.Sizes[1].ManualCutMm = sql.NullInt32{} },
		"row gone":   func(s *entity.TechCardCutStrip) { s.Sizes = s.Sizes[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			row := cutDigestStripRow()
			change(&row)
			got := TechCardSectionDigests(cutDigestCard(row))
			require.NotEqual(t, with[entity.SignoffMaterials], got[entity.SignoffMaterials])
			require.Equal(t, with[entity.SignoffConstruction], got[entity.SignoffConstruction])
		})
	}
}

// A signature made from the payload must equal the fingerprint of the next read, or a signed card reads
// as changed forever. The store hands back DECIMAL(n,2) strings, rows by primary key, strips by display order.
func TestCutStripDigestWriteEqualsRead(t *testing.T) {
	write := cutDigestStripRow()
	second := cutDigestStripRow()
	second.LineKey, second.DisplayOrder = "01JABCDEFGHJKMNPQRSTVWXYS2", 1

	read := cutDigestStripRow()
	read.Id, read.TechCardId, read.BomItemId = 9, 7, 502
	read.FitPct = decimal.RequireFromString("90.00")
	read.OverlapMm = decimal.RequireFromString("20.00")
	read.WidthMm = decimal.RequireFromString("30.00")
	read.Sizes = []entity.TechCardCutStripSize{
		{StripId: 9, SizeId: 2, SeamMm: nd("800.00"), ManualCutMm: sql.NullInt32{Int32: 900, Valid: true}},
		{StripId: 9, SizeId: 1, SeamMm: nd("700.00")},
	}
	readSecond := cutDigestStripRow()
	readSecond.Id, readSecond.LineKey, readSecond.DisplayOrder = 10, second.LineKey, 0
	read.DisplayOrder = 1

	require.Equal(t,
		TechCardSectionDigests(cutDigestCard(write, second)),
		TechCardSectionDigests(cutDigestCard(readSecond, read)))
	require.Equal(t,
		TechCardSectionDigestsAsRead(cutDigestCard(write, second), nil),
		TechCardSectionDigests(cutDigestCard(readSecond, read)))
}

// Digest of a step that takes a strip: the write form comes out of the canonicalizer, the read form out of
// the store, and a strip input is a fact of construction a piece input is not.
func TestCutStripInputDigest(t *testing.T) {
	pieces := []entity.TechCardPiece{
		{LineKey: "FR", Name: "front", PiecesPerGarment: 1},
		{LineKey: "BK", Name: "back", PiecesPerGarment: 1},
	}
	stripKeys := map[string]bool{cutDigestStrip: true}
	writeOps := []entity.TechCardOperation{{
		OperationNumber: ni32(10), OperationType: "machine", Zone: "closure",
		InputKeys: []string{"FR", cutDigestStrip},
	}}
	require.Nil(t, canonicalizeAssembly(writeOps, pieces, stripKeys, true))
	require.Equal(t, []entity.OperationInput{
		{Kind: entity.AssemblyInputPiece, Key: "FR"},
		{Kind: entity.AssemblyInputStrip, Key: cutDigestStrip},
	}, writeOps[0].AssemblyInputs)
	require.Equal(t, []string{"FR"}, writeOps[0].PieceLineKeys, "a strip is not a piece of the step")

	readOps := []entity.TechCardOperation{{
		OperationNumber: ni32(10), OperationType: "machine", Zone: "closure",
		InputKeys:     []string{"FR", cutDigestStrip},
		PieceLineKeys: []string{"FR"},
		AssemblyInputs: []entity.OperationInput{
			{Kind: entity.AssemblyInputPiece, Key: "FR"},
			{Kind: entity.AssemblyInputStrip, Key: cutDigestStrip},
		},
	}}
	card := func(ops []entity.TechCardOperation) *entity.TechCardInsert {
		c := cutDigestCard(cutDigestStripRow())
		c.Pieces, c.Operations = pieces, ops
		return c
	}
	write := TechCardSectionDigests(card(writeOps))
	require.Equal(t, write[entity.SignoffConstruction], TechCardSectionDigests(card(readOps))[entity.SignoffConstruction])

	noStrip := TechCardSectionDigests(card([]entity.TechCardOperation{{
		OperationNumber: ni32(10), OperationType: "machine", Zone: "closure",
		InputKeys: []string{"FR"}, PieceLineKeys: []string{"FR"},
		AssemblyInputs: []entity.OperationInput{{Kind: entity.AssemblyInputPiece, Key: "FR"}},
	}}))
	require.NotEqual(t, noStrip[entity.SignoffConstruction], write[entity.SignoffConstruction], "taking a strip moves construction")
	require.Equal(t, noStrip[entity.SignoffMaterials], write[entity.SignoffMaterials], "and nothing in materials")
}

func cutPbDecimal(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

func cutPbStrip() *pb_common.TechCardCutStrip {
	return &pb_common.TechCardCutStrip{
		LineKey: cutDigestStrip, BomLineKey: cutDigestLine, LengthMode: entity.CutStripModeFromSeam,
		FitPct: cutPbDecimal("90"), OverlapMm: cutPbDecimal("20"), WidthMm: cutPbDecimal("30"), Qty: 1,
		Sizes: []*pb_common.TechCardCutStripSize{
			{SizeId: 1, SeamMm: cutPbDecimal("700")},
			{SizeId: 2, SeamMm: cutPbDecimal("800")},
		},
	}
}

func cutParseTarget() *entity.TechCardInsert {
	return &entity.TechCardInsert{
		SizeIds:  []int{1, 2},
		BomItems: []entity.TechCardBomItem{{LineKey: cutDigestLine, Section: entity.BomSectionHardware}},
		Pieces:   []entity.TechCardPiece{{LineKey: "01JABCDEFGHJKMNPQRSTVWXYP1"}},
	}
}

func cutRequireViolation(t *testing.T, err error, field, reason string) {
	t.Helper()
	require.Error(t, err)
	var ve *entity.ValidationError
	require.True(t, errors.As(err, &ve), "got %v", err)
	require.Contains(t, err.Error(), field+": "+reason)
}

func TestParseTechCardCutStrips(t *testing.T) {
	parse := func(mut func(*pb_common.TechCardInsert)) ([]entity.TechCardCutStrip, error) {
		pb := &pb_common.TechCardInsert{CutStripsAware: true, CutStrips: []*pb_common.TechCardCutStrip{cutPbStrip()}}
		if mut != nil {
			mut(pb)
		}
		return parseTechCardCutStrips(pb, cutParseTarget())
	}

	t.Run("a valid row", func(t *testing.T) {
		got, err := parse(nil)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, cutDigestLine, got[0].BomLineKey)
		mm, known := entity.CutStripLengthMm(&got[0], 2)
		require.True(t, known)
		require.Equal(t, 740, mm)
	})
	t.Run("strips from a client that did not say it knows them are refused, not lost", func(t *testing.T) {
		_, err := parse(func(pb *pb_common.TechCardInsert) { pb.CutStripsAware = false })
		cutRequireViolation(t, err, "cut_strips", "aware_required")
	})
	t.Run("flag absent, no strips: nothing to say", func(t *testing.T) {
		got, err := parse(func(pb *pb_common.TechCardInsert) { pb.CutStripsAware, pb.CutStrips = false, nil })
		require.NoError(t, err)
		require.Nil(t, got)
	})
	t.Run("a key is minted when it is missing", func(t *testing.T) {
		got, err := parse(func(pb *pb_common.TechCardInsert) { pb.CutStrips[0].LineKey = "" })
		require.NoError(t, err)
		require.Len(t, got[0].LineKey, 26)
	})
	t.Run("a key twice", func(t *testing.T) {
		_, err := parse(func(pb *pb_common.TechCardInsert) { pb.CutStrips = append(pb.CutStrips, cutPbStrip()) })
		cutRequireViolation(t, err, "cut_strips[1].line_key", "duplicate")
	})
	t.Run("a key that is a piece of this card", func(t *testing.T) {
		_, err := parse(func(pb *pb_common.TechCardInsert) { pb.CutStrips[0].LineKey = "01JABCDEFGHJKMNPQRSTVWXYP1" })
		cutRequireViolation(t, err, "cut_strips[0].line_key", "cut_strip_key_taken")
	})
	t.Run("a line the card does not have", func(t *testing.T) {
		_, err := parse(func(pb *pb_common.TechCardInsert) { pb.CutStrips[0].BomLineKey = "01JABCDEFGHJKMNPQRSTVWXYZZ" })
		cutRequireViolation(t, err, "cut_strips[0].bom_line_key", "cut_strip_bom_line_missing")
	})
	t.Run("a size outside the range is dropped with the rest of the save", func(t *testing.T) {
		got, err := parse(func(pb *pb_common.TechCardInsert) {
			pb.CutStrips[0].Sizes = append(pb.CutStrips[0].Sizes, &pb_common.TechCardCutStripSize{SizeId: 9, SeamMm: cutPbDecimal("900")})
		})
		require.NoError(t, err)
		require.Len(t, got[0].Sizes, 2)
	})
	t.Run("numbers are scaled as the columns store them", func(t *testing.T) {
		got, err := parse(func(pb *pb_common.TechCardInsert) { pb.CutStrips[0].Sizes[0].SeamMm = cutPbDecimal("700.126") })
		require.NoError(t, err)
		require.Equal(t, "700.13", got[0].Sizes[0].SeamMm.Decimal.String())
	})
	t.Run("not a number", func(t *testing.T) {
		_, err := parse(func(pb *pb_common.TechCardInsert) { pb.CutStrips[0].Sizes[0].SeamMm = cutPbDecimal("long") })
		cutRequireViolation(t, err, "cut_strips[0].sizes[0].seam_mm", "not a number")
	})
	t.Run("too many", func(t *testing.T) {
		_, err := parse(func(pb *pb_common.TechCardInsert) {
			pb.CutStrips = make([]*pb_common.TechCardCutStrip, entity.CutStripMaxPerCard+1)
		})
		cutRequireViolation(t, err, "cut_strips", "too_many")
	})
}

// Unknown is absent on the wire, never 0. The client echoes what it reads, so a stored row stays visible even
// when the card's range no longer has its size.
func TestTechCardCutStripsToPb(t *testing.T) {
	strip := cutDigestStripRow()
	strip.Sizes = []entity.TechCardCutStripSize{{SizeId: 1, SeamMm: nd("700")}, {SizeId: 9, SeamMm: nd("900")}}
	got := TechCardCutStripsToPb([]entity.TechCardCutStrip{strip}, []int{1, 2})
	require.Len(t, got, 1)
	rows := got[0].GetSizes()
	require.Len(t, rows, 3)

	require.Equal(t, int32(1), rows[0].GetSizeId())
	require.NotNil(t, rows[0].CutMm)
	require.Equal(t, int32(650), rows[0].GetCutMm())
	require.Equal(t, "630", rows[0].GetSewnMm().GetValue())

	require.Equal(t, int32(2), rows[1].GetSizeId())
	require.Nil(t, rows[1].CutMm, "unknown is absent")
	require.Nil(t, rows[1].GetSewnMm())

	require.Equal(t, int32(9), rows[2].GetSizeId())
	require.Equal(t, "900", rows[2].GetSeamMm().GetValue())

	require.Nil(t, TechCardCutStripsToPb(nil, []int{1}))
}
