package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	cutsLineShell  = "01JCVT000000000000000000A1"
	cutsLineRubber = "01JCVT000000000000000000A2"
	cutsLineTape   = "01JCVT000000000000000000A3"
	cutsLineButton = "01JCVT000000000000000000A4"
	cutsPieceFront = "01JCVT000000000000000000B1"
	cutsStripSeam  = "01JCVT000000000000000000C1"
	cutsStripOne   = "01JCVT000000000000000000C2"
	cutsStripSize  = "01JCVT000000000000000000C3"
	cutsSeamKey    = "01JCVT000000000000000000D1"
)

func requireCutStripIntegrationDB(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
}

// cutRig is a small card through the real admin server and the real store. The rubber line is the golden
// shape: section hardware, unit m, no recipe row of any colourway.
type cutRig struct {
	t      *testing.T
	ctx    context.Context
	rt     *rtRig
	T      dependency.TechCards
	cardID int
	sizeA  int
	sizeB  int
	cwID   int
}

func newCutRig(t *testing.T, ctx context.Context) *cutRig {
	t.Helper()
	rt := foldArchRig(t, ctx)
	T := rt.store.TechCards()
	r := &cutRig{t: t, ctx: ctx, rt: rt, T: T}
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&r.sizeA))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size WHERE id > ?", r.sizeA).Scan(&r.sizeB))

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	step := func(note string) entity.TechCardOperation {
		return entity.TechCardOperation{OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"),
			Zone: entity.ZoneOuter, Note: ns(note), PieceLineKeys: []string{cutsPieceFront}}
	}
	id, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "CUT STRIPS", Stage: entity.TechCardStageProto, StyleNumber: ns(fmt.Sprintf("CUTS-%d", time.Now().UnixNano())),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		Purpose: entity.TechCardPurposeSellable, SizeIds: []int{r.sizeA, r.sizeB},
		SeasonCode: ns("SS"), SeasonYear: sql.NullInt32{Int32: 2026, Valid: true},
		BomItems: []entity.TechCardBomItem{
			{LineKey: cutsLineShell, Section: entity.BomSectionFabric, Name: "shell", Unit: ns("m"), Purpose: ns("main")},
			{LineKey: cutsLineRubber, Section: entity.BomSectionHardware, Name: "rubber", Unit: ns("m")},
			{LineKey: cutsLineTape, Section: entity.BomSectionTrim, Name: "tape", Unit: ns("m")},
			{LineKey: cutsLineButton, Section: entity.BomSectionHardware, Name: "button", Unit: ns("pcs")},
		},
		Pieces:     []entity.TechCardPiece{{LineKey: cutsPieceFront, Name: "front", PiecesPerGarment: 1, Grainline: "lengthwise"}},
		Operations: []entity.TechCardOperation{step("a"), step("b")},
	})
	require.NoError(t, err)
	r.cardID = id
	t.Cleanup(func() {
		// A step's piece input holds its piece by RESTRICT (0307), and tech_card deletes its pieces first.
		_, _ = testDB.ExecContext(context.Background(), `DELETE i FROM tech_card_operation_input i
			JOIN tech_card_operation o ON o.id = i.operation_id WHERE o.tech_card_id = ?`, id)
		_ = T.DeleteTechCard(context.Background(), id)
	})
	return r
}

func (r *cutRig) addColourway() {
	r.t.Helper()
	mediaID, err := r.rt.store.Media().AddMedia(r.ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(r.t, err)
	res, err := testDB.ExecContext(r.ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?)`, fmt.Sprintf("CUTS-CW-%d", r.cardID), mediaID, r.cardID)
	require.NoError(r.t, err)
	id, err := res.LastInsertId()
	require.NoError(r.t, err)
	r.cwID = int(id)
	r.t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", id) })
}

func (r *cutRig) read() *entity.TechCard {
	r.t.Helper()
	card, err := r.T.GetTechCardById(r.ctx, r.cardID)
	require.NoError(r.t, err)
	return card
}

func (r *cutRig) wire() *pb_common.TechCard {
	r.t.Helper()
	return dto.ConvertEntityTechCardToPb(r.read(), dto.CostingFx{})
}

// cutsEveryOtherFlag says what the clone says: the body knows every capability but the strips.
func cutsEveryOtherFlag(in *pb_common.TechCardInsert) {
	in.AssemblyAware = true
	in.MachineFieldsAware = true
	in.MediaAware = true
	in.OperationKindsAware = true
	in.BomQtyAware = true
	in.OperationWorkAware = true
	in.OperationDraftAware = true
	in.OperationReviewAware = true
	in.OperationBlockMembershipAware = true
	in.OperationSeamsAware = true
	in.OperationFoldsAware = true
	in.OperationLayerOrderAware = true
	in.OperationSeamToleranceAware = true
	in.PackagingPicksAware = true
	in.OperationSeamKindAware = true
	in.LabelsAware = true
}

func (r *cutRig) send(in *pb_common.TechCardInsert, version int) error {
	r.t.Helper()
	_, err := r.rt.srv.UpdateTechCard(r.rt.adminCtx("cut.owner@example.com"), &pb_admin.UpdateTechCardRequest{
		Id: int32(r.cardID), TechCard: in, ExpectedLockVersion: int32(version),
	})
	return err
}

// save is a client that knows strips: it echoes what it read, then edits.
func (r *cutRig) save(edit func(in *pb_common.TechCardInsert)) error {
	r.t.Helper()
	card := r.read()
	full := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{})
	in := full.GetTechCard()
	cutsEveryOtherFlag(in)
	in.CutStrips = full.GetCutStrips()
	in.CutStripsAware = true
	if edit != nil {
		edit(in)
	}
	return r.send(in, card.LockVersion)
}

// saveOlder is a client from before the strips: it knows every other capability and says nothing here.
func (r *cutRig) saveOlder(edit func(in *pb_common.TechCardInsert)) error {
	r.t.Helper()
	card := r.read()
	in := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{}).GetTechCard()
	cutsEveryOtherFlag(in)
	in.CutStrips, in.CutStripsAware = nil, false
	if edit != nil {
		edit(in)
	}
	return r.send(in, card.LockVersion)
}

func cutsDec(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

func cutsMm(v int32) *int32 { return &v }

func (r *cutRig) fromSeam() *pb_common.TechCardCutStrip {
	return &pb_common.TechCardCutStrip{
		LineKey: cutsStripSeam, BomLineKey: cutsLineRubber, LengthMode: entity.CutStripModeFromSeam,
		FitPct: cutsDec("90"), OverlapMm: cutsDec("20"), WidthMm: cutsDec("30"), Qty: 1,
		Sizes: []*pb_common.TechCardCutStripSize{
			{SizeId: int32(r.sizeA), SeamMm: cutsDec("700")},
			{SizeId: int32(r.sizeB), SeamMm: cutsDec("720")},
		},
	}
}

func (r *cutRig) oneLength() *pb_common.TechCardCutStrip {
	return &pb_common.TechCardCutStrip{
		LineKey: cutsStripOne, BomLineKey: cutsLineRubber, LengthMode: entity.CutStripModeOneLength,
		FitPct: cutsDec("100"), OverlapMm: cutsDec("0"), WidthMm: cutsDec("10"), Qty: 2, OneLengthMm: cutsMm(400),
	}
}

func (r *cutRig) perSize() *pb_common.TechCardCutStrip {
	return &pb_common.TechCardCutStrip{
		LineKey: cutsStripSize, BomLineKey: cutsLineTape, LengthMode: entity.CutStripModePerSize,
		FitPct: cutsDec("100"), OverlapMm: cutsDec("5"), WidthMm: cutsDec("12"), Qty: 1,
		Sizes: []*pb_common.TechCardCutStripSize{
			{SizeId: int32(r.sizeA), ManualCutMm: cutsMm(300)},
			{SizeId: int32(r.sizeB), ManualCutMm: cutsMm(310)},
		},
	}
}

func (r *cutRig) stripOf(key string) *pb_common.TechCardCutStrip {
	r.t.Helper()
	for _, s := range r.wire().GetCutStrips() {
		if s.GetLineKey() == key {
			return s
		}
	}
	r.t.Fatalf("no strip %s on the card", key)
	return nil
}

// cuts is size id → the cut length the wire says. A size with no number has no entry.
func cutsOfWire(s *pb_common.TechCardCutStrip) map[int]int {
	out := map[int]int{}
	for _, row := range s.GetSizes() {
		if row.CutMm != nil {
			out[int(row.GetSizeId())] = int(row.GetCutMm())
		}
	}
	return out
}

// normOf is the size norms of one BOM line as the card's norm list says them, in metres.
func cutsNormOf(card *pb_common.TechCard, line string) map[int]string {
	for _, n := range card.GetCutStripNorms() {
		if n.GetBomLineKey() != line {
			continue
		}
		out := map[int]string{}
		for _, row := range n.GetSizeNorms() {
			out[int(row.GetSizeId())] = decimal.RequireFromString(row.GetConsumption().GetValue()).String()
		}
		return out
	}
	return nil
}

func cutsCount(t *testing.T, ctx context.Context, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx, query, args...).Scan(&n))
	return n
}

func cutsRequireField(t *testing.T, err error, reason string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err), err.Error())
	require.Contains(t, err.Error(), reason)
}

func (r *cutRig) strips() map[string]int {
	r.t.Helper()
	rows, err := testDB.QueryContext(r.ctx, "SELECT line_key, id FROM tech_card_cut_strip WHERE tech_card_id = ?", r.cardID)
	require.NoError(r.t, err)
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var key string
		var id int
		require.NoError(r.t, rows.Scan(&key, &id))
		out[key] = id
	}
	require.NoError(r.t, rows.Err())
	return out
}

// TestTechCardCutStripsRoundTrip is the save path end to end: wire, converter, store, read, and every
// number the wire gives back.
func TestTechCardCutStripsRoundTrip(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	r := newCutRig(t, ctx)

	// ── a card with no strips stays one ──
	before := r.wire()
	require.Empty(t, before.GetCutStrips())
	require.Empty(t, before.GetCutStripNorms())
	require.NoError(t, r.save(nil))
	require.Empty(t, r.strips())

	// ── the three modes ──
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.CutStrips = []*pb_common.TechCardCutStrip{r.fromSeam(), r.oneLength(), r.perSize()}
	}))
	require.Equal(t, map[int]int{r.sizeA: 650, r.sizeB: 668}, cutsOfWire(r.stripOf(cutsStripSeam)), "700 x 90% + 20, 720 x 90% + 20")
	require.Equal(t, map[int]int{r.sizeA: 400, r.sizeB: 400}, cutsOfWire(r.stripOf(cutsStripOne)))
	require.Equal(t, map[int]int{r.sizeA: 300, r.sizeB: 310}, cutsOfWire(r.stripOf(cutsStripSize)))
	seamRow := r.stripOf(cutsStripSeam).GetSizes()[0]
	require.True(t, decimal.RequireFromString(seamRow.GetSewnMm().GetValue()).Equal(decimal.NewFromInt(630)), "sewn is cut minus overlap")

	// ── the line's norm: the sum over its strips, by size ──
	wire := r.wire()
	require.Equal(t, map[int]string{r.sizeA: "1.45", r.sizeB: "1.468"}, cutsNormOf(wire, cutsLineRubber), "650 + 2 x 400, 668 + 2 x 400")
	require.Equal(t, map[int]string{r.sizeA: "0.3", r.sizeB: "0.31"}, cutsNormOf(wire, cutsLineTape))
	for _, n := range wire.GetCutStripNorms() {
		require.True(t, n.GetComplete())
	}

	// ── an echo moves nothing, a key keeps its row ──
	ids := r.strips()
	require.Len(t, ids, 3)
	digests := wire.GetSectionDigests()
	require.NoError(t, r.save(nil))
	require.Equal(t, ids, r.strips())
	require.Equal(t, digests, r.wire().GetSectionDigests(), "the digest a read shows is the digest an echo keeps")

	// ── a typed value on one size wins and stays; clearing it returns to the formula ──
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.GetCutStrips()[0].GetSizes()[0].ManualCutMm = cutsMm(900)
	}))
	require.Equal(t, map[int]int{r.sizeA: 900, r.sizeB: 668}, cutsOfWire(r.stripOf(cutsStripSeam)))
	require.NoError(t, r.save(nil))
	require.Equal(t, 900, cutsOfWire(r.stripOf(cutsStripSeam))[r.sizeA], "an echo keeps the typed value")
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.GetCutStrips()[0].GetSizes()[0].ManualCutMm = nil
	}))
	require.Equal(t, 650, cutsOfWire(r.stripOf(cutsStripSeam))[r.sizeA])

	// ── an unknown length is no number, and no norm for that size ──
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.GetCutStrips()[0].GetSizes()[1].SeamMm = nil
		in.GetCutStrips()[0].GetSizes()[1].ManualCutMm = nil
	}))
	wire = r.wire()
	require.Equal(t, map[int]int{r.sizeA: 650}, cutsOfWire(r.stripOf(cutsStripSeam)))
	require.Equal(t, map[int]string{r.sizeA: "1.45"}, cutsNormOf(wire, cutsLineRubber))
	for _, n := range wire.GetCutStripNorms() {
		if n.GetBomLineKey() == cutsLineRubber {
			require.False(t, n.GetComplete())
		}
	}
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.GetCutStrips()[0].GetSizes()[1].SeamMm = cutsDec("720")
	}))

	// ── the size range: rows of a size that left go, and the norm with them ──
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) { in.SizeIds = []int32{int32(r.sizeA)} }))
	require.Zero(t, cutsCount(t, ctx, `SELECT COUNT(*) FROM tech_card_cut_strip_size z JOIN tech_card_cut_strip s ON s.id = z.strip_id
		WHERE s.tech_card_id = ? AND z.size_id = ?`, r.cardID, r.sizeB))
	require.Equal(t, map[int]string{r.sizeA: "1.45"}, cutsNormOf(r.wire(), cutsLineRubber))
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) { in.SizeIds = []int32{int32(r.sizeA), int32(r.sizeB)} }))

	// ── the rules of the mark, each refused with its field and leaving the card as it was ──
	ids = r.strips()
	wrong := r.fromSeam()
	wrong.LineKey, wrong.BomLineKey = "01JCVT000000000000000000C9", cutsLineShell
	cutsRequireField(t, r.save(func(in *pb_common.TechCardInsert) { in.CutStrips = append(in.CutStrips, wrong) }), "cut_strip_main_fabric")
	wrong.BomLineKey = cutsLineButton
	cutsRequireField(t, r.save(func(in *pb_common.TechCardInsert) { in.CutStrips = append(in.CutStrips, wrong) }), "cut_strip_not_metres")
	wrong.BomLineKey = "01JCVT000000000000000000A9"
	cutsRequireField(t, r.save(func(in *pb_common.TechCardInsert) { in.CutStrips = append(in.CutStrips, wrong) }), "cut_strip_bom_line_missing")
	wrong.BomLineKey, wrong.LineKey = cutsLineRubber, cutsPieceFront
	cutsRequireField(t, r.save(func(in *pb_common.TechCardInsert) { in.CutStrips = append(in.CutStrips, wrong) }), "cut_strip_key_taken")
	require.Equal(t, ids, r.strips())

	// ── the linked article's unit is the line's unit, on the store's side too ──
	matID, err := r.T.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "counted braid", Section: "hardware", Unit: sql.NullString{String: "pcs", Valid: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", matID) })
	err = r.save(func(in *pb_common.TechCardInsert) {
		for _, line := range in.GetBomItems() {
			if line.GetLineKey() == cutsLineRubber {
				line.MaterialId = int64(matID)
			}
		}
	})
	cutsRequireField(t, err, "cut_strip_not_metres")

	// ── unchecking an unused strip deletes it, and its size rows ──
	stripID := ids[cutsStripSeam]
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.CutStrips = []*pb_common.TechCardCutStrip{r.oneLength(), r.perSize()}
	}))
	require.Zero(t, cutsCount(t, ctx, "SELECT COUNT(*) FROM tech_card_cut_strip WHERE id = ?", stripID))
	require.Zero(t, cutsCount(t, ctx, "SELECT COUNT(*) FROM tech_card_cut_strip_size WHERE strip_id = ?", stripID))
	require.Equal(t, ids[cutsStripOne], r.strips()[cutsStripOne], "the others keep their rows")

	// ── the card goes, the strips and the step inputs on them go. The step takes only the strip here, since a
	// piece input holds its piece by RESTRICT (0307) and would refuse this delete with or without strips. ──
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		for _, op := range in.GetOperations() {
			op.InputKeys, op.PieceLineKeys = nil, nil
		}
		in.GetOperations()[0].InputKeys = []string{cutsStripOne}
	}))
	require.Equal(t, 1, cutsCount(t, ctx, `SELECT COUNT(*) FROM tech_card_operation_input i
		JOIN tech_card_operation o ON o.id = i.operation_id WHERE o.tech_card_id = ? AND i.cut_strip_id IS NOT NULL`, r.cardID))
	require.NoError(t, r.T.DeleteTechCard(ctx, r.cardID))
	require.Zero(t, cutsCount(t, ctx, "SELECT COUNT(*) FROM tech_card_cut_strip WHERE tech_card_id = ?", r.cardID))
}

// TestTechCardCutStripsOlderClient: no aware flag, the strips are not its business.
func TestTechCardCutStripsOlderClient(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	r := newCutRig(t, ctx)
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.CutStrips = []*pb_common.TechCardCutStrip{r.fromSeam(), r.perSize()}
	}))
	ids := r.strips()
	wire := r.wire()

	require.NoError(t, r.saveOlder(func(in *pb_common.TechCardInsert) { in.Name = "edited by an older tab" }))
	after := r.wire()
	require.Equal(t, "edited by an older tab", after.GetTechCard().GetName())
	require.Equal(t, ids, r.strips(), "rows untouched")
	require.Equal(t, cutsOfWire(wire.GetCutStrips()[0]), cutsOfWire(after.GetCutStrips()[0]))
	require.Equal(t, cutsNormOf(wire, cutsLineRubber), cutsNormOf(after, cutsLineRubber))

	// A strip list with no flag is a client bug, and it is refused instead of ignored.
	card := r.read()
	in := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{}).GetTechCard()
	cutsEveryOtherFlag(in)
	in.CutStrips = []*pb_common.TechCardCutStrip{r.fromSeam()}
	cutsRequireField(t, r.send(in, card.LockVersion), "aware_required")

	// An older client removing the line takes that line's unused strip with it, by cascade.
	require.NoError(t, r.saveOlder(func(in *pb_common.TechCardInsert) {
		kept := in.GetBomItems()[:0]
		for _, line := range in.GetBomItems() {
			if line.GetLineKey() != cutsLineTape {
				kept = append(kept, line)
			}
		}
		in.BomItems = kept
	}))
	_, stays := r.strips()[cutsStripSize]
	require.False(t, stays)
	require.Contains(t, r.strips(), cutsStripSeam)
}

func cutsOpByNote(t *testing.T, card *pb_common.TechCardInsert, note string) *pb_common.TechCardOperation {
	t.Helper()
	for _, op := range card.GetOperations() {
		if op.GetNote() == note {
			return op
		}
	}
	t.Fatalf("no step %q", note)
	return nil
}

func cutsSeamInput(piece, strip string) entity.TechCardSeamInput {
	anchor := func(key string) entity.TechCardSeamAnchor {
		return entity.TechCardSeamAnchor{
			PieceLineKey: key,
			Samples:      []entity.TechCardSeamSample{{U: 0, V: 0}, {U: 0.25, V: 0.1}, {U: 0.5, V: 0.2}, {U: 0.75, V: 0.3}, {U: 1, V: 0.4}},
			PerimShare:   0.25, LenMm: 120, Notches: 1, TurnDeg: -2,
		}
	}
	return entity.TechCardSeamInput{
		SeamKey: cutsSeamKey, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
		Direction: entity.TechCardSeamSame, Source: entity.TechCardSeamSourceManual,
		SideA: []entity.TechCardSeamAnchor{anchor(piece)}, SideB: []entity.TechCardSeamAnchor{anchor(strip)},
	}
}

// TestTechCardCutStripInputsAndSeams: a strip is an input of a step and an end of a seam by its key, and
// while it is one, it cannot be unchecked.
func TestTechCardCutStripInputsAndSeams(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	r := newCutRig(t, ctx)
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.CutStrips = []*pb_common.TechCardCutStrip{r.fromSeam(), r.oneLength()}
	}))

	inputRows := func() int {
		return cutsCount(t, ctx, `SELECT COUNT(*) FROM tech_card_operation_input i JOIN tech_card_cut_strip s ON s.id = i.cut_strip_id
			WHERE s.tech_card_id = ? AND s.line_key = ?`, r.cardID, cutsStripSeam)
	}
	keysOf := func(note string) []string {
		return cutsOpByNote(t, r.wire().GetTechCard(), note).GetInputKeys()
	}

	// ── a step takes the strip, and an echo keeps it, the wire shape unchanged ──
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		op := cutsOpByNote(t, in, "a")
		op.InputKeys = []string{cutsPieceFront, cutsStripSeam}
	}))
	require.Equal(t, []string{cutsPieceFront, cutsStripSeam}, keysOf("a"))
	require.Equal(t, 1, inputRows())
	require.NoError(t, r.save(nil))
	require.Equal(t, []string{cutsPieceFront, cutsStripSeam}, keysOf("a"), "a re-save keeps the input")
	require.Equal(t, 1, inputRows())

	// ── a reorder keeps it on its step ──
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		ops := in.GetOperations()
		ops[0], ops[1] = ops[1], ops[0]
	}))
	require.Equal(t, []string{cutsPieceFront, cutsStripSeam}, keysOf("a"))

	// ── an edit of the strip keeps the link: same row, same key ──
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		for _, s := range in.GetCutStrips() {
			if s.GetLineKey() == cutsStripSeam {
				s.FitPct = cutsDec("80")
			}
		}
	}))
	require.Equal(t, []string{cutsPieceFront, cutsStripSeam}, keysOf("a"))

	// ── a used strip cannot be unchecked, by either client, and nothing moves ──
	ids := r.strips()
	cutsRequireField(t, r.save(func(in *pb_common.TechCardInsert) {
		in.CutStrips = []*pb_common.TechCardCutStrip{r.oneLength()}
	}), "cut_strip_in_use")
	cutsRequireField(t, r.save(func(in *pb_common.TechCardInsert) {
		kept := in.GetBomItems()[:0]
		for _, line := range in.GetBomItems() {
			if line.GetLineKey() != cutsLineRubber {
				kept = append(kept, line)
			}
		}
		in.BomItems, in.CutStrips = kept, nil
	}), "cut_strip_in_use")
	cutsRequireField(t, r.saveOlder(func(in *pb_common.TechCardInsert) {
		kept := in.GetBomItems()[:0]
		for _, line := range in.GetBomItems() {
			if line.GetLineKey() != cutsLineRubber {
				kept = append(kept, line)
			}
		}
		in.BomItems = kept
	}), "cut_strip_in_use")
	require.Equal(t, ids, r.strips())
	require.Equal(t, 1, inputRows())

	// ── a bundle from before the assembly inputs would erase the input, so it is refused outright ──
	card := r.read()
	old := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{}).GetTechCard()
	cutsEveryOtherFlag(old)
	old.AssemblyAware = false
	for _, op := range old.GetOperations() {
		op.InputKeys = nil
	}
	err := r.send(old, card.LockVersion)
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err), err.Error())
	require.Equal(t, 1, inputRows())

	// ── the same save may let go of both ──
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		cutsOpByNote(t, in, "a").InputKeys = []string{cutsPieceFront}
		in.CutStrips = []*pb_common.TechCardCutStrip{r.oneLength()}
	}))
	require.NotContains(t, r.strips(), cutsStripSeam)
	require.Equal(t, []string{cutsPieceFront}, keysOf("a"))

	// ── a seam with a strip end: accepted, fresh, and it holds the strip ──
	_, written, err := r.T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{
		TechCardId: r.cardID, By: "cut.owner@example.com", Seams: []entity.TechCardSeamInput{cutsSeamInput(cutsPieceFront, cutsStripOne)},
	})
	require.NoError(t, err)
	require.Equal(t, 1, written)
	got := r.read()
	require.Len(t, got.Seams, 1)
	require.False(t, got.Seams[0].Stale, "a strip end adds no fabric source")

	// A seam end that is neither a piece nor a strip of this card is still refused.
	_, _, err = r.T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{
		TechCardId: r.cardID, By: "cut.owner@example.com", Seams: []entity.TechCardSeamInput{cutsSeamInput(cutsPieceFront, "01JCVT000000000000000000C8")},
	})
	require.Error(t, err)

	ids = r.strips()
	cutsRequireField(t, r.save(func(in *pb_common.TechCardInsert) { in.CutStrips = nil }), "cut_strip_in_use")
	cutsRequireField(t, r.saveOlder(func(in *pb_common.TechCardInsert) {
		kept := in.GetBomItems()[:0]
		for _, line := range in.GetBomItems() {
			if line.GetLineKey() != cutsLineRubber {
				kept = append(kept, line)
			}
		}
		in.BomItems = kept
	}), "cut_strip_in_use")
	require.Equal(t, ids, r.strips())
	require.Len(t, r.read().Seams, 1)

	// An echo with the seam on leaves the seam as it was.
	require.NoError(t, r.save(nil))
	got = r.read()
	require.Len(t, got.Seams, 1)
	require.False(t, got.Seams[0].Stale)

	// ── the seam gone, the strip may go ──
	_, _, err = r.T.DeleteTechCardSeams(ctx, r.cardID, []string{cutsSeamKey})
	require.NoError(t, err)
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) { in.CutStrips = nil }))
	require.Empty(t, r.strips())
}

// TestTechCardCutStripNormOnRead is the norm through the store: resolved on read, on a colourway with no
// recipe row, and never stored. The fabric line and the counted line around it read as they did.
func TestTechCardCutStripNormOnRead(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	r := newCutRig(t, ctx)
	r.addColourway()

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	card := r.read()
	version, err := r.T.UpdateColorwayRecipe(ctx, r.cwID, card.LockVersion, []entity.TechCardColorwayUsage{
		{BomLineKey: cutsLineShell, Placement: ns("outer"), Color: ns("black"),
			Consumption: decimal.NewNullDecimal(decimal.RequireFromString("1.5"))},
	})
	require.NoError(t, err)
	require.Equal(t, card.LockVersion+1, version)

	// A card with no strips: the recipe is what was stored, one row, no made-up line.
	recipe, err := r.T.GetColorwayRecipe(ctx, r.cwID)
	require.NoError(t, err)
	require.Len(t, recipe, 1)

	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.CutStrips = []*pb_common.TechCardCutStrip{r.fromSeam(), r.oneLength()}
	}))

	// A stored row reads with its bom_item_id, a made-up one has both.
	lineIDs := map[string]int64{}
	for _, line := range card.BomItems {
		lineIDs[line.LineKey] = int64(line.Id)
	}
	usageOf := func(usages []entity.TechCardColorwayUsage, line string) *entity.TechCardColorwayUsage {
		for i := range usages {
			if usages[i].BomItemId.Valid && usages[i].BomItemId.Int64 == lineIDs[line] {
				return &usages[i]
			}
		}
		return nil
	}
	check := func(usages []entity.TechCardColorwayUsage) {
		t.Helper()
		rubber := usageOf(usages, cutsLineRubber)
		require.NotNil(t, rubber, "the golden line has no recipe row and still gets its norm")
		require.Equal(t, entity.ConsumptionSourceCutLength, rubber.ConsumptionSource.String)
		bySize := map[int]string{}
		for _, row := range rubber.SizeConsumptions {
			bySize[row.SizeId] = row.Consumption.String()
		}
		require.Equal(t, map[int]string{r.sizeA: "1.45", r.sizeB: "1.468"}, bySize)
		shell := usageOf(usages, cutsLineShell)
		require.NotNil(t, shell)
		require.True(t, shell.Consumption.Decimal.Equal(decimal.RequireFromString("1.5")), "a fabric line keeps its number")
		require.NotEqual(t, entity.ConsumptionSourceCutLength, shell.ConsumptionSource.String)
	}

	// The two ways a recipe is read give the same number.
	got := r.read()
	var cw *entity.TechCardColorway
	for i := range got.Colorways {
		if got.Colorways[i].Id == r.cwID {
			cw = &got.Colorways[i]
		}
	}
	require.NotNil(t, cw)
	check(cw.Usages)
	recipe, err = r.T.GetColorwayRecipe(ctx, r.cwID)
	require.NoError(t, err)
	check(recipe)

	// Never written. The old binary sees no new value, and the CHECK of 0294 has nothing to say.
	require.Zero(t, cutsCount(t, ctx, `SELECT COUNT(*) FROM tech_card_colorway_usage u
		JOIN tech_card_bom_item b ON b.id = u.bom_item_id WHERE b.tech_card_id = ? AND b.line_key = ?`, r.cardID, cutsLineRubber))

	// ── a client echoing the read recipe writes no row for the strip line ──
	latest := r.read()
	for i := range recipe { // the wire names a line by its key
		for key, id := range lineIDs {
			if recipe[i].BomItemId.Int64 == id {
				recipe[i].BomLineKey = key
			}
		}
	}
	version, err = r.T.UpdateColorwayRecipe(ctx, r.cwID, latest.LockVersion, recipe)
	require.NoError(t, err)
	require.Zero(t, cutsCount(t, ctx, `SELECT COUNT(*) FROM tech_card_colorway_usage u
		JOIN tech_card_bom_item b ON b.id = u.bom_item_id WHERE b.tech_card_id = ? AND b.line_key = ?`, r.cardID, cutsLineRubber),
		"the echo of a made-up row is not a row")
	recipe, err = r.T.GetColorwayRecipe(ctx, r.cwID)
	require.NoError(t, err)
	check(recipe)

	// ── a number typed on a strip line is ignored; the colour on the same row stays ──
	version, err = r.T.UpdateColorwayRecipe(ctx, r.cwID, version, []entity.TechCardColorwayUsage{
		{BomLineKey: cutsLineShell, Placement: ns("outer"), Color: ns("black"),
			Consumption: decimal.NewNullDecimal(decimal.RequireFromString("1.5"))},
		{BomLineKey: cutsLineRubber, Color: ns("white"),
			Consumption: decimal.NewNullDecimal(decimal.RequireFromString("99"))},
	})
	require.NoError(t, err)
	var stored sql.NullString
	var color sql.NullString
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT CAST(u.consumption AS CHAR), u.color FROM tech_card_colorway_usage u
		JOIN tech_card_bom_item b ON b.id = u.bom_item_id WHERE b.tech_card_id = ? AND b.line_key = ?`, r.cardID, cutsLineRubber).Scan(&stored, &color))
	require.False(t, stored.Valid, "the typed number is not stored")
	require.Equal(t, "white", color.String)
	recipe, err = r.T.GetColorwayRecipe(ctx, r.cwID)
	require.NoError(t, err)
	check(recipe)
	require.Equal(t, "white", usageOf(recipe, cutsLineRubber).Color.String)

	// ── a claim of cut_length with no strip behind it is refused ──
	_, err = r.T.UpdateColorwayRecipe(ctx, r.cwID, version, []entity.TechCardColorwayUsage{
		{BomLineKey: cutsLineTape, ConsumptionSource: ns(entity.ConsumptionSourceCutLength)},
	})
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "cut_length_without_strip", ve.Reason)

	// ── the strips leave, and the line is what it was before: no number at all ──
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) { in.CutStrips = nil }))
	recipe, err = r.T.GetColorwayRecipe(ctx, r.cwID)
	require.NoError(t, err)
	rubber := usageOf(recipe, cutsLineRubber)
	require.NotNil(t, rubber, "the row with the colour stays")
	require.False(t, rubber.Consumption.Valid)
	require.Empty(t, rubber.SizeConsumptions)
}

// TestTechCardCutStripsOnACardWithNoColourways: the BOM line's norm has no recipe to ride.
func TestTechCardCutStripsOnACardWithNoColourways(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	r := newCutRig(t, ctx)
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.CutStrips = []*pb_common.TechCardCutStrip{r.oneLength()}
	}))
	wire := r.wire()
	require.Empty(t, wire.GetColorways())
	require.Len(t, wire.GetCutStripNorms(), 1)
	require.True(t, decimal.RequireFromString(wire.GetCutStripNorms()[0].GetNorm().GetValue()).Equal(decimal.RequireFromString("0.8")), "2 x 400 mm")
	for _, line := range r.read().BomItems {
		require.Equal(t, line.LineKey == cutsLineRubber, line.CutToLength, line.LineKey)
	}
}

// TestTechCardCutStripsClone: the clone has its own rows of the same strips, the step inputs follow, the
// seams do not.
func TestTechCardCutStripsClone(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	r := newCutRig(t, ctx)
	seamed := r.fromSeam()
	seamed.SeamKey = cutsSeamKey
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.CutStrips = []*pb_common.TechCardCutStrip{seamed, r.oneLength(), r.perSize()}
		cutsOpByNote(t, in, "a").InputKeys = []string{cutsPieceFront, cutsStripSeam}
	}))
	src := r.read()
	resp, err := r.rt.srv.CloneStyleForSeason(r.rt.adminCtx("cut.owner@example.com"), &pb_admin.CloneStyleForSeasonRequest{
		SourceStyleId: int64(r.cardID), ExpectedSourceVersion: uint64(src.LockVersion),
		SkuSeason: &pb_common.SkuSeason{Code: pb_common.SeasonEnum_SEASON_ENUM_FW, Year: 2031},
	})
	require.NoError(t, err)
	newID := int(resp.GetNewStyleId())
	require.NotZero(t, newID)
	require.NotEqual(t, r.cardID, newID)
	t.Cleanup(func() { _ = r.T.DeleteTechCard(context.Background(), newID) })

	clone, err := r.T.GetTechCardById(ctx, newID)
	require.NoError(t, err)
	wire := dto.ConvertEntityTechCardToPb(clone, dto.CostingFx{})
	require.Len(t, wire.GetCutStrips(), 3)
	require.Equal(t, map[int]int{r.sizeA: 650, r.sizeB: 668}, cutsOfWire(wire.GetCutStrips()[0]))
	require.Empty(t, wire.GetCutStrips()[0].GetSeamKey(), "seams are not copied, the seam lengths are")
	require.Equal(t, cutsNormOf(r.wire(), cutsLineRubber), cutsNormOf(wire, cutsLineRubber))
	require.Equal(t, []string{cutsPieceFront, cutsStripSeam}, cutsOpByNote(t, wire.GetTechCard(), "a").GetInputKeys())

	var sourceRow, cloneRow int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM tech_card_cut_strip WHERE tech_card_id = ? AND line_key = ?", r.cardID, cutsStripSeam).Scan(&sourceRow))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM tech_card_cut_strip WHERE tech_card_id = ? AND line_key = ?", newID, cutsStripSeam).Scan(&cloneRow))
	require.NotEqual(t, sourceRow, cloneRow, "the same key on another card is another row")
}

// cutsSnapshot freezes the card as a save does. Older removes the strip list, which is what a row written
// by the binary before this feature looks like.
func (r *cutRig) cutsSnapshot(older bool) int64 {
	r.t.Helper()
	card := r.read()
	write := card.TechCardInsert
	write.UpdatedBy = "cut.owner@example.com"
	write.SnapshotSaveSessionId = "44444444-4444-4444-8444-444444444444"
	write.SnapshotChangedPaths = []string{"overview"}
	write.CutStripsAware = false // the store's save of a read card, not a strip edit
	write.BuildSnapshotFromLockedCard = func(locked *entity.TechCard) (string, error) {
		blob, err := protojson.Marshal(dto.ConvertEntityTechCardToPb(locked, dto.CostingFx{}))
		if err != nil {
			return "", err
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(blob, &top); err != nil {
			return "", err
		}
		if _, ok := top["seams"]; !ok {
			top["seams"] = json.RawMessage(`[]`)
		}
		if older {
			delete(top, "cutStrips")
			delete(top, "cutStripNorms")
		} else if _, ok := top["cutStrips"]; !ok {
			top["cutStrips"] = json.RawMessage(`[]`)
		}
		blob, err = json.Marshal(top)
		return string(blob), err
	}
	r.rt.store.ts = r.rt.store.ts.Add(time.Hour) // a new autosave bucket: one row per call
	require.NoError(r.t, r.T.UpdateTechCard(r.ctx, r.cardID, &write, card.LockVersion))
	rows, err := r.T.ListTechCardSnapshots(r.ctx, r.cardID, 0, 1)
	require.NoError(r.t, err)
	require.Len(r.t, rows, 1)
	return rows[0].Id
}

func (r *cutRig) restore(snapshotID int64) error {
	r.t.Helper()
	_, err := r.rt.srv.RestoreTechCardSnapshot(r.rt.adminCtx("cut.owner@example.com"), &pb_admin.RestoreTechCardSnapshotRequest{
		TechCardId: int32(r.cardID), SnapshotId: snapshotID, ExpectedLockVersion: int32(r.read().LockVersion),
	})
	return err
}

// TestTechCardCutStripsRestore: only a snapshot that states the list may replace it.
func TestTechCardCutStripsRestore(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	r := newCutRig(t, ctx)
	r.rt.store.ts = time.Date(2026, time.October, 24, 12, 0, 0, 0, time.UTC)

	// ── a snapshot from before the feature: the strips added since stay ──
	oldRow := r.cutsSnapshot(true)
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.CutStrips = []*pb_common.TechCardCutStrip{r.fromSeam(), r.perSize()}
		cutsOpByNote(t, in, "a").InputKeys = []string{cutsPieceFront, cutsStripSeam}
	}))
	ids := r.strips()
	require.NoError(t, r.restore(oldRow))
	require.Equal(t, ids, r.strips(), "a restore of an old snapshot leaves the strips alone")
	require.Equal(t, map[int]int{r.sizeA: 650, r.sizeB: 668}, cutsOfWire(r.stripOf(cutsStripSeam)))

	// ── a snapshot that has them brings them back: the values, and the row of the same key ──
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		cutsOpByNote(t, in, "a").InputKeys = []string{cutsPieceFront, cutsStripSeam}
	}))
	withStrips := r.cutsSnapshot(false)
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		cutsOpByNote(t, in, "a").InputKeys = []string{cutsPieceFront}
		in.CutStrips = []*pb_common.TechCardCutStrip{r.oneLength()}
	}))
	require.NotContains(t, r.strips(), cutsStripSeam)
	require.NoError(t, r.restore(withStrips))
	back := r.strips()
	require.Contains(t, back, cutsStripSeam)
	require.Contains(t, back, cutsStripSize)
	require.NotContains(t, back, cutsStripOne, "the list of the snapshot replaces the list of the card")
	require.Equal(t, map[int]int{r.sizeA: 650, r.sizeB: 668}, cutsOfWire(r.stripOf(cutsStripSeam)))
	require.Contains(t, cutsOpByNote(t, r.wire().GetTechCard(), "a").GetInputKeys(), cutsStripSeam, "and the step takes it again")

	// ── a snapshot that says «none» means none ──
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		cutsOpByNote(t, in, "a").InputKeys = []string{cutsPieceFront}
		in.CutStrips = nil
	}))
	none := r.cutsSnapshot(false)
	require.NoError(t, r.save(func(in *pb_common.TechCardInsert) {
		in.CutStrips = []*pb_common.TechCardCutStrip{r.oneLength()}
	}))
	require.NoError(t, r.restore(none))
	require.Empty(t, r.strips())
}

// TestTechCardCutStripMigrationIsIdempotent reruns the file over itself under the production sql_mode.
func TestTechCardCutStripMigrationIsIdempotent(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := testDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "SET SESSION sql_mode = CONCAT(@@SESSION.sql_mode, ',ANSI_QUOTES')")
	require.NoError(t, err)
	up := migrationUpBody(t, "0449_tech_card_cut_strip.sql")
	for i := 0; i < 2; i++ {
		_, err = conn.ExecContext(ctx, up)
		require.NoError(t, err, "re-run %d", i+1)
	}
	var n int
	require.NoError(t, conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation_input' AND COLUMN_NAME = 'cut_strip_id'`).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tech_card_operation_input' AND CONSTRAINT_NAME = 'fk_op_input_cut_strip'`).Scan(&n))
	require.Equal(t, 1, n)
}

const (
	cutsArchRubber = "01JCVT0000000000000000AA01"
	cutsArchSeam   = "01JCVT0000000000000000AB01"
	cutsArchOne    = "01JCVT0000000000000000AB02"
)

func cutsArchCounter(rep *pb_admin.TechCardImportReport) (imported, skipped int, ok bool) {
	for _, c := range rep.GetCounters() {
		if c.GetEntity() == techcardarchive.EntityCutStrip {
			return int(c.GetImported()), int(c.GetSkipped()), true
		}
	}
	return 0, 0, false
}

// cutsArchCard puts a metre line with two strips on the maximal card, the first strip an input of a step.
func cutsArchCard(t *testing.T, ctx context.Context, rig *rtRig, fx rtFixture) *entity.TechCard {
	t.Helper()
	T := rig.store.TechCards()
	card, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	write := card.TechCardInsert
	write.UpdatedBy = rtActor
	write.BomItems = append(append([]entity.TechCardBomItem(nil), write.BomItems...), entity.TechCardBomItem{
		LineKey: cutsArchRubber, Section: entity.BomSectionHardware, Name: "резинка",
		Unit: sql.NullString{String: "m", Valid: true},
	})
	hundred := decimal.NewFromInt(100)
	write.CutStripsAware = true
	write.CutStrips = []entity.TechCardCutStrip{
		{
			LineKey: cutsArchSeam, BomLineKey: cutsArchRubber, LengthMode: entity.CutStripModeFromSeam,
			FitPct: decimal.NewFromInt(90), OverlapMm: decimal.NewFromInt(20), WidthMm: decimal.NewFromInt(30), Qty: 1,
			Sizes: []entity.TechCardCutStripSize{
				{SizeId: fx.sizeA, SeamMm: decimal.NewNullDecimal(decimal.NewFromInt(700))},
				{SizeId: fx.sizeB, SeamMm: decimal.NewNullDecimal(decimal.NewFromInt(720)), ManualCutMm: sql.NullInt32{Int32: 900, Valid: true}},
			},
		},
		{
			LineKey: cutsArchOne, BomLineKey: cutsArchRubber, LengthMode: entity.CutStripModeOneLength,
			FitPct: hundred, OverlapMm: decimal.Zero, WidthMm: decimal.NewFromInt(10), Qty: 2,
			OneLengthMm: sql.NullInt32{Int32: 400, Valid: true},
		},
	}
	require.NoError(t, T.UpdateTechCard(ctx, fx.cardID, &write, card.LockVersion))

	var opID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM tech_card_operation WHERE tech_card_id = ? AND note = ?", fx.cardID, foldArchNoteA).Scan(&opID))
	var stripID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM tech_card_cut_strip WHERE tech_card_id = ? AND line_key = ?", fx.cardID, cutsArchSeam).Scan(&stripID))
	_, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_operation_input (operation_id, cut_strip_id, display_order)
		SELECT ?, ?, COALESCE(MAX(display_order), -1) + 1 FROM tech_card_operation_input WHERE operation_id = ?`, opID, stripID, opID)
	require.NoError(t, err)
	got, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	return got
}

func cutsArchStepKeys(card *entity.TechCard) []string {
	for _, op := range card.Operations {
		if op.Note.String == foldArchNoteA {
			return op.InputKeys
		}
	}
	return nil
}

// TestTechCardArchiveCarriesCutStrips is the round trip of the strips: they ride card.json, the format
// stays 1.3 and no file is added. Then the ways an archive may lack something, none of which fail the
// import.
func TestTechCardArchiveCarriesCutStrips(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	rig := foldArchRig(t, ctx)
	fx := rtBuildMaximalCard(t, rig)
	T := rig.store.TechCards()
	src := cutsArchCard(t, ctx, rig, fx)
	require.Len(t, src.CutStrips, 2)
	require.Contains(t, cutsArchStepKeys(src), cutsArchSeam)
	srcWire := dto.ConvertEntityTechCardToPb(src, dto.CostingFx{})

	// ── EXPORT ──
	archive, _ := rig.export(t, fx.cardID)
	arch, err := techcardarchive.OpenArchive(bytes.NewReader(archive), int64(len(archive)))
	require.NoError(t, err)
	require.Equal(t, "1.3", arch.Manifest.FormatVersion, "the strips do not bump the format")
	cardJSON, err := arch.CardJSON()
	require.NoError(t, err)
	require.Len(t, cardJSON.GetCutStrips(), 2)

	// ── IMPORT: the same strips, the same numbers, the same input ──
	newID, rep := rig.importArchive(t, archive)
	got, err := T.GetTechCardById(ctx, newID)
	require.NoError(t, err)
	require.Len(t, got.CutStrips, 2)
	gotWire := dto.ConvertEntityTechCardToPb(got, dto.CostingFx{})
	for i, strip := range gotWire.GetCutStrips() {
		require.Equal(t, srcWire.GetCutStrips()[i].GetLineKey(), strip.GetLineKey())
		require.Equal(t, srcWire.GetCutStrips()[i].GetLengthMode(), strip.GetLengthMode())
		require.Equal(t, cutsOfWire(srcWire.GetCutStrips()[i]), cutsOfWire(strip))
	}
	require.Equal(t, map[int]int{fx.sizeA: 650, fx.sizeB: 900}, cutsOfWire(gotWire.GetCutStrips()[0]), "the typed value came along")
	require.Equal(t, cutsNormOf(srcWire, cutsArchRubber), cutsNormOf(gotWire, cutsArchRubber))
	require.Contains(t, cutsArchStepKeys(got), cutsArchSeam, "the step takes the strip on the new card")
	imported, skipped, ok := cutsArchCounter(rep)
	require.True(t, ok, "a card that carries strips has a strip counter")
	require.Equal(t, 2, imported)
	require.Zero(t, skipped)

	// ── A BAD ROW, A ROW OF A LINE THE CARD LACKS: dropped, reported, the rest land, the input goes with it ──
	const keyOrphan = "01JCVT0000000000000000AB03"
	const keyBroken = "01JCVT0000000000000000AB04"
	zipHoles := foldArchEditCard(t, archive, func(card map[string]any) {
		rows, ok := card["cutStrips"].([]any)
		require.True(t, ok)
		orphan := map[string]any{}
		for k, v := range rows[0].(map[string]any) {
			orphan[k] = v
		}
		orphan["lineKey"] = keyOrphan
		orphan["bomLineKey"] = "01JCVT0000000000000000AA99"
		broken := map[string]any{}
		for k, v := range rows[0].(map[string]any) {
			broken[k] = v
		}
		broken["lineKey"] = keyBroken
		broken["lengthMode"] = "by_eye"
		card["cutStrips"] = append(rows, orphan, broken)
		ops := foldArchOps(t, card)
		ops[0]["inputKeys"] = append(asAnyList(ops[0]["inputKeys"]), keyOrphan)
	})
	holesID, repHoles := rig.importArchive(t, zipHoles)
	require.Positive(t, holesID, "a bad strip never fails the import")
	rtRequireReportReason(t, repHoles, techcardarchive.EntityCutStrip, "cut_strip_key="+keyOrphan,
		string(techcardarchive.ReasonCutStripLineUnusable))
	rtRequireReportReason(t, repHoles, techcardarchive.EntityCutStrip, "cut_strip_key="+keyBroken,
		string(techcardarchive.ReasonArchiveRowInvalid))
	imported, skipped, ok = cutsArchCounter(repHoles)
	require.True(t, ok)
	require.Equal(t, 2, imported)
	require.Equal(t, 2, skipped)
	holes, err := T.GetTechCardById(ctx, holesID)
	require.NoError(t, err)
	require.Len(t, holes.CutStrips, 2)
	for _, op := range holes.Operations {
		require.NotContains(t, op.InputKeys, keyOrphan, "a dropped strip takes its input with it")
	}

	// ── THE STRIPS OUT OF card.json, the way an older server exported: no strips, no error ──
	zipOld := foldArchEditCard(t, archive, func(card map[string]any) {
		delete(card, "cutStrips")
		delete(card, "cutStripNorms")
		for _, op := range foldArchOps(t, card) {
			keep := []any{}
			for _, k := range asAnyList(op["inputKeys"]) {
				if k != cutsArchSeam {
					keep = append(keep, k)
				}
			}
			op["inputKeys"] = keep
		}
	})
	oldID, repOld := rig.importArchive(t, zipOld)
	_, _, ok = cutsArchCounter(repOld)
	require.False(t, ok, "no strips, no counter")
	old, err := T.GetTechCardById(ctx, oldID)
	require.NoError(t, err)
	require.Empty(t, old.CutStrips)
}

// TestTechCardArchiveStriplessCardIsUnchanged: what a card with no strips exports names none of it.
func TestTechCardArchiveStriplessCardIsUnchanged(t *testing.T) {
	requireCutStripIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	rig := foldArchRig(t, ctx)
	fx := rtBuildMaximalCard(t, rig)

	archive, _ := rig.export(t, fx.cardID)
	arch, err := techcardarchive.OpenArchive(bytes.NewReader(archive), int64(len(archive)))
	require.NoError(t, err)
	raw, err := arch.ReadFile(techcardarchive.FileCard)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "cutStrip")

	newID, rep := rig.importArchive(t, archive)
	_, _, ok := cutsArchCounter(rep)
	require.False(t, ok, "no strips, no counter")
	got, err := rig.store.TechCards().GetTechCardById(ctx, newID)
	require.NoError(t, err)
	require.Empty(t, got.CutStrips)
	require.Zero(t, cutsCount(t, ctx, "SELECT COUNT(*) FROM tech_card_cut_strip WHERE tech_card_id = ?", newID))
}
