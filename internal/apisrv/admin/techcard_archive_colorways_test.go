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
		cards: cards, products: products, bucket: bucket,
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
	require.False(t, tcacHasReason(resp.GetReport(), techcardarchive.ReasonColorwaysNotApplied),
		"the «colourways were not created» line must not survive the press that created them:\n%s",
		tcacDumpLines(resp.GetReport()))
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
func TestApplyImportColorwaysTurnsAUniqueCollisionIntoExists(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"the store saw the duplicate", entity.ErrColorwayColorExists},
		{"the UNIQUE saw it first", fmt.Errorf("insert colourway: Error 1062 (23000): Duplicate entry 'x' for key 'uniq_product_style_color'")},
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
			r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

			resp, err := r.apply(t)
			require.NoError(t, err, "a colour that turned out to be taken is not a server fault")
			require.Empty(t, resp.GetCreatedColorwayIds())
			line := tcacLineFor(t, resp.GetReport(), "color_code=BLK", techcardarchive.ReasonColorwayExists)
			require.Equal(t, techcardarchive.StatusDegraded, line.GetStatus())
		})
	}
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
	r.tcacArchiveInBucket(t, tcacPassport())
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
