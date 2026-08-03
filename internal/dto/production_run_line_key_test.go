package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

func runInsertWithLines(lines ...*pb_common.ProductionRunLine) *pb_common.ProductionRunInsert {
	return &pb_common.ProductionRunInsert{
		TechCardId: 7,
		Status:     pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
		Lines:      lines,
	}
}

// A keyless line is either a brand-new line or a client that predates 0230. It must leave the DTO
// layer WITH an identity, so the run reads back keyed and the next save can be diffed instead of
// delete+reinserted.
func TestConvertPbProductionRunLinesMintsMissingLineKeys(t *testing.T) {
	e, err := ConvertPbProductionRunInsertToEntity(runInsertWithLines(
		&pb_common.ProductionRunLine{ProductId: 11, SizeId: 1, PlannedQty: 60},
		&pb_common.ProductionRunLine{ProductId: 11, SizeId: 2, PlannedQty: 40, LineKey: "  ABCDEFGHJKMNPQRSTVWXYZ0123  "},
	))
	require.NoError(t, err)
	require.Len(t, e.Lines, 2)

	require.True(t, entity.IsValidProductionRunLineKey(e.Lines[0].LineKey),
		"a keyless line must be minted a valid key, got %q", e.Lines[0].LineKey)
	require.Equal(t, "ABCDEFGHJKMNPQRSTVWXYZ0123", e.Lines[1].LineKey,
		"a client key must survive conversion (trimmed)")
	require.NotEqual(t, e.Lines[0].LineKey, e.Lines[1].LineKey)

	// Two keyless lines never collide on the minted identity.
	e2, err := ConvertPbProductionRunInsertToEntity(runInsertWithLines(
		&pb_common.ProductionRunLine{ProductId: 11, SizeId: 1, PlannedQty: 1},
		&pb_common.ProductionRunLine{ProductId: 11, SizeId: 2, PlannedQty: 1},
	))
	require.NoError(t, err)
	require.NotEqual(t, e2.Lines[0].LineKey, e2.Lines[1].LineKey)

	// And it round-trips back onto the wire, which is how the client keeps the key across saves.
	pb := ConvertEntityProductionRunToPb(&entity.ProductionRun{Id: 9, ProductionRunInsert: *e})
	require.Equal(t, e.Lines[0].LineKey, pb.Run.Lines[0].LineKey)
	require.Equal(t, e.Lines[1].LineKey, pb.Run.Lines[1].LineKey)
}

func TestConvertPbProductionRunLinesRejectsBadLineKeys(t *testing.T) {
	cases := map[string]*pb_common.ProductionRunInsert{
		"too short": runInsertWithLines(
			&pb_common.ProductionRunLine{ProductId: 11, SizeId: 1, PlannedQty: 1, LineKey: "ABC"}),
		"too long": runInsertWithLines(
			&pb_common.ProductionRunLine{ProductId: 11, SizeId: 1, PlannedQty: 1, LineKey: "ABCDEFGHJKMNPQRSTVWXYZ01234"}),
		"lowercase": runInsertWithLines(
			&pb_common.ProductionRunLine{ProductId: 11, SizeId: 1, PlannedQty: 1, LineKey: "abcdefghjkmnpqrstvwxyz0123"}),
		"punctuation": runInsertWithLines(
			&pb_common.ProductionRunLine{ProductId: 11, SizeId: 1, PlannedQty: 1, LineKey: "ABCDEFGHJKMNPQRSTVWXYZ-123"}),
		// Two lines claiming one identity would silently collapse onto a single row in the keyed diff.
		"duplicate key": runInsertWithLines(
			&pb_common.ProductionRunLine{ProductId: 11, SizeId: 1, PlannedQty: 1, LineKey: "ABCDEFGHJKMNPQRSTVWXYZ0123"},
			&pb_common.ProductionRunLine{ProductId: 11, SizeId: 2, PlannedQty: 1, LineKey: "ABCDEFGHJKMNPQRSTVWXYZ0123"}),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ConvertPbProductionRunInsertToEntity(in)
			require.Error(t, err)
		})
	}
}

// The accepted charset is deliberately wider than Crockford base32: migration 0230 backfilled legacy
// rows with 'LEGACY'-prefixed keys (Crockford has no 'L') and the server-side fallback mints standard
// base32, so a strict Crockford check would reject the store's own keys on the next save.
func TestProductionRunLineKeyAcceptsTheKeysTheStoreItselfMints(t *testing.T) {
	require.True(t, entity.IsValidProductionRunLineKey("LEGACY00000000000000000123"),
		"migration 0230's backfilled key must round-trip through the API")

	minted, err := mintProductionRunLineKey()
	require.NoError(t, err)
	require.Len(t, minted, entity.ProductionRunLineKeyLen)
	require.True(t, entity.IsValidProductionRunLineKey(minted))

	require.False(t, entity.IsValidProductionRunLineKey(""))
	require.False(t, entity.IsValidProductionRunLineKey("ABCDEFGHJKMNPQRSTVWXYZ012"))
}
