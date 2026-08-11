package dto

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

const (
	aliasSlotKey  = "01SLOTKEY0000000000000000A"
	aliasPieceKey = "01PIECEKEY000000000000000B"
)

func validAliasSet() *pb_common.TechCardPieceDxfAliasSet {
	return &pb_common.TechCardPieceDxfAliasSet{Items: []*pb_common.TechCardPieceDxfAlias{
		{BomLineKey: aliasSlotKey, BlockName: "ПЕРЕД", PieceLineKey: aliasPieceKey},
	}}
}

func TestParseTechCardPieceDxfAliases(t *testing.T) {
	t.Run("nil wrapper is absent", func(t *testing.T) {
		out, set, err := parseTechCardPieceDxfAliases(nil)
		require.NoError(t, err)
		require.False(t, set, "absent wrapper must read as «did not speak», not as clear-all")
		require.Nil(t, out)
	})
	t.Run("present-empty wrapper clears", func(t *testing.T) {
		out, set, err := parseTechCardPieceDxfAliases(&pb_common.TechCardPieceDxfAliasSet{})
		require.NoError(t, err)
		require.True(t, set)
		require.Empty(t, out)
	})
	t.Run("valid roundtrip with normalization", func(t *testing.T) {
		pb := validAliasSet()
		pb.Items[0].BlockName = "  ПЕРЕД   ПОЛОЧКА  "
		out, set, err := parseTechCardPieceDxfAliases(pb)
		require.NoError(t, err)
		require.True(t, set)
		require.Len(t, out, 1)
		require.Equal(t, "ПЕРЕД ПОЛОЧКА", out[0].BlockName, "trim + inner whitespace collapse")
		require.Equal(t, aliasSlotKey, out[0].BomLineKey)
		require.Equal(t, aliasPieceKey, out[0].PieceLineKey)
	})
	t.Run("nil item skipped", func(t *testing.T) {
		pb := validAliasSet()
		pb.Items = append([]*pb_common.TechCardPieceDxfAlias{nil}, pb.Items...)
		out, _, err := parseTechCardPieceDxfAliases(pb)
		require.NoError(t, err)
		require.Len(t, out, 1)
	})
	t.Run("case-insensitive duplicate rejected", func(t *testing.T) {
		pb := validAliasSet()
		pb.Items = append(pb.Items, &pb_common.TechCardPieceDxfAlias{
			BomLineKey: aliasSlotKey, BlockName: "перед", PieceLineKey: aliasPieceKey,
		})
		_, _, err := parseTechCardPieceDxfAliases(pb)
		require.Error(t, err)
		require.Contains(t, err.Error(), "under one purpose")
	})
	t.Run("same block on two slots is fine", func(t *testing.T) {
		pb := validAliasSet()
		pb.Items = append(pb.Items, &pb_common.TechCardPieceDxfAlias{
			BomLineKey: "01SLOTKEY0000000000000000B", BlockName: "ПЕРЕД", PieceLineKey: aliasPieceKey,
		})
		out, _, err := parseTechCardPieceDxfAliases(pb)
		require.NoError(t, err)
		require.Len(t, out, 2, "fabric scope: generic names may repeat across slots")
	})
	rejects := []struct {
		name   string
		mutate func(*pb_common.TechCardPieceDxfAlias)
		want   string
	}{
		{"neither purpose nor slot", func(a *pb_common.TechCardPieceDxfAlias) { a.BomLineKey = "  " },
			"one of the two must say which cloth"},
		{"bad slot shape", func(a *pb_common.TechCardPieceDxfAlias) { a.BomLineKey = "short" }, "26-character"},
		{"empty block", func(a *pb_common.TechCardPieceDxfAlias) { a.BlockName = "   " }, "block_name is required"},
		{"block over 255 runes", func(a *pb_common.TechCardPieceDxfAlias) { a.BlockName = strings.Repeat("ю", 256) }, "255 characters"},
		{"empty piece key", func(a *pb_common.TechCardPieceDxfAlias) { a.PieceLineKey = "" }, "piece_line_key is required"},
		{"bad piece key", func(a *pb_common.TechCardPieceDxfAlias) { a.PieceLineKey = "01PIECEKEY00000000000000!!" }, "alphanumeric"},
	}
	for _, c := range rejects {
		t.Run(c.name, func(t *testing.T) {
			pb := validAliasSet()
			c.mutate(pb.Items[0])
			_, _, err := parseTechCardPieceDxfAliases(pb)
			require.Error(t, err)
			require.Contains(t, err.Error(), c.want)
		})
	}
}

// TestParseTechCardPieceDxfAliasesFabricPurpose covers the 0267 half: the scope is the назначение
// where one is given, and the payload is deduped on THAT — which is the client-visible face of the
// alias-collapse case. dto is the last line before the transaction: a pair that reaches the UNIQUE
// index comes back as a driver 1062 and fails the WHOLE card save, so it has to be refused here,
// readably, and earlier still by the client's pre-save check.
func TestParseTechCardPieceDxfAliasesFabricPurpose(t *testing.T) {
	const otherSlot = "01SLOTKEY0000000000000000B"
	main := pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_MAIN
	lining := pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_LINING

	t.Run("purpose alone is a complete binding", func(t *testing.T) {
		out, set, err := parseTechCardPieceDxfAliases(&pb_common.TechCardPieceDxfAliasSet{
			Items: []*pb_common.TechCardPieceDxfAlias{
				{FabricPurpose: main, BlockName: "ПЕРЕД", PieceLineKey: aliasPieceKey},
			}})
		require.NoError(t, err)
		require.True(t, set)
		require.Len(t, out, 1)
		require.Equal(t, "main", out[0].FabricPurpose)
		require.Empty(t, out[0].BomLineKey, "no line to record when the purpose owns several")
		require.Equal(t, "main", out[0].ScopeKey(), "purpose wins the COALESCE")
	})
	t.Run("compatibility line rides along and the purpose still wins", func(t *testing.T) {
		out, _, err := parseTechCardPieceDxfAliases(&pb_common.TechCardPieceDxfAliasSet{
			Items: []*pb_common.TechCardPieceDxfAlias{
				{FabricPurpose: main, BomLineKey: aliasSlotKey, BlockName: "ПЕРЕД", PieceLineKey: aliasPieceKey},
			}})
		require.NoError(t, err)
		require.Equal(t, aliasSlotKey, out[0].BomLineKey)
		require.Equal(t, "main", out[0].ScopeKey())
	})
	t.Run("THE COLLAPSE: two lines of one purpose, one block name", func(t *testing.T) {
		// Line-scoped these are two legal aliases (see «same block on two slots is fine» above).
		// Sorted into ONE назначение they become one key, and the second one has to be refused.
		_, _, err := parseTechCardPieceDxfAliases(&pb_common.TechCardPieceDxfAliasSet{
			Items: []*pb_common.TechCardPieceDxfAlias{
				{FabricPurpose: main, BomLineKey: aliasSlotKey, BlockName: "ПЕРЕД", PieceLineKey: aliasPieceKey},
				{FabricPurpose: main, BomLineKey: otherSlot, BlockName: "перед", PieceLineKey: aliasPieceKey},
			}})
		require.Error(t, err)
		require.Contains(t, err.Error(), "under one purpose")
	})
	t.Run("two purposes keep the same block name apart", func(t *testing.T) {
		out, _, err := parseTechCardPieceDxfAliases(&pb_common.TechCardPieceDxfAliasSet{
			Items: []*pb_common.TechCardPieceDxfAlias{
				{FabricPurpose: main, BlockName: "ПЕРЕД", PieceLineKey: aliasPieceKey},
				{FabricPurpose: lining, BlockName: "ПЕРЕД", PieceLineKey: aliasPieceKey},
			}})
		require.NoError(t, err)
		require.Len(t, out, 2, "«полочка верха» and «полочка подкладки» are two pieces, as before")
	})
	t.Run("MID-SORT: a purpose-scoped and a line-scoped row coexist", func(t *testing.T) {
		out, _, err := parseTechCardPieceDxfAliases(&pb_common.TechCardPieceDxfAliasSet{
			Items: []*pb_common.TechCardPieceDxfAlias{
				{FabricPurpose: main, BlockName: "ПЕРЕД", PieceLineKey: aliasPieceKey},
				{BomLineKey: otherSlot, BlockName: "ПЕРЕД", PieceLineKey: aliasPieceKey},
			}})
		require.NoError(t, err)
		require.Len(t, out, 2, "the unsorted line keeps its own scope — no migration step between them")
	})
}

func TestTechCardPieceDxfAliasesToPb(t *testing.T) {
	// The wrapper is ALWAYS present on read — that is what lets a new client round-trip presence.
	out := techCardPieceDxfAliasesToPb(nil)
	require.NotNil(t, out)
	require.Empty(t, out.Items)

	// The purpose round-trips; an unset one degrades to UNSET rather than to some other purpose.
	out = techCardPieceDxfAliasesToPb([]entity.TechCardPieceDxfAlias{
		{FabricPurpose: "main", BlockName: "ПЕРЕД", PieceLineKey: aliasPieceKey},
		{BomLineKey: aliasSlotKey, BlockName: "СПИНКА", PieceLineKey: aliasPieceKey},
	})
	require.Equal(t, pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_MAIN, out.Items[0].FabricPurpose)
	require.Equal(t, pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET, out.Items[1].FabricPurpose)
}
