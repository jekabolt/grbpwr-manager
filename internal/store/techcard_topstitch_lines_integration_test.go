package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	topStoreThread        = "01JCVT000000000000000000A6"
	topStoreLine          = "01JCVT000000000000000000E1"
	topStoreRestoredStale = "restored-stale"
)

func newTopstitchRig(t *testing.T, ctx context.Context) *cutRig {
	t.Helper()
	r := newCutRig(t, ctx)
	card := r.read()
	write := card.TechCardInsert
	write.BomItems = append(write.BomItems, entity.TechCardBomItem{
		LineKey: topStoreThread, Section: entity.BomSectionThread, Name: "top thread",
		Unit:        sql.NullString{String: "m", Valid: true},
		ThreadPerMm: decimal.NewNullDecimal(decimal.RequireFromString("2.5")),
	})
	write.Operations[0].BomLineKeys = append(write.Operations[0].BomLineKeys, topStoreThread)
	write.TopstitchLinesAware = true
	require.NoError(t, r.T.UpdateTechCard(ctx, r.cardID, &write, card.LockVersion))
	return r
}

func topStoreAnchor() *pb_common.TechCardSeamAnchor {
	return &pb_common.TechCardSeamAnchor{
		PieceLineKey: cutsPieceFront,
		Samples: []*pb_common.TechCardSeamSample{
			{U: 0, V: 0}, {U: .25, V: .1}, {U: .5, V: .2}, {U: .75, V: .3}, {U: 1, V: .4},
		},
		PerimShare: .2, LenMm: 250, EdgeHint: "FR#1", ContourSig: "abcdef",
	}
}

func (r *cutRig) createTopstitchLine() error {
	return r.save(func(in *pb_common.TechCardInsert) {
		for _, line := range in.GetBomItems() {
			if line.GetLineKey() == topStoreThread {
				line.ThreadPerMm = cutsDec("2.5")
			}
		}
		cutsOpByNote(r.t, in, "a").TopstitchLines = []*pb_common.TechCardTopstitchLine{{
			LineKey: topStoreLine, BomLineKey: topStoreThread, Parts: []*pb_common.TechCardSeamAnchor{topStoreAnchor()},
			AnchoredSize: "m", Rows: 2,
			Sizes: []*pb_common.TechCardTopstitchLineSize{
				{SizeId: int32(r.sizeA), LineMm: cutsDec("200")},
				{SizeId: int32(r.sizeB), LineMm: cutsDec("220")},
			},
		}}
	})
}

func TestTechCardTopstitchLinesRoundTripAndCascade(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	r := newTopstitchRig(t, ctx)
	require.NoError(t, r.createTopstitchLine())

	card := r.read()
	require.Len(t, card.Operations[0].TopstitchLines, 1)
	line := card.Operations[0].TopstitchLines[0]
	require.Equal(t, topStoreLine, line.LineKey)
	require.Equal(t, topStoreThread, line.BomLineKey)
	require.Equal(t, 2, line.Rows)
	require.Equal(t, []int{r.sizeA, r.sizeB}, []int{line.Sizes[0].SizeId, line.Sizes[1].SizeId})
	require.False(t, line.Stale)
	require.Equal(t, map[int]string{r.sizeA: "1", r.sizeB: "1.1"}, cutsNormOf(r.wire(), topStoreThread))

	var opID, lineID int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT o.id, l.id FROM tech_card_operation o
		JOIN tech_card_operation_topstitch_line l ON l.operation_id = o.id
		WHERE o.tech_card_id = ? AND l.line_key = ?`, r.cardID, topStoreLine).Scan(&opID, &lineID))
	require.Equal(t, 2, cutsCount(t, ctx,
		"SELECT COUNT(*) FROM tech_card_operation_topstitch_line_size WHERE line_id = ?", lineID))
	require.NoError(t, func() error {
		_, err := testDB.ExecContext(ctx, "DELETE FROM tech_card_operation WHERE id = ?", opID)
		return err
	}())
	require.Zero(t, cutsCount(t, ctx, "SELECT COUNT(*) FROM tech_card_operation_topstitch_line WHERE id = ?", lineID))
	require.Zero(t, cutsCount(t, ctx, "SELECT COUNT(*) FROM tech_card_operation_topstitch_line_size WHERE line_id = ?", lineID))
}

// TestTechCardThreadConeUnitReadAndTopstitchValidation proves the store boundary end to end: the
// linked cone remains a cone in the catalog, while the THREAD slot reads in metres and its derived
// topstitch norm is unchanged. The resolver exception is section-specific (a fabric slot still reads
// its linked kg article), and changing the thread SLOT itself to cone remains refused.
func TestTechCardThreadConeUnitReadAndTopstitchValidation(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	r := newTopstitchRig(t, ctx)

	coneID, err := r.T.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "topstitch cone", Section: "thread", MaterialClass: string(entity.MaterialClassThread),
		Unit: sql.NullString{String: "cone", Valid: true},
		ThreadAttr: &entity.MaterialThreadAttr{
			LengthPerConeM: decimal.NewNullDecimal(decimal.RequireFromString("5000")),
		},
	})
	require.NoError(t, err)
	card := r.read()
	write := card.TechCardInsert
	for i := range write.BomItems {
		if write.BomItems[i].LineKey == topStoreThread {
			write.BomItems[i].MaterialId = sql.NullInt64{Int64: int64(coneID), Valid: true}
			write.BomItems[i].Unit = sql.NullString{String: "m", Valid: true}
		}
	}
	require.NoError(t, r.T.UpdateTechCard(ctx, r.cardID, &write, card.LockVersion))
	require.NoError(t, r.createTopstitchLine(), "an m THREAD slot linked to a cone article must pass")

	read := r.read()
	foundThread := false
	for i := range read.BomItems {
		if read.BomItems[i].LineKey == topStoreThread {
			foundThread = true
			require.Equal(t, "m", read.BomItems[i].Unit.String)
		}
	}
	require.True(t, foundThread)
	require.Equal(t, map[int]string{r.sizeA: "1", r.sizeB: "1.1"}, cutsNormOf(r.wire(), topStoreThread))

	kgID, err := r.T.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "fabric by weight", Section: "fabric", MaterialClass: string(entity.MaterialClassFabric),
		Unit: sql.NullString{String: "kg", Valid: true},
	})
	require.NoError(t, err)
	card = r.read()
	write = card.TechCardInsert
	for i := range write.BomItems {
		if write.BomItems[i].LineKey == cutsLineShell {
			write.BomItems[i].MaterialId = sql.NullInt64{Int64: int64(kgID), Valid: true}
			write.BomItems[i].Unit = sql.NullString{String: "m", Valid: true}
		}
	}
	require.NoError(t, r.T.UpdateTechCard(ctx, r.cardID, &write, card.LockVersion))
	read = r.read()
	for i := range read.BomItems {
		if read.BomItems[i].LineKey == cutsLineShell {
			require.Equal(t, "kg", read.BomItems[i].Unit.String, "non-thread lines keep article-first resolution")
		}
	}

	card = r.read()
	write = card.TechCardInsert
	for i := range write.BomItems {
		if write.BomItems[i].LineKey == topStoreThread {
			write.BomItems[i].Unit = sql.NullString{String: "cone", Valid: true}
		}
	}
	err = r.T.UpdateTechCard(ctx, r.cardID, &write, card.LockVersion)
	var violation *entity.ValidationError
	require.ErrorAs(t, err, &violation)
	require.Equal(t, "not_metres", violation.Reason)
}

func TestTechCardTopstitchLinesUnawarePublicWriterIsRefused(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	r := newTopstitchRig(t, ctx)
	require.NoError(t, r.createTopstitchLine())

	err := r.saveOlder(func(in *pb_common.TechCardInsert) { in.Name = "older topstitch writer" })
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	card := r.read()
	require.NotEqual(t, "older topstitch writer", card.Name)
	require.Len(t, card.Operations[0].TopstitchLines, 1)
	require.Equal(t, topStoreLine, card.Operations[0].TopstitchLines[0].LineKey)
	for _, line := range card.BomItems {
		if line.LineKey == topStoreThread {
			require.Equal(t, "2.5", line.ThreadPerMm.Decimal.String())
			return
		}
	}
	t.Fatal("thread BOM line not read")
}

func TestTechCardTopstitchLineRestorePreservesStaleAndFreshVerdicts(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	r := newTopstitchRig(t, ctx)
	require.NoError(t, r.createTopstitchLine())

	writeRestore := func(stale bool) {
		card := r.read()
		write := card.TechCardInsert
		line := &write.Operations[0].TopstitchLines[0]
		line.RestoreSourceFingerprint = true
		line.RestoreStale = stale
		require.NoError(t, r.T.UpdateTechCard(ctx, r.cardID, &write, card.LockVersion))
	}

	writeRestore(true)
	var fingerprint string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT source_fingerprint
		FROM tech_card_operation_topstitch_line WHERE tech_card_id = ? AND line_key = ?`,
		r.cardID, topStoreLine).Scan(&fingerprint))
	require.Equal(t, topStoreRestoredStale, fingerprint)
	require.True(t, r.read().Operations[0].TopstitchLines[0].Stale)

	writeRestore(false)
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT source_fingerprint
		FROM tech_card_operation_topstitch_line WHERE tech_card_id = ? AND line_key = ?`,
		r.cardID, topStoreLine).Scan(&fingerprint))
	require.Len(t, fingerprint, 64)
	require.NotEqual(t, topStoreRestoredStale, fingerprint)
	require.False(t, r.read().Operations[0].TopstitchLines[0].Stale)
}

func TestTechCardTopstitchLineFingerprintCarryAndRestamp(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	r := newTopstitchRig(t, ctx)
	require.NoError(t, r.createTopstitchLine())

	sentinel := strings.Repeat("f", 64)
	_, err := testDB.ExecContext(ctx, `UPDATE tech_card_operation_topstitch_line
		SET source_fingerprint = ? WHERE tech_card_id = ? AND line_key = ?`, sentinel, r.cardID, topStoreLine)
	require.NoError(t, err)
	require.True(t, r.read().Operations[0].TopstitchLines[0].Stale)

	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		line := cutsOpByNote(t, in, "a").TopstitchLines[0]
		line.Rows = 3
		line.Sizes[0].LineMm = cutsDec("201")
	}))
	var carried string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT source_fingerprint
		FROM tech_card_operation_topstitch_line WHERE tech_card_id = ? AND line_key = ?`, r.cardID, topStoreLine).Scan(&carried))
	require.Equal(t, sentinel, carried, "rows and measurements do not restamp the pattern source")

	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		cutsOpByNote(t, in, "a").TopstitchLines[0].Measured = true
	}))
	var restamped string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT source_fingerprint
		FROM tech_card_operation_topstitch_line WHERE tech_card_id = ? AND line_key = ?`, r.cardID, topStoreLine).Scan(&restamped))
	require.NotEqual(t, sentinel, restamped)
	require.Len(t, restamped, 64)
	require.False(t, r.read().Operations[0].TopstitchLines[0].Stale)
}
