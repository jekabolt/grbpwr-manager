package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф6.2 — «create colourways from archive», tested for the six ways it can go wrong.
//
//  1. It writes MONEY. The archive carries none by construction, and this action creates PRODUCTS —
//     the one place in the feature where a price would have somewhere to land.
//  2. A second press duplicates the colours, or answers 500 because the UNIQUE refused them. Both
//     make the button unusable: the operator cannot tell whether the first press worked.
//  3. It optimistically locks on a STALE card version. Every recipe write bumps
//     tech_card.lock_version, so a press over two colours races with itself.
//  4. One unusable row costs the whole colourway. The recipe write aborts on an unknown line_key,
//     and an archive whose card lost a BOM line would then create colours with empty recipes.
//  5. It leaves the report saying the colours were never created — next to the colours.
//  6. It reports a pin as MISSING FROM THE CATALOGUE when what is missing is the description of
//     which article was meant. That sends somebody to create an article they already have.
//
// Helpers are prefixed tcac*; the package already owns tcimp*, tcrep*, tcup* and tcz*.
// ─────────────────────────────────────────────────────────────────────────────

const (
	tcacCardID     = 214
	tcacImportID   = "01J8ZZQ9V2R6M1K0"
	tcacObjectKey  = "techcard-imports/01J8ZZQ9V2R6M1K0.zip"
	tcacSourceMat  = int64(8120)
	tcacTargetMat  = int64(4410)
	tcacBomKeyMain = "BOM-MAIN"
	tcacPieceKey   = "PIECE-FRONT"
)

// tcacRig is the whole cast: a Server whose every dependency is a STRICT mock, so «the handler
// asked for nothing else» is proved by the test being green at all.
type tcacRig struct {
	s        *Server
	repo     *mocks.MockRepository
	cards    *mocks.MockTechCards
	products *mocks.MockProducts
	bucket   *mocks.MockFileStore
}

func tcacServer(t *testing.T) *tcacRig {
	t.Helper()
	// The colour dictionary is a PROCESS-WIDE cache and dto.BuildColorwayInsertEntity reads it —
	// a colourway's colour code is a dictionary FK, so without this the create refuses every
	// colour. Restored to empty afterwards: this package has no TestMain seeding it, and a test
	// that leaves a global loaded is a test that makes its neighbours pass for the wrong reason.
	//
	// It is the SAME DictionaryInfo the mock answers with, and that is not tidiness: every
	// successful colourway create ends in afterColorwayLifecycleChange, which re-seeds this very
	// cache from GetDictionaryInfo. A mock answer without colours would therefore WIPE the colour
	// dictionary after the first colour and make the second one fail — which is precisely what it
	// did until this line said so.
	cache.RefreshDictionary(tcacDictionary())
	t.Cleanup(func() { cache.RefreshDictionary(&entity.DictionaryInfo{}) })

	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	products := mocks.NewMockProducts(t)
	hero := mocks.NewMockHero(t)
	dict := mocks.NewMockCache(t)
	bucket := mocks.NewMockFileStore(t)
	re := mocks.NewMockRevalidationService(t)

	repo.EXPECT().TechCards().Return(cards).Maybe()
	repo.EXPECT().Products().Return(products).Maybe()
	repo.EXPECT().Hero().Return(hero).Maybe()
	repo.EXPECT().Cache().Return(dict).Maybe()
	hero.EXPECT().RefreshHero(mock.Anything).Return(nil).Maybe()
	dict.EXPECT().GetDictionaryInfo(mock.Anything).Return(tcacDictionary(), nil).Maybe()
	re.EXPECT().RevalidateAll(mock.Anything, mock.Anything).Return(nil).Maybe()

	return &tcacRig{
		s: &Server{
			repo: repo, bucket: bucket, re: re,
			revalidateSem: make(chan struct{}, 1), revalCtx: context.Background(),
		},
		repo: repo, cards: cards, products: products, bucket: bucket,
	}
}

// tcacDictionary is the TARGET base: sizes s/m (no l), which is what makes the two size codes
// distinguishable — `l` is absent from the dictionary, `m` is present and outside the card's range.
func tcacDictionary() *entity.DictionaryInfo {
	return &entity.DictionaryInfo{
		Sizes: []entity.Size{{Id: 30, Name: "s"}, {Id: 40, Name: "m"}},
		Colors: []entity.Color{
			{ID: 1, Code: "BLK", Name: "black"},
			{ID: 2, Code: "OLV", Name: "olive"},
		},
	}
}

// tcacCard is the imported card: one BOM line, one cut-piece, one size, no colourways yet.
func tcacCard() *entity.TechCard {
	return &entity.TechCard{
		Id:          tcacCardID,
		LockVersion: 7,
		TechCardInsert: entity.TechCardInsert{
			StyleNumber: sql.NullString{String: "GRB-SS26-014", Valid: true},
			SizeIds:     []int{30},
			BomItems:    []entity.TechCardBomItem{{Id: 1, LineKey: tcacBomKeyMain}},
			Pieces:      []entity.TechCardPiece{{Id: 5, LineKey: tcacPieceKey}},
		},
	}
}

// tcacPayloadOf marshals colourway payloads the way the archive carries them — through the format
// package's own structs, so a test cannot pass against a shape the export stopped writing.
func tcacPayloadOf(t *testing.T, in ...techcardarchive.ColorwayPayload) []byte {
	t.Helper()
	raw, err := json.Marshal(in)
	require.NoError(t, err)
	return raw
}

// tcacColourway is the ordinary payload element: one garment-level norm on the card's only BOM
// line, pinned to a catalogue article, plus the piece→cloth mapping.
func tcacColourway(code string) techcardarchive.ColorwayPayload {
	return techcardarchive.ColorwayPayload{
		ColorCode: code,
		BaseSKU:   "GRB-SS26-014-" + code,
		Recipe: []techcardarchive.RecipeLine{{
			BomLineKey:       tcacBomKeyMain,
			Placement:        "outer",
			Color:            "black",
			Consumption:      "1.42",
			SizeConsumptions: map[string]string{"s": "1.38"},
			MaterialRef:      tcacSourceMat,
		}},
		PieceMaterials: []techcardarchive.PieceMaterialLine{{
			PieceLineKey: tcacPieceKey,
			BomLineKey:   tcacBomKeyMain,
		}},
	}
}

// tcacStoredReport is the report the COMMIT left on the card: every colour counted as skipped with
// a colorways_not_applied line, exactly as resolveColorways writes it. Built through the report's
// own package, never hand-written.
func tcacStoredReport(t *testing.T, codes ...string) []byte {
	t.Helper()
	c := techcardarchive.NewCounters()
	c.AddImported(techcardarchive.EntityMedia, 4)
	c.AddSkipped(techcardarchive.EntityColorway, len(codes))
	holes := []techcardarchive.ImportHole{{
		Entity: techcardarchive.EntityMedia, Ref: "media_id=812",
		Reason: techcardarchive.ReasonMediaMissing, Detail: "the archive names a picture it does not carry",
	}, {
		// The resolver's OTHER colourway line, filed against a CUT PIECE rather than a colour
		// (techcard_archive_resolve.go): the piece named the cloth it is cut from per colourway and
		// the import wrote no colourways, so the piece landed without that mapping. It is entity
		// `colorway` and it is NOT the press's news to revise.
		Entity: techcardarchive.EntityColorway, Ref: "piece_line_key=" + tcacPieceKey,
		Status: techcardarchive.StatusSkipped, Reason: techcardarchive.ReasonColorwaysNotApplied,
		Detail: "this piece named the cloth it is cut from PER COLOURWAY",
	}}
	for _, code := range codes {
		holes = append(holes, techcardarchive.ImportHole{
			Entity: techcardarchive.EntityColorway, Ref: "color_code=" + code,
			Status: techcardarchive.StatusSkipped, Reason: techcardarchive.ReasonColorwaysNotApplied,
			Detail: "travelled as reference only",
		})
	}
	raw, err := techcardarchive.MarshalReport(techcardarchive.BuildReport(techcardarchive.ReportInput{
		ImportID: tcacImportID, StyleNumber: "GRB-SS26-014", Stage: "proto",
		Counters: c, Holes: holes,
	}))
	require.NoError(t, err)
	return raw
}

func tcacImportRow(t *testing.T, payload []byte, codes ...string) entity.TechCardArchiveImportRecord {
	t.Helper()
	return entity.TechCardArchiveImportRecord{
		ImportID:         tcacImportID,
		TechCardID:       sql.NullInt32{Int32: tcacCardID, Valid: true},
		ObjectKey:        tcacObjectKey,
		Status:           entity.TechCardImportStatusCommitted,
		ColorwaysPayload: payload,
		Report:           tcacStoredReport(t, codes...),
	}
}

// ────────────────────────────── the archive, still in the bucket ──────────────────────────────

// tcacReaderAt is an in-memory ReaderAtCloser. A mock of the interface would answer ReadAt with
// whatever the test told it to and prove nothing about a zip being read.
type tcacReaderAt struct{ *bytes.Reader }

func (tcacReaderAt) Close() error { return nil }

// tcacArchiveBytes builds a MINIMAL VALID archive carrying one material passport — the only entry
// this action reads out of it. The two mandatory entries and the money policy are what the reader
// refuses an archive without.
func tcacArchiveBytes(t *testing.T, passports []techcardarchive.MaterialPassport) []byte {
	t.Helper()
	cardJSON, err := protojson.Marshal(&pb_common.TechCard{
		Id: tcacCardID, TechCard: &pb_common.TechCardInsert{StyleNumber: "GRB-SS26-014", Name: "coat"},
	})
	require.NoError(t, err)

	files := map[string][]byte{
		techcardarchive.FileManifest: tcimpJSON(t, techcardarchive.Manifest{
			Format:        techcardarchive.FormatName,
			FormatVersion: techcardarchive.FormatVersion,
			MoneyPolicy:   techcardarchive.MoneyPolicyStrippedV1,
			Source:        techcardarchive.Source{Host: "backend.source.example", TechCardID: tcacCardID},
		}),
		techcardarchive.FileCard: cardJSON,
	}
	if passports != nil {
		files[techcardarchive.FileMaterialsIndex] = tcimpJSON(t, passports)
	}
	return tcimpZip(t, files)
}

// tcacArchiveInBucket makes the uploaded archive readable, which is what lets a recipe pin resolve.
func (r *tcacRig) tcacArchiveInBucket(t *testing.T, passports []techcardarchive.MaterialPassport) {
	t.Helper()
	raw := tcacArchiveBytes(t, passports)
	r.bucket.EXPECT().GetImportObjectReaderAt(mock.Anything, tcacObjectKey).
		Return(tcacReaderAt{bytes.NewReader(raw)}, int64(len(raw)), nil).Once()
}

// tcacArchiveGone is the other half of the same story: the retention window closed.
func (r *tcacRig) tcacArchiveGone() {
	r.bucket.EXPECT().GetImportObjectReaderAt(mock.Anything, tcacObjectKey).
		Return(nil, int64(0), errors.New("NoSuchKey")).Once()
}

// tcacCatalogue is the target catalogue the passport is matched against.
func tcacCatalogue() []entity.MaterialWithPrice {
	return []entity.MaterialWithPrice{{Material: entity.Material{
		Id: int(tcacTargetMat),
		MaterialInsert: entity.MaterialInsert{
			Name:     "wool melton 320 g",
			Section:  "fabric",
			Code:     sql.NullString{String: "F-WOOL-320", Valid: true},
			Unit:     sql.NullString{String: "m", Valid: true},
			Supplier: sql.NullString{String: "Lanificio", Valid: true},
		},
	}}}
}

func tcacPassport() []techcardarchive.MaterialPassport {
	return []techcardarchive.MaterialPassport{{
		Ref: tcacSourceMat, Code: "F-WOOL-320", Name: "wool melton 320 g",
		Supplier: "Lanificio", Unit: "m",
	}}
}

// tcacLineFor finds the report line about one colour with one reason. Fails loudly rather than
// returning nil: a missing line is the defect every case here is about.
func tcacLineFor(t *testing.T, rep *pb_admin.TechCardImportReport, ref string, reason techcardarchive.Reason) *pb_admin.TechCardImportReportLine {
	t.Helper()
	for _, l := range rep.GetLines() {
		if l.GetRef() == ref && l.GetReason() == string(reason) {
			return l
		}
	}
	t.Fatalf("the report carries no %q line for %q; it has:\n%s", reason, ref, tcacDumpLines(rep))
	return nil
}

// tcacHasLine is tcacHasReason narrowed to ONE ref, which is the distinction the colourway half of
// a report turns on: the same reason code is filed against a colour and against a cut piece, and a
// press supersedes the first without touching the second.
func tcacHasLine(rep *pb_admin.TechCardImportReport, ref string, reason techcardarchive.Reason) bool {
	for _, l := range rep.GetLines() {
		if l.GetRef() == ref && l.GetReason() == string(reason) {
			return true
		}
	}
	return false
}

func tcacHasReason(rep *pb_admin.TechCardImportReport, reason techcardarchive.Reason) bool {
	for _, l := range rep.GetLines() {
		if l.GetReason() == string(reason) {
			return true
		}
	}
	return false
}

func tcacDumpLines(rep *pb_admin.TechCardImportReport) string {
	var b bytes.Buffer
	for _, l := range rep.GetLines() {
		fmt.Fprintf(&b, "  %s | %s | %s | %s\n", l.GetEntity(), l.GetRef(), l.GetStatus(), l.GetReason())
	}
	return b.String()
}

func (r *tcacRig) apply(t *testing.T) (*pb_admin.ApplyTechCardImportColorwaysResponse, error) {
	t.Helper()
	return r.s.ApplyTechCardImportColorways(t.Context(),
		&pb_admin.ApplyTechCardImportColorwaysRequest{TechCardId: tcacCardID})
}

// ────────────────────── 1. the happy path, end to end ──────────────────────

// The whole shape of the action in one case: a draft is created, its recipe lands with the pin
// resolved through the archive's passport, the piece→cloth mapping is written, and the report stops
// saying the colour was not created.
func TestApplyImportColorwaysCreatesADraftWithItsRecipe(t *testing.T) {
	r := tcacServer(t)
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK")), "BLK"), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	r.tcacArchiveInBucket(t, tcacPassport())

	var createdWith *entity.ColorwayInsert
	r.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ int, prd *entity.ColorwayInsert, _ []int,
			_ []entity.ColorwayTagInsert, _ []entity.ColorwayPriceInsert, _ *entity.ColorwayDevelopmentPatch) {
			createdWith = prd
		}).Return(901, nil).Once()

	var wrote []entity.TechCardColorwayUsage
	r.cards.EXPECT().GetTechCardLockVersion(mock.Anything, tcacCardID).Return(7, nil).Once()
	r.cards.EXPECT().UpdateColorwayRecipe(mock.Anything, 901, 7, mock.Anything).
		Run(func(_ context.Context, _ int, _ int, usages []entity.TechCardColorwayUsage) {
			wrote = usages
		}).Return(8, nil).Once()

	var mapped []entity.TechCardArchivePieceMaterial
	r.cards.EXPECT().ApplyImportedColorwayPieceMaterials(mock.Anything, tcacCardID, 901, mock.Anything).
		Run(func(_ context.Context, _ int, _ int, rows []entity.TechCardArchivePieceMaterial) {
			mapped = rows
		}).Return(nil).Once()

	var stamped []byte
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).
		Run(func(_ context.Context, _ string, report []byte) { stamped = report }).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err)
	require.Equal(t, []int32{901}, resp.GetCreatedColorwayIds())

	require.Equal(t, "BLK", createdWith.ProductBodyInsert.ColorCode)

	require.Len(t, wrote, 1, "the archive's one recipe row must land")
	require.Equal(t, tcacBomKeyMain, wrote[0].BomLineKey, "the row addresses the BOM line by its verbatim key")
	require.True(t, wrote[0].Consumption.Valid)
	require.True(t, decimal.RequireFromString("1.42").Equal(wrote[0].Consumption.Decimal))
	require.Equal(t, int64(tcacTargetMat), wrote[0].MaterialId.Int64,
		"the pin must resolve to THIS base's article through the archive's passport")
	require.True(t, wrote[0].MaterialIdSet, "presence must be explicit: a fresh colourway has no stored pin to preserve")
	require.Len(t, wrote[0].SizeConsumptions, 1)
	require.Equal(t, 30, wrote[0].SizeConsumptions[0].SizeId, "the per-size norm travels by NAME and is remapped")

	require.Equal(t, []entity.TechCardArchivePieceMaterial{
		{PieceLineKey: tcacPieceKey, BomLineKey: tcacBomKeyMain},
	}, mapped)

	// The report: the colour moved out of skipped, and the sentence saying it was never created is
	// gone. A stale line standing next to the created colour is the lie this rewrite exists for.
	require.Equal(t, stamped, mustMarshalReport(t, resp.GetReport()),
		"the answer must be the bytes that were stored, not a second opinion")
	require.False(t, tcacHasLine(resp.GetReport(), "color_code=BLK", techcardarchive.ReasonColorwaysNotApplied),
		"the «colourways were not created» line must not survive the press that created them:\n%s",
		tcacDumpLines(resp.GetReport()))
	// …but the resolver's per-PIECE line is a different sentence about a different thing, and this
	// press has no verdict to put in its place (R6/M-7).
	tcacLineFor(t, resp.GetReport(), "piece_line_key="+tcacPieceKey, techcardarchive.ReasonColorwaysNotApplied)
	cw := tcrepCounter(t, resp.GetReport(), techcardarchive.EntityColorway)
	require.Equal(t, int32(1), cw.GetImported())
	require.Equal(t, int32(0), cw.GetSkipped())
	require.Equal(t, int32(0), cw.GetDegraded())
	// Everything the import said about anything ELSE is untouched: this action revises colourways.
	require.True(t, tcacHasReason(resp.GetReport(), techcardarchive.ReasonMediaMissing),
		"a media hole is not news this action has any standing to revise")
	require.Equal(t, int32(4), tcrepCounter(t, resp.GetReport(), techcardarchive.EntityMedia).GetImported())
}

func mustMarshalReport(t *testing.T, rep *pb_admin.TechCardImportReport) []byte {
	t.Helper()
	raw, err := techcardarchive.MarshalReport(rep)
	require.NoError(t, err)
	return raw
}

// ────────────────────── 2. money ──────────────────────

// The action creates PRODUCTS, which is the one place in this feature a price would have somewhere
// to land. The colourway is created as a bare draft: no cost, no retail prices, no lab-dip block.
func TestApplyImportColorwaysWritesNoMoney(t *testing.T) {
	r := tcacServer(t)
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK")), "BLK"), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	r.tcacArchiveGone()

	var (
		prd    *entity.ColorwayInsert
		prices []entity.ColorwayPriceInsert
		dev    *entity.ColorwayDevelopmentPatch
	)
	r.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ int, p *entity.ColorwayInsert, _ []int,
			_ []entity.ColorwayTagInsert, pr []entity.ColorwayPriceInsert, d *entity.ColorwayDevelopmentPatch) {
			prd, prices, dev = p, pr, d
		}).Return(901, nil).Once()
	r.cards.EXPECT().GetTechCardLockVersion(mock.Anything, tcacCardID).Return(7, nil).Once()

	var wrote []entity.TechCardColorwayUsage
	r.cards.EXPECT().UpdateColorwayRecipe(mock.Anything, 901, 7, mock.Anything).
		Run(func(_ context.Context, _ int, _ int, usages []entity.TechCardColorwayUsage) { wrote = usages }).
		Return(8, nil).Once()
	r.cards.EXPECT().ApplyImportedColorwayPieceMaterials(mock.Anything, tcacCardID, 901, mock.Anything).
		Return(nil).Once()
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

	_, err := r.apply(t)
	require.NoError(t, err)

	require.False(t, prd.CostPrice.Valid, "an archive may never set a colourway's cost_price")
	require.Empty(t, prices, "an archive may never set retail prices")
	require.Nil(t, dev, "an archive carries no lab dips (§5.3) and must not invent one")
	require.NotEmpty(t, wrote)
	// tech_card_colorway_usage has no money column at all, so the assertion that matters is the
	// entity's: nothing on the usage carries a figure other than the norm the archive stated.
	require.False(t, wrote[0].Quantity.Valid, "a measured row states a norm, not a count")
}

// A payload that DOES carry money is an archive of a build that predates the money policy. It is
// refused whole, and — the half that matters — nothing is created first.
func TestApplyImportColorwaysRefusesAPayloadCarryingMoney(t *testing.T) {
	// Hand-built rather than marshalled from ColorwayPayload, and that is the point: the struct has
	// no member for a price, so a belt reading the parsed form would be blind by construction.
	dirty := []byte(`[{"color_code":"BLK","recipe":[{"bom_line_key":"BOM-MAIN","consumption":"1.42","unit_price":{"value":"18.40"}}]}]`)

	r := tcacServer(t)
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, dirty, "BLK"), nil).Once()

	resp, err := r.apply(t)
	require.Nil(t, resp)
	require.Equal(t, codes.FailedPrecondition, tcrepCode(t, err))
	require.Contains(t, err.Error(), "unit_price", "the refusal must name the field that leaked")
	// No CreateColorway / GetTechCardByIdConsistent expectations were registered: the strict mock
	// would have failed on either, which is how «nothing was created» is proved.
}

// ────────────────────── 3. pressing twice ──────────────────────

// A colour the card already carries is reported and skipped. This is the whole of the button's
// idempotency: the operator can press it again without wondering what the first press did.
func TestApplyImportColorwaysSkipsAColourTheCardAlreadyHas(t *testing.T) {
	r := tcacServer(t)
	card := tcacCard()
	card.Colorways = []entity.TechCardColorway{{Id: 777, ColorCode: "BLK"}}

	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK")), "BLK"), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(card, nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	r.tcacArchiveInBucket(t, tcacPassport())
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err)
	require.Empty(t, resp.GetCreatedColorwayIds(), "a second press creates nothing and is still a success")

	line := tcacLineFor(t, resp.GetReport(), "color_code=BLK", techcardarchive.ReasonColorwayExists)
	require.Equal(t, techcardarchive.StatusDegraded, line.GetStatus(),
		"the colour IS on the card — «skipped» would send the operator to create a duplicate")
	require.Contains(t, line.GetDetail(), "777", "the line must name the colourway that is already there")
	require.Equal(t, int32(1), tcrepCounter(t, resp.GetReport(), techcardarchive.EntityColorway).GetDegraded())
	// No CreateColorway expectation: the strict mock proves nothing was written.
}

// The PHANTOM collision: the store's own duplicate check and its INSERT are not the same instant,
// and a second click fits between them. Caught and reported as «exists» — a 500 here would punish
// the operator for double-clicking.
//
// R6/M-6: «the colour is taken» arrives here from TWO different worlds and they must not share one
// sentence. The store's uniqueness pre-check counts EVERY product row of the style, archived
// included (colorway_write.go); the card read that fills colorwayIDByCode drops
// lifecycle_status = 4 (materials.go). So a colour whose only colourway is ARCHIVED reports as
// taken while the colourways tab shows nothing — and the operator was told «this card already has
// a colourway of this colour», with no id, no way to open it and nothing to do. The two are told
// apart by re-reading the card.
func TestApplyImportColorwaysTellsARaceApartFromAnArchivedColourway(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		recheck func() *entity.TechCard
		assert  func(t *testing.T, rep *pb_admin.TechCardImportReport)
	}{
		{
			name: "the store saw the duplicate and the colour is now on the card",
			err:  entity.ErrColorwayColorExists,
			recheck: func() *entity.TechCard {
				card := tcacCard()
				card.Colorways = []entity.TechCardColorway{{Id: 778, ColorCode: "BLK"}}
				return card
			},
			assert: func(t *testing.T, rep *pb_admin.TechCardImportReport) {
				line := tcacLineFor(t, rep, "color_code=BLK", techcardarchive.ReasonColorwayExists)
				require.Equal(t, techcardarchive.StatusDegraded, line.GetStatus())
				require.Contains(t, line.GetDetail(), "778",
					"a race that resolved must name the colourway the winner created")
				require.Equal(t, int32(1), tcrepCounter(t, rep, techcardarchive.EntityColorway).GetDegraded())
			},
		},
		{
			name: "the UNIQUE saw it first and the colour is now on the card",
			err:  fmt.Errorf("insert colourway: Error 1062 (23000): Duplicate entry 'x' for key 'uniq_product_style_color'"),
			recheck: func() *entity.TechCard {
				card := tcacCard()
				card.Colorways = []entity.TechCardColorway{{Id: 778, ColorCode: "BLK"}}
				return card
			},
			assert: func(t *testing.T, rep *pb_admin.TechCardImportReport) {
				tcacLineFor(t, rep, "color_code=BLK", techcardarchive.ReasonColorwayExists)
			},
		},
		{
			name:    "the colour is taken and nothing on the card shows it: an ARCHIVED colourway",
			err:     entity.ErrColorwayColorExists,
			recheck: tcacCard, // still no colourway of that colour: the occupant is archived
			assert: func(t *testing.T, rep *pb_admin.TechCardImportReport) {
				line := tcacLineFor(t, rep, "color_code=BLK", techcardarchive.ReasonColorwayNotCreated)
				require.Equal(t, techcardarchive.StatusSkipped, line.GetStatus(),
					"nothing landed and the colourways tab shows nothing — «degraded» would send the "+
						"operator looking for a row that is not on their screen")
				require.Contains(t, line.GetDetail(), "ARCHIVED",
					"the operator has to be told WHERE the colour went and what to do:\n%s", line.GetDetail())
				require.False(t, tcacHasReason(rep, techcardarchive.ReasonColorwayExists),
					"«nothing to do unless you want the archive's recipe» is the wrong afternoon here")
				require.Equal(t, int32(1), tcrepCounter(t, rep, techcardarchive.EntityColorway).GetSkipped())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tcacServer(t)
			r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
				Return(tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK")), "BLK"), nil).Once()
			r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
			r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
			r.tcacArchiveGone()
			r.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
				mock.Anything, mock.Anything, mock.Anything).Return(0, tc.err).Once()
			// The re-read that tells the two apart. It is ONLY on this path — every other test in
			// this file reads the card exactly once, which is what proves it.
			r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tc.recheck(), nil).Once()
			r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

			resp, err := r.apply(t)
			require.NoError(t, err, "a colour that turned out to be taken is not a server fault")
			require.Empty(t, resp.GetCreatedColorwayIds())
			tc.assert(t, resp.GetReport())
		})
	}
}

// R6/M-5: a write the DATABASE refused for CONTENTION is not a write it refused for content.
//
// store.Tx is SERIALIZABLE and already retries 1213/1205 five times with backoff, re-running the
// whole closure — on which CreateColorway's own uniqueness pre-check sees the winner's row and
// answers ErrColorwayColorExists, which is why an ordinary simultaneous double press lands on
// «exists». What reaches here is the rarer case where five retries were not enough, and it used to
// be filed under a sentence about the colour dictionary: «add the colour and press again», about a
// colour that is present and correct.
func TestApplyImportColorwaysDoesNotBlameTheDictionaryForADeadlock(t *testing.T) {
	r := tcacServer(t)
	r.repo.EXPECT().IsErrorRepeat(mock.Anything).Return(true).Once()
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK")), "BLK"), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	r.tcacArchiveGone()
	r.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything).
		Return(0, fmt.Errorf("transaction failed after 5 retries: Error 1213 (40001): Deadlock found when trying to get lock")).Once()
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err, "contention is not a server fault the operator can do anything about")
	line := tcacLineFor(t, resp.GetReport(), "color_code=BLK", techcardarchive.ReasonColorwayNotCreated)
	require.Contains(t, line.GetDetail(), "press the button again")
	require.Contains(t, line.GetDetail(), "nothing is wrong with this colour",
		"the default sentence for this code sends the operator to the colour dictionary:\n%s", line.GetDetail())
}

// ────────────────────── 4. the optimistic token ──────────────────────

// EVERY recipe write bumps tech_card.lock_version, so a press over two colours races with ITSELF.
// The token is therefore re-read before each write; a version captured once at the top would make
// the second colour of every press fail.
func TestApplyImportColorwaysReadsAFreshLockVersionPerColourway(t *testing.T) {
	r := tcacServer(t)
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK"), tcacColourway("OLV")), "BLK", "OLV"), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	r.tcacArchiveInBucket(t, tcacPassport())

	ids := []int{901, 902}
	created := 0
	r.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, int, *entity.ColorwayInsert, []int, []entity.ColorwayTagInsert,
			[]entity.ColorwayPriceInsert, *entity.ColorwayDevelopmentPatch) (int, error) {
			id := ids[created]
			created++
			return id, nil
		}).Times(2)

	// The card's version as the DATABASE would answer it: 7 before the first recipe write, 8 after.
	version := 7
	r.cards.EXPECT().GetTechCardLockVersion(mock.Anything, tcacCardID).
		RunAndReturn(func(context.Context, int) (int, error) { return version, nil }).Times(2)

	var sawVersions []int
	r.cards.EXPECT().UpdateColorwayRecipe(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _ int, expected int, _ []entity.TechCardColorwayUsage) (int, error) {
			sawVersions = append(sawVersions, expected)
			version++ // the store bumps the shared lock at the end of every recipe write
			return version, nil
		}).Times(2)

	r.cards.EXPECT().ApplyImportedColorwayPieceMaterials(mock.Anything, tcacCardID, mock.Anything, mock.Anything).
		Return(nil).Times(2)
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err)
	require.Equal(t, []int32{901, 902}, resp.GetCreatedColorwayIds())
	require.Equal(t, []int{7, 8}, sawVersions,
		"the second colourway must be written against the version the FIRST one left behind")
	require.Equal(t, int32(2), tcrepCounter(t, resp.GetReport(), techcardarchive.EntityColorway).GetImported())
}

// ────────────────────── 5. rows this card cannot hold ──────────────────────

// UpdateColorwayRecipe aborts the WHOLE recipe on an unknown line_key — correct for a panel save,
// ruinous for a restore. Each unusable row is filtered out and reported instead, and the two size
// failures keep their own codes because they send the operator to two different places.
func TestApplyImportColorwaysDropsRowsThisCardCannotHold(t *testing.T) {
	payload := techcardarchive.ColorwayPayload{
		ColorCode: "BLK",
		Recipe: []techcardarchive.RecipeLine{
			{BomLineKey: "BOM-GONE", Consumption: "1.00"},
			{BomLineKey: tcacBomKeyMain, PieceLineKey: "PIECE-GONE", Consumption: "0.40"},
			{BomLineKey: tcacBomKeyMain, Consumption: "1.42", SizeConsumptions: map[string]string{
				"s": "1.38", // in the dictionary AND in the card's range
				"m": "1.44", // in the dictionary, outside the card's range
				"l": "1.50", // not in this base's dictionary at all
			}},
		},
		PieceMaterials: []techcardarchive.PieceMaterialLine{
			{PieceLineKey: "PIECE-GONE", BomLineKey: tcacBomKeyMain},
			{PieceLineKey: tcacPieceKey, BomLineKey: tcacBomKeyMain},
		},
	}

	r := tcacServer(t)
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, tcacPayloadOf(t, payload), "BLK"), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	// NO bucket expectation, and that is an assertion (R6/M-8): not one row of this payload pins an
	// article, so there is no passport to look up — and fetching the archive means copying the whole
	// uploaded ZIP out of the bucket into a temporary file. The strict mock is what proves it is
	// not fetched.
	r.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything).Return(901, nil).Once()
	r.cards.EXPECT().GetTechCardLockVersion(mock.Anything, tcacCardID).Return(7, nil).Once()

	var wrote []entity.TechCardColorwayUsage
	r.cards.EXPECT().UpdateColorwayRecipe(mock.Anything, 901, 7, mock.Anything).
		Run(func(_ context.Context, _ int, _ int, usages []entity.TechCardColorwayUsage) { wrote = usages }).
		Return(8, nil).Once()

	var mapped []entity.TechCardArchivePieceMaterial
	r.cards.EXPECT().ApplyImportedColorwayPieceMaterials(mock.Anything, tcacCardID, 901, mock.Anything).
		Run(func(_ context.Context, _ int, _ int, rows []entity.TechCardArchivePieceMaterial) { mapped = rows }).
		Return(nil).Once()
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err, "one unusable row must not cost the colourway")
	require.Equal(t, []int32{901}, resp.GetCreatedColorwayIds())

	require.Len(t, wrote, 1, "only the row this card can hold is written")
	require.Equal(t, tcacBomKeyMain, wrote[0].BomLineKey)
	require.Len(t, wrote[0].SizeConsumptions, 1, "only the in-range, in-dictionary size survives")
	require.Equal(t, 30, wrote[0].SizeConsumptions[0].SizeId)

	require.Len(t, mapped, 1, "the piece→cloth row naming a missing piece is dropped, not written")
	require.Equal(t, tcacPieceKey, mapped[0].PieceLineKey)

	rep := resp.GetReport()
	require.True(t, tcacHasReason(rep, techcardarchive.ReasonArchiveRowInvalid),
		"a row naming a line_key this card has not must be reported:\n%s", tcacDumpLines(rep))
	tcacLineFor(t, rep, "color_code=BLK bom_line_key="+tcacBomKeyMain, techcardarchive.ReasonSizeNotInCardRange)
	tcacLineFor(t, rep, "color_code=BLK bom_line_key="+tcacBomKeyMain, techcardarchive.ReasonSizeUnknown)
	cw := tcrepCounter(t, rep, techcardarchive.EntityColorway)
	require.Equal(t, int32(1), cw.GetDegraded(), "the colour landed, thinner")
	require.Equal(t, int32(0), cw.GetImported())
	require.False(t, tcacHasReason(rep, techcardarchive.ReasonColorwayPinLost),
		"skipping the archive fetch must not invent a lost pin: no row here pins anything")
}

// ────────────────────── 6. the pin whose description is gone ──────────────────────

// A pin travels as the SOURCE's material_id; what identifies it is the archive's material passport,
// and that does not outlive the uploaded file. Once the object has aged out the pin cannot be
// re-resolved — and the report must NOT say material_not_found, whose action text sends the
// operator to create an article this catalogue may hold perfectly well.
func TestApplyImportColorwaysReportsALostPinRatherThanAMissingArticle(t *testing.T) {
	r := tcacServer(t)
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK")), "BLK"), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	r.tcacArchiveGone()
	r.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything).Return(901, nil).Once()
	r.cards.EXPECT().GetTechCardLockVersion(mock.Anything, tcacCardID).Return(7, nil).Once()

	var wrote []entity.TechCardColorwayUsage
	r.cards.EXPECT().UpdateColorwayRecipe(mock.Anything, 901, 7, mock.Anything).
		Run(func(_ context.Context, _ int, _ int, usages []entity.TechCardColorwayUsage) { wrote = usages }).
		Return(8, nil).Once()
	r.cards.EXPECT().ApplyImportedColorwayPieceMaterials(mock.Anything, tcacCardID, 901, mock.Anything).
		Return(nil).Once()
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err)
	require.Len(t, wrote, 1)
	require.False(t, wrote[0].MaterialId.Valid, "an unresolvable pin is left EMPTY, never guessed")
	require.True(t, wrote[0].Consumption.Valid, "the norm and the placement still land")

	line := tcacLineFor(t, resp.GetReport(), "color_code=BLK bom_line_key="+tcacBomKeyMain,
		techcardarchive.ReasonColorwayPinLost)
	require.Equal(t, techcardarchive.StatusDegraded, line.GetStatus())
	require.False(t, tcacHasReason(resp.GetReport(), techcardarchive.ReasonMaterialNotFound),
		"«this catalogue has no such article» is a different sentence and a different afternoon")
}

// ────────────────────── 7. the refusals before anything is written ──────────────────────

func TestApplyImportColorwaysRefusesWhatItCannotBuildFrom(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  func(t *testing.T) entity.TechCardArchiveImportRecord
		code codes.Code
	}{
		{
			name: "an archive that carried no colourways",
			row: func(t *testing.T) entity.TechCardArchiveImportRecord {
				return tcacImportRow(t, nil)
			},
			code: codes.FailedPrecondition,
		},
		{
			name: "an archive whose colourway list is empty",
			row: func(t *testing.T) entity.TechCardArchiveImportRecord {
				return tcacImportRow(t, []byte(`[]`))
			},
			code: codes.FailedPrecondition,
		},
		{
			name: "an import that was never committed",
			row: func(t *testing.T) entity.TechCardArchiveImportRecord {
				rec := tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK")), "BLK")
				rec.Status = entity.TechCardImportStatusUploaded
				return rec
			},
			code: codes.FailedPrecondition,
		},
		{
			name: "a row that names a card and carries no report",
			row: func(t *testing.T) entity.TechCardArchiveImportRecord {
				rec := tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK")), "BLK")
				rec.Report = nil
				return rec
			},
			code: codes.NotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tcacServer(t)
			r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).Return(tc.row(t), nil).Once()

			resp, err := r.apply(t)
			require.Nil(t, resp)
			require.Equal(t, tc.code, tcrepCode(t, err))
			// No further expectations: the strict mock proves nothing was created.
		})
	}
}

// A card nobody imported reaches this call the same way it reaches the report reader, and gets the
// same sentence. Never Internal: the client asks on a flag.
func TestApplyImportColorwaysSaysNotFoundForACardNobodyImported(t *testing.T) {
	r := tcacServer(t)
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(entity.TechCardArchiveImportRecord{}, fmt.Errorf("struct scan: %w", sql.ErrNoRows)).Once()

	resp, err := r.apply(t)
	require.Nil(t, resp)
	require.Equal(t, codes.NotFound, tcrepCode(t, err))
}

// ────────────────────── 8. R6: the store refusing the LAST write of a colour ──────────────────────

// R6/MAJOR-2. The piece→cloth mapping is the last thing written for a colour, and its refusal used
// to be returned as an error — which aborted the whole RPC. That contradicted the argument written
// forty lines above it, in recipe(), about exactly the same kind of write onto exactly the same
// already-created colourway.
//
// What the abort cost is not one line of prose. The colourway is created and STAYS created; the
// report is never stamped (the stamp is downstream of the loop), so the card goes on claiming
// `colorways_not_applied` next to a colour that is on it; and on the second press that colour reads
// as standing, so the mapping is never attempted by anything, ever. A loss nothing re-attempts and
// nothing records is the one outcome the owner ruled out.
func TestApplyImportColorwaysReportsARefusedPieceMappingInsteadOfAborting(t *testing.T) {
	r := tcacServer(t)
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK"), tcacColourway("OLV")), "BLK", "OLV"), nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	r.tcacArchiveInBucket(t, tcacPassport())

	ids := []int{901, 902}
	created := 0
	r.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, int, *entity.ColorwayInsert, []int, []entity.ColorwayTagInsert,
			[]entity.ColorwayPriceInsert, *entity.ColorwayDevelopmentPatch) (int, error) {
			id := ids[created]
			created++
			return id, nil
		}).Times(2)
	r.cards.EXPECT().GetTechCardLockVersion(mock.Anything, tcacCardID).Return(7, nil).Times(2)
	r.cards.EXPECT().UpdateColorwayRecipe(mock.Anything, mock.Anything, 7, mock.Anything).Return(8, nil).Times(2)

	// The FIRST colour's mapping is refused; the second one's lands. A fatal refusal would never
	// have reached the second colour at all.
	r.cards.EXPECT().ApplyImportedColorwayPieceMaterials(mock.Anything, tcacCardID, 901, mock.Anything).
		Return(fmt.Errorf("clear colourway 901 piece materials on card 214: Error 1213 (40001): Deadlock found")).Once()
	r.cards.EXPECT().ApplyImportedColorwayPieceMaterials(mock.Anything, tcacCardID, 902, mock.Anything).
		Return(nil).Once()

	var stamped []byte
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).
		Run(func(_ context.Context, _ string, report []byte) { stamped = report }).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err, "one refused mapping must cost that colour's mapping, not the whole press")
	require.Equal(t, []int32{901, 902}, resp.GetCreatedColorwayIds(),
		"the second colour must still be attempted")
	require.NotEmpty(t, stamped, "the report MUST be rewritten: the drafts are on the card either way")

	rep := resp.GetReport()
	line := tcacLineFor(t, rep, "color_code=BLK", techcardarchive.ReasonArchiveRowInvalid)
	require.Equal(t, techcardarchive.StatusDegraded, line.GetStatus())
	require.Contains(t, line.GetDetail(), "piece→cloth")
	require.Contains(t, line.GetDetail(), "will NOT re-attempt",
		"the operator must be told the button will not fix this by itself:\n%s", line.GetDetail())
	require.False(t, tcacHasLine(rep, "color_code=BLK", techcardarchive.ReasonColorwaysNotApplied),
		"the stale «not created» lines must go: the colours ARE on the card:\n%s", tcacDumpLines(rep))
	require.False(t, tcacHasLine(rep, "color_code=OLV", techcardarchive.ReasonColorwaysNotApplied),
		"…for both of them:\n%s", tcacDumpLines(rep))

	cw := tcrepCounter(t, rep, techcardarchive.EntityColorway)
	require.Equal(t, int32(1), cw.GetDegraded(), "the colour whose mapping was refused landed thinner")
	require.Equal(t, int32(1), cw.GetImported())
}

// ────────────────────── 9. R6: a colour the base refuses to create ──────────────────────

// R6/MAJOR-3. A colour code that is not in THIS base's colour dictionary is the likeliest thing to
// happen on a real import — color_code is a dictionary FK and the archive's codes are the source's
// — and it had no guard at all: deleting the report line entirely left every test in the package
// green.
//
// Two ways in, because they enter the switch from opposite sides: the CONVERTER refuses the colour
// before the store is ever asked (errColorwayInvalid, the real path), and the STORE refuses it for
// something else. Both must be one report line and a successful RPC.
func TestApplyImportColorwaysReportsAColourThisBaseWillNotCreate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  string
		store func(r *tcacRig)
	}{
		{
			name: "the colour is not in this base's colour dictionary",
			code: "ZZZ",
			// No CreateColorway expectation: dto.BuildColorwayInsertEntity resolves color_code
			// against the dictionary and refuses before the store is reached. The strict mock
			// proves nothing was written.
			store: func(r *tcacRig) {},
		},
		{
			name: "the store refuses it for something else",
			code: "OLV",
			store: func(r *tcacRig) {
				r.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
					mock.Anything, mock.Anything, mock.Anything).
					Return(0, fmt.Errorf("insert colourway: Error 1452 (23000): foreign key constraint fails on country_of_origin")).Once()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tcacServer(t)
			r.repo.EXPECT().IsErrorRepeat(mock.Anything).Return(false).Once()
			r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
				Return(tcacImportRow(t, tcacPayloadOf(t, tcacColourway(tc.code)), tc.code), nil).Once()
			r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
			r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
			r.tcacArchiveInBucket(t, tcacPassport())
			tc.store(r)
			r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

			resp, err := r.apply(t)
			require.NoError(t, err, "one colour this base cannot hold must not refuse the whole press")
			require.Empty(t, resp.GetCreatedColorwayIds(), "nothing was created")

			rep := resp.GetReport()
			line := tcacLineFor(t, rep, "color_code="+tc.code, techcardarchive.ReasonColorwayNotCreated)
			require.Equal(t, techcardarchive.StatusSkipped, line.GetStatus(),
				"nothing landed, so «degraded» would send the operator looking for a colourway that is not there")
			require.NotEmpty(t, line.GetAction(), "a closed reason code carries the instruction; a line without one is noise")

			cw := tcrepCounter(t, rep, techcardarchive.EntityColorway)
			require.Equal(t, int32(1), cw.GetSkipped())
			require.Equal(t, int32(0), cw.GetImported())
			require.Equal(t, int32(0), cw.GetDegraded())
			require.False(t, tcacHasLine(rep, "color_code="+tc.code, techcardarchive.ReasonColorwaysNotApplied),
				"the commit's line is superseded even when the verdict is a refusal — the press DID look at "+
					"this colour, and two verdicts about one colour is worse than a bad one:\n%s", tcacDumpLines(rep))
		})
	}
}

// ────────────────────── 10. R6: pressing twice is idempotent IN THE REPORT ──────────────────────

// R6/M-4 and R6/M-7 together, and they are one property: a press that changes nothing must leave a
// report that says nothing changed.
//
// It did not. ApplyColorways replaced the colourway half WHOLE, and a colour already standing
// counted as degraded — so a first press reading «imported 2, clean» became «imported 0, degraded
// 2» on an accidental second click, permanently, on a card's own provenance. And the resolver's
// per-PIECE line (`piece_line_key=… / colorways_not_applied`, the record that the piece→cloth
// mapping never arrived) was erased by a press that replaced it with nothing.
//
// The assertion is the strongest one available: the SECOND press stores the SAME BYTES as the first.
func TestApplyImportColorwaysPressedTwiceStoresTheSameReport(t *testing.T) {
	payload := tcacPayloadOf(t, tcacColourway("BLK"), tcacColourway("OLV"))

	// ── press one: both colours are created ────────────────────────────────────────
	first := tcacServer(t)
	first.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
		Return(tcacImportRow(t, payload, "BLK", "OLV"), nil).Once()
	first.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
	first.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	first.tcacArchiveInBucket(t, tcacPassport())
	ids := []int{901, 902}
	created := 0
	first.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, int, *entity.ColorwayInsert, []int, []entity.ColorwayTagInsert,
			[]entity.ColorwayPriceInsert, *entity.ColorwayDevelopmentPatch) (int, error) {
			id := ids[created]
			created++
			return id, nil
		}).Times(2)
	first.cards.EXPECT().GetTechCardLockVersion(mock.Anything, tcacCardID).Return(7, nil).Times(2)
	first.cards.EXPECT().UpdateColorwayRecipe(mock.Anything, mock.Anything, 7, mock.Anything).Return(8, nil).Times(2)
	first.cards.EXPECT().ApplyImportedColorwayPieceMaterials(mock.Anything, tcacCardID, mock.Anything, mock.Anything).
		Return(nil).Times(2)
	var afterFirst []byte
	first.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).
		Run(func(_ context.Context, _ string, report []byte) { afterFirst = report }).Return(nil).Once()

	resp1, err := first.apply(t)
	require.NoError(t, err)
	require.Equal(t, []int32{901, 902}, resp1.GetCreatedColorwayIds())
	cw1 := tcrepCounter(t, resp1.GetReport(), techcardarchive.EntityColorway)
	require.Equal(t, int32(2), cw1.GetImported())
	require.Equal(t, int32(0), cw1.GetDegraded())
	// M-7: the piece's own line is about a CUT PIECE, not about a colour. The mapping question it
	// records is still open on that piece, and this press has no verdict to put in its place.
	tcacLineFor(t, resp1.GetReport(), "piece_line_key="+tcacPieceKey, techcardarchive.ReasonColorwaysNotApplied)

	// ── press two: the card now carries both colours, and nothing is touched ────────
	second := tcacServer(t)
	card := tcacCard()
	card.Colorways = []entity.TechCardColorway{{Id: 901, ColorCode: "BLK"}, {Id: 902, ColorCode: "OLV"}}
	row := tcacImportRow(t, payload, "BLK", "OLV")
	row.Report = afterFirst // the report the FIRST press stored is what the second one reads
	second.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).Return(row, nil).Once()
	second.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(card, nil).Once()
	second.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	second.tcacArchiveInBucket(t, tcacPassport())
	var afterSecond []byte
	second.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).
		Run(func(_ context.Context, _ string, report []byte) { afterSecond = report }).Return(nil).Once()
	// No CreateColorway / UpdateColorwayRecipe / ApplyImportedColorwayPieceMaterials expectations:
	// the strict mock is what proves the second press writes nothing to the card.

	resp2, err := second.apply(t)
	require.NoError(t, err)
	require.Empty(t, resp2.GetCreatedColorwayIds())

	require.JSONEq(t, string(afterFirst), string(afterSecond),
		"a press that changed nothing must leave the report it found:\n%s", tcacDumpLines(resp2.GetReport()))
	cw2 := tcrepCounter(t, resp2.GetReport(), techcardarchive.EntityColorway)
	require.Equal(t, int32(2), cw2.GetImported(),
		"a colour a PREVIOUS press created is still imported; counting it degraded turns an accidental "+
			"double click into a permanent claim of degradation")
	require.Equal(t, int32(0), cw2.GetDegraded())
	tcacLineFor(t, resp2.GetReport(), "piece_line_key="+tcacPieceKey, techcardarchive.ReasonColorwaysNotApplied)
}

// The other half of M-4: a colour the card carried BEFORE this feature ever ran is a different
// story, and must keep its colorway_exists line. The stored report still carries the commit's
// `colorways_not_applied` for it, and that is exactly what tells the two apart.
func TestApplyImportColorwaysCarriesForwardWhatAnEarlierPressReported(t *testing.T) {
	payload := tcacPayloadOf(t, tcacColourway("BLK"))

	// A stored report as an earlier press left it: the colour was created, its mapping was refused,
	// and the commit's «not applied» line is gone.
	stored, err := techcardarchive.ParseReport(tcacStoredReport(t, "BLK"))
	require.NoError(t, err)
	earlier, err := stored.ApplyColorways([]techcardarchive.ImportHole{{
		Entity: techcardarchive.EntityColorway, Ref: "color_code=BLK",
		Status: techcardarchive.StatusDegraded, Reason: techcardarchive.ReasonArchiveRowInvalid,
		Detail: "the colourway was created and its piece→cloth mapping was refused",
	}}, techcardarchive.EntityTally{Degraded: 1}, func(ref string) bool { return ref == "color_code=BLK" })
	require.NoError(t, err)

	r := tcacServer(t)
	card := tcacCard()
	card.Colorways = []entity.TechCardColorway{{Id: 901, ColorCode: "BLK"}}
	row := tcacImportRow(t, payload, "BLK")
	row.Report = earlier
	r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).Return(row, nil).Once()
	r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(card, nil).Once()
	r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
	r.tcacArchiveInBucket(t, tcacPassport())
	r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

	resp, err := r.apply(t)
	require.NoError(t, err)

	rep := resp.GetReport()
	line := tcacLineFor(t, rep, "color_code=BLK", techcardarchive.ReasonArchiveRowInvalid)
	require.Contains(t, line.GetDetail(), "piece→cloth",
		"the earlier press's record of what it could not write is the ONLY record there is: nothing "+
			"re-attempts a standing colourway's mapping")
	require.False(t, tcacHasReason(rep, techcardarchive.ReasonColorwayExists),
		"replacing that record with «this card already has a colourway of this colour» is the silent "+
			"loss the owner ruled out:\n%s", tcacDumpLines(rep))
	require.Equal(t, int32(1), tcrepCounter(t, rep, techcardarchive.EntityColorway).GetDegraded())
}

// ────────────────────── 11. R6/N-10: the recipe's own retry ──────────────────────

// tcacRecipeAttempts exists because every recipe write bumps the card's SHARED lock_version, so a
// press over several colours races with itself and with anybody saving the card. Nothing exercised
// it: no test made UpdateColorwayRecipe answer with a conflict.
func TestApplyImportColorwaysRetriesTheRecipeOnALockConflict(t *testing.T) {
	for _, tc := range []struct {
		name      string
		conflicts int
		lands     bool
	}{
		{name: "a conflict costs a re-read, not the recipe", conflicts: 1, lands: true},
		{name: "a recipe that never lands is reported, and the colourway still stands", conflicts: tcacRecipeAttempts, lands: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tcacServer(t)
			r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
				Return(tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK")), "BLK"), nil).Once()
			r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
			r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
			r.tcacArchiveInBucket(t, tcacPassport())
			r.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
				mock.Anything, mock.Anything, mock.Anything).Return(901, nil).Once()

			// The version the DATABASE would answer with moves under our feet on every conflict; the
			// point of the retry is that the next attempt is made against the NEW one.
			version := 7
			reads := 0
			r.cards.EXPECT().GetTechCardLockVersion(mock.Anything, tcacCardID).
				RunAndReturn(func(context.Context, int) (int, error) { reads++; return version, nil })

			left := tc.conflicts
			var sawVersions []int
			r.cards.EXPECT().UpdateColorwayRecipe(mock.Anything, 901, mock.Anything, mock.Anything).
				RunAndReturn(func(_ context.Context, _ int, expected int, _ []entity.TechCardColorwayUsage) (int, error) {
					sawVersions = append(sawVersions, expected)
					if left > 0 {
						left--
						version++ // somebody else's save landed
						return 0, entity.ErrTechCardConflict
					}
					return version + 1, nil
				})
			// Written whether or not the recipe landed: the colourway exists and its cloths are a
			// separate write. A recipe conflict is not a reason to leave the pieces unassigned too.
			r.cards.EXPECT().ApplyImportedColorwayPieceMaterials(mock.Anything, tcacCardID, 901, mock.Anything).
				Return(nil).Once()
			r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

			resp, err := r.apply(t)
			require.NoError(t, err)
			require.Equal(t, []int32{901}, resp.GetCreatedColorwayIds(), "the colourway stands either way")

			if tc.lands {
				require.Equal(t, []int{7, 8}, sawVersions,
					"the retry must be made against a FRESHLY READ token, not the stale one it just lost on")
				require.Equal(t, 2, reads, "one read per attempt")
				require.Equal(t, int32(1), tcrepCounter(t, resp.GetReport(), techcardarchive.EntityColorway).GetImported())
				return
			}
			require.Len(t, sawVersions, tcacRecipeAttempts, "the press gives up after tcacRecipeAttempts")
			line := tcacLineFor(t, resp.GetReport(), "color_code=BLK", techcardarchive.ReasonArchiveRowInvalid)
			require.Contains(t, line.GetDetail(), "recipe was refused",
				"a colourway created with no recipe must SAY so; it is otherwise a draft nobody can explain")
			require.Equal(t, int32(1), tcrepCounter(t, resp.GetReport(), techcardarchive.EntityColorway).GetDegraded())
		})
	}
}

// ────────────────── 12. R7: a recipe row that names NEITHER line_key ──────────────────

// R7. tcacRowRef names a report line after the recipe row it is about — «color_code=BLK
// bom_line_key=BOM-MAIN» — by appending whichever keys the row states. A row that states NEITHER
// was therefore named by the bare COLOUR ref, and that collision was not cosmetic.
//
// SUCH ROWS EXIST BY CONSTRUCTION, so this is not a hypothetical shape.
// tech_card_colorway_usage.bom_item_index has been NULLable since 0079_tech_card_overhaul.sql, and
// 0159_bom_stable_lines.sql backfilled bom_item_id / piece_id only `WHERE bom_item_index IS NOT
// NULL` — so a legacy usage row with both references empty survives every migration. The export
// reads both keys out of a map keyed by id (techcard_archive_sidecars.go) and writes "" on a miss,
// so the archive carries such a row keyless and this action reads it back keyless.
//
// What the collision cost, and it cost it twice:
//
//   - THE BUTTON. The client offers «create colourways from archive» for the colours whose report
//     line stands at the bare colour ref, so a ROW-level loss filed there advertised the button
//     forever for a colour that is already on the card.
//   - THE RECORD. priorVerdict reads a SKIPPED line at the EXACT colour ref as «the previous press
//     did not create this colour». The second press therefore treats the colour as never pressed,
//     goes to exists(), and supersedes the ref — ERASING the first press's record of the loss and
//     putting colorway_exists in its place. Nothing ever re-attempts that row, so that line was
//     the only record the loss had. A silent loss is the one outcome this feature may not produce.
//
// Both cases below file a SKIPPED row-level line, which is the status priorVerdict turns on.
func TestApplyImportColorwaysNamesARecipeRowThatNamesNoLineKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		row    techcardarchive.RecipeLine
		reason techcardarchive.Reason
	}{
		{
			// Reachable end to end: per-size norms travel by size NAME, and «l» is not in this
			// base's size dictionary.
			name:   "its per-size norm names a size this base has never heard of",
			row:    techcardarchive.RecipeLine{Consumption: "1.42", SizeConsumptions: map[string]string{"l": "1.50"}},
			reason: techcardarchive.ReasonSizeUnknown,
		},
		{
			name:   "its norm is not a number, so the row is dropped whole",
			row:    techcardarchive.RecipeLine{Consumption: "n/a"},
			reason: techcardarchive.ReasonArchiveRowInvalid,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Row 0 names neither key; row 1 is an ordinary row that lands, so the colour is
			// created either way and the loss is genuinely a ROW-level one.
			payload := tcacPayloadOf(t, techcardarchive.ColorwayPayload{
				ColorCode: "BLK",
				Recipe: []techcardarchive.RecipeLine{
					tc.row,
					{BomLineKey: tcacBomKeyMain, Consumption: "1.42"},
				},
				PieceMaterials: []techcardarchive.PieceMaterialLine{
					{PieceLineKey: tcacPieceKey, BomLineKey: tcacBomKeyMain},
				},
			})
			const rowRef = "color_code=BLK recipe_row=0"

			// ── press one: the colour is created and the keyless row's loss is reported ──
			first := tcacServer(t)
			first.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
				Return(tcacImportRow(t, payload, "BLK"), nil).Once()
			first.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(tcacCard(), nil).Once()
			first.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
			// No bucket expectation: nothing in this payload pins an article, so the uploaded ZIP
			// is not fetched. The strict mock is what proves it.
			first.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
				mock.Anything, mock.Anything, mock.Anything).Return(901, nil).Once()
			first.cards.EXPECT().GetTechCardLockVersion(mock.Anything, tcacCardID).Return(7, nil).Once()
			first.cards.EXPECT().UpdateColorwayRecipe(mock.Anything, 901, 7, mock.Anything).Return(8, nil).Once()
			first.cards.EXPECT().ApplyImportedColorwayPieceMaterials(mock.Anything, tcacCardID, 901, mock.Anything).
				Return(nil).Once()
			var afterFirst []byte
			first.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).
				Run(func(_ context.Context, _ string, report []byte) { afterFirst = report }).Return(nil).Once()

			resp1, err := first.apply(t)
			require.NoError(t, err)
			require.Equal(t, []int32{901}, resp1.GetCreatedColorwayIds(),
				"the colour IS on the card: a keyless row is a row-level loss, not a refusal of the colour")
			require.NotEmpty(t, afterFirst, "the report must be stamped: the draft is on the card either way")

			// A nested subtest rather than a bare require, so that the SECOND press below is
			// exercised even when this half fails — the two are separate properties and a mutation
			// has to be seen breaking both.
			t.Run("the loss is filed on the row's own ref, never on the colour's", func(t *testing.T) {
				require.False(t, tcacHasLine(resp1.GetReport(), "color_code=BLK", tc.reason),
					"a ROW-level loss standing at the bare colour ref is the collision this guards: the "+
						"client reads it as «this colour never arrived» and priorVerdict reads it as «the "+
						"previous press did not create it»:\n%s", tcacDumpLines(resp1.GetReport()))
				line := tcacLineFor(t, resp1.GetReport(), rowRef, tc.reason)
				require.Equal(t, techcardarchive.StatusSkipped, line.GetStatus())
				require.Equal(t, int32(1), tcrepCounter(t, resp1.GetReport(), techcardarchive.EntityColorway).GetDegraded(),
					"the colour landed, thinner")
			})

			// ── press two: the card now carries the colour, and nothing may be rewritten ──
			second := tcacServer(t)
			card := tcacCard()
			card.Colorways = []entity.TechCardColorway{{Id: 901, ColorCode: "BLK"}}
			row := tcacImportRow(t, payload, "BLK")
			row.Report = afterFirst // the report the FIRST press stored is what the second one reads
			second.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).Return(row, nil).Once()
			second.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(card, nil).Once()
			second.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
			var afterSecond []byte
			second.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).
				Run(func(_ context.Context, _ string, report []byte) { afterSecond = report }).Return(nil).Once()
			// No CreateColorway / UpdateColorwayRecipe / ApplyImportedColorwayPieceMaterials
			// expectations: the strict mock proves the second press writes nothing to the card.

			resp2, err := second.apply(t)
			require.NoError(t, err)
			require.Empty(t, resp2.GetCreatedColorwayIds(), "a second press creates nothing")

			t.Run("the record of that loss survives the second press", func(t *testing.T) {
				tcacLineFor(t, resp2.GetReport(), rowRef, tc.reason)
				require.False(t, tcacHasReason(resp2.GetReport(), techcardarchive.ReasonColorwayExists),
					"replacing the record of a row this press will never re-attempt with «this card already "+
						"has a colourway of this colour» is the silent loss the owner ruled out:\n%s",
					tcacDumpLines(resp2.GetReport()))
				require.JSONEq(t, string(afterFirst), string(afterSecond),
					"a press that changed nothing must leave the report it found")
			})
		})
	}
}
