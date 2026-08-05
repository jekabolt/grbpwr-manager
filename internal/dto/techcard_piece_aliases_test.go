package dto

import (
	"strings"
	"testing"

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
		require.Contains(t, err.Error(), "mapped twice")
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
		{"empty slot", func(a *pb_common.TechCardPieceDxfAlias) { a.BomLineKey = "  " }, "bom_line_key is required"},
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

func TestTechCardPieceDxfAliasesToPb(t *testing.T) {
	// The wrapper is ALWAYS present on read — that is what lets a new client round-trip presence.
	out := techCardPieceDxfAliasesToPb(nil)
	require.NotNil(t, out)
	require.Empty(t, out.Items)
}
