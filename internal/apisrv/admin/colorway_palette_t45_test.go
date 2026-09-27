package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// ─────────────────────────────────────────────────────────────────────────────
// T45 (27.09) — the colourway handlers after the palette: the server mints the SKU token and
// refuses one from the client, proposes the family from the main colour's hex, writes a
// development-only mask without reading merchandising, answers field-tagged refusals as
// InvalidArgument, and the «apply to slots» door maps its store answers. Every dependency is a
// STRICT mock: a store call the case does not expect fails the case.
// ─────────────────────────────────────────────────────────────────────────────

type t45Rig struct {
	s        *Server
	products *mocks.MockProducts
	cards    *mocks.MockTechCards
}

// t45Dictionary is the seeded dictionary (0130) the family is proposed from.
func t45Dictionary() *entity.DictionaryInfo {
	hex := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	return &entity.DictionaryInfo{Colors: []entity.Color{
		{ID: 1, Code: "BLK", Name: "black", Hex: hex("#000000")},
		{ID: 2, Code: "WHT", Name: "white", Hex: hex("#FFFFFF")},
		{ID: 3, Code: "NAV", Name: "navy", Hex: hex("#1A2238")},
		{ID: 4, Code: "GRY", Name: "grey", Hex: hex("#808080")},
	}}
}

func t45Server(t *testing.T) *t45Rig {
	t.Helper()
	cache.RefreshDictionary(t45Dictionary())
	t.Cleanup(func() { cache.RefreshDictionary(&entity.DictionaryInfo{}) })

	repo := mocks.NewMockRepository(t)
	products := mocks.NewMockProducts(t)
	cards := mocks.NewMockTechCards(t)
	hero := mocks.NewMockHero(t)
	dict := mocks.NewMockCache(t)
	re := mocks.NewMockRevalidationService(t)
	repo.EXPECT().Products().Return(products).Maybe()
	repo.EXPECT().TechCards().Return(cards).Maybe()
	repo.EXPECT().Hero().Return(hero).Maybe()
	repo.EXPECT().Cache().Return(dict).Maybe()
	hero.EXPECT().RefreshHero(mock.Anything).Return(nil).Maybe()
	dict.EXPECT().GetDictionaryInfo(mock.Anything).Return(t45Dictionary(), nil).Maybe()
	re.EXPECT().RevalidateAll(mock.Anything, mock.Anything).Return(nil).Maybe()
	return &t45Rig{
		s: &Server{
			repo: repo, re: re,
			revalidateSem: make(chan struct{}, 1), revalCtx: context.Background(),
		},
		products: products, cards: cards,
	}
}

// t45Violation returns the BadRequest field violation of a status error, or fails.
func t45Violation(t *testing.T, err error, code codes.Code) *errdetails.BadRequest_FieldViolation {
	t.Helper()
	require.Equal(t, code, status.Code(err), "%v", err)
	for _, d := range status.Convert(err).Details() {
		if br, ok := d.(*errdetails.BadRequest); ok && len(br.FieldViolations) > 0 {
			return br.FieldViolations[0]
		}
	}
	t.Fatalf("%v carries no field violation", err)
	return nil
}

// ─── CreateColorway ───

// Owner's decision 1: the token is the SERVER's. A request that names one is refused with the
// field, and nothing is written. MUTATION: drop the refusal in createColorway — the store is
// called and the strict mock fails the case.
func TestCreateColorwayRefusesAClientSkuToken(t *testing.T) {
	r := t45Server(t)
	_, err := r.s.CreateColorway(context.Background(), &pb_admin.CreateColorwayRequest{
		StyleId:       3,
		Merchandising: &pb_common.ColorwayMerchandisingInsert{ColorCode: "BLK", SkuColorToken: "bkw"},
	})
	fv := t45Violation(t, err, codes.InvalidArgument)
	require.Equal(t, "merchandising.sku_color_token", fv.GetField())
	require.Contains(t, fv.GetDescription(), "server_minted")
}

// Owner's decision 4: an empty family is proposed from the palette's main hex, and the store gets
// the proposed code and name — plus the palette itself, untouched.
func TestCreateColorwayProposesTheFamilyFromThePalette(t *testing.T) {
	r := t45Server(t)
	var got *entity.ColorwayInsert
	var dev *entity.ColorwayDevelopmentPatch
	r.products.EXPECT().CreateColorway(mock.Anything, 3, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ int, prd *entity.ColorwayInsert, _ []int, _ []entity.ColorwayTagInsert,
			_ []entity.ColorwayPriceInsert, d *entity.ColorwayDevelopmentPatch) {
			got, dev = prd, d
		}).Return(41, nil).Once()

	name := "Stretch Limo"
	resp, err := r.s.CreateColorway(context.Background(), &pb_admin.CreateColorwayRequest{
		StyleId:       3,
		Merchandising: &pb_common.ColorwayMerchandisingInsert{},
		Development: &pb_common.ColorwayDevelopmentInsert{
			Name:    name,
			Colours: []*pb_common.ColorwayColour{{Pantone: "19-4005 TCX", Hex: "#2B2C30"}, {Label: "bone", Hex: "#EFE9DC"}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, int32(41), resp.GetColorwayId())
	require.Equal(t, "BLK", got.ProductBodyInsert.ColorCode, "a fabric black files under BLK, not NAV")
	require.Equal(t, "black", got.ProductBodyInsert.Color)
	require.Empty(t, got.ProductBodyInsert.SkuColorToken, "the token is minted by the store, inside its transaction")
	require.False(t, got.RefuseTakenColourToken, "an RPC create never asks for the archive's pre-T45 rule")
	require.Len(t, dev.Colours, 2)
	require.Equal(t, "#2B2C30", dev.Colours[0].Hex)
}

func TestCreateColorwayRefusesAFamilyItCannotPropose(t *testing.T) {
	r := t45Server(t)
	_, err := r.s.CreateColorway(context.Background(), &pb_admin.CreateColorwayRequest{
		StyleId:       3,
		Merchandising: &pb_common.ColorwayMerchandisingInsert{},
		Development:   &pb_common.ColorwayDevelopmentInsert{Colours: []*pb_common.ColorwayColour{{Label: "undyed"}}},
	})
	fv := t45Violation(t, err, codes.InvalidArgument)
	require.Equal(t, "merchandising.color_code", fv.GetField())
	require.Contains(t, fv.GetDescription(), "family_required")
}

func TestCreateColorwayRefusesABadPaletteBeforeTheStore(t *testing.T) {
	r := t45Server(t)
	_, err := r.s.CreateColorway(context.Background(), &pb_admin.CreateColorwayRequest{
		StyleId:       3,
		Merchandising: &pb_common.ColorwayMerchandisingInsert{ColorCode: "BLK"},
		Development:   &pb_common.ColorwayDevelopmentInsert{Colours: []*pb_common.ColorwayColour{{Label: "ink", Hex: "ink"}}},
	})
	fv := t45Violation(t, err, codes.InvalidArgument)
	require.Equal(t, "development.colours[0].hex", fv.GetField())
}

// ─── UpdateColorway ───

// A mask entirely under `development` writes the development block ALONE: merchandising is not
// required, not read, and the store is told so with prd == nil. This is the lab-dip panel's and
// the palette editor's write — before T45 the lab-dip one failed on «merchandising is nil».
// MUTATION: build prd regardless of the mask — the case fails on the nil merchandising.
func TestUpdateColorwayDevelopmentOnlyMaskWritesNoMerchandising(t *testing.T) {
	r := t45Server(t)
	var gotPrd *entity.ColorwayInsert
	var gotDev *entity.ColorwayDevelopmentPatch
	var gotMedia []int
	r.products.EXPECT().UpdateColorway(mock.Anything, 41, 7, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ int, _ int, prd *entity.ColorwayInsert, media []int, _ []entity.ColorwayTagInsert,
			_ []entity.ColorwayPriceInsert, dev *entity.ColorwayDevelopmentPatch) {
			gotPrd, gotDev, gotMedia = prd, dev, media
		}).Return(8, nil).Once()

	resp, err := r.s.UpdateColorway(context.Background(), &pb_admin.UpdateColorwayRequest{
		ColorwayId:              41,
		ExpectedColorwayVersion: 7,
		MediaIds:                []int32{5}, // rides along and is NOT written
		Development: &pb_common.ColorwayDevelopmentInsert{
			Colours:  []*pb_common.ColorwayColour{{Pantone: "19-4052", PantoneSystem: "TCX", Hex: "#0F4C81"}},
			NameI18N: map[int32]string{2: "bleu"},
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"development.colours", "development.nameI18n"}},
	})
	require.NoError(t, err)
	require.Equal(t, int32(8), resp.GetLockVersion())
	require.Nil(t, gotPrd, "a development-only write carries no merchandising row")
	require.Len(t, gotDev.Colours, 1)
	require.Equal(t, map[int]string{2: "bleu"}, gotDev.NameI18n)
	require.False(t, gotDev.HasScalars(), "the mask named the palette and the names only")
	// Media still reach the store (the handler converts them), and the STORE drops them for prd == nil;
	// the store half is covered by the CI probe.
	require.Equal(t, []int{5}, gotMedia)
}

// A merchandising write with an empty family and a palette re-proposes the family from the new
// main colour; without a palette the store keeps the stored family — even when the request moves
// dev_hex (that is a lab-dip fact, not a palette). MUTATION: propose from ColorwayMainHex (dev_hex
// fallback) on update — the second call files the colourway under NAV.
func TestUpdateColorwayReproposesTheFamilyOnlyWithAPalette(t *testing.T) {
	r := t45Server(t)
	var calls []*entity.ColorwayInsert
	r.products.EXPECT().UpdateColorway(mock.Anything, 41, 7, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ int, _ int, prd *entity.ColorwayInsert, _ []int, _ []entity.ColorwayTagInsert,
			_ []entity.ColorwayPriceInsert, _ *entity.ColorwayDevelopmentPatch) {
			calls = append(calls, prd)
		}).Return(8, nil).Twice()

	_, err := r.s.UpdateColorway(context.Background(), &pb_admin.UpdateColorwayRequest{
		ColorwayId: 41, ExpectedColorwayVersion: 7,
		Merchandising: &pb_common.ColorwayMerchandisingInsert{},
		Development:   &pb_common.ColorwayDevelopmentInsert{Colours: []*pb_common.ColorwayColour{{Label: "ink", Hex: "#1F2A44"}}},
	})
	require.NoError(t, err)
	_, err = r.s.UpdateColorway(context.Background(), &pb_admin.UpdateColorwayRequest{
		ColorwayId: 41, ExpectedColorwayVersion: 7,
		Merchandising: &pb_common.ColorwayMerchandisingInsert{},
		Development:   &pb_common.ColorwayDevelopmentInsert{DevHex: "#1F2A44"},
	})
	require.NoError(t, err)
	require.Len(t, calls, 2)
	require.Equal(t, "NAV", calls[0].ProductBodyInsert.ColorCode)
	require.Empty(t, calls[1].ProductBodyInsert.ColorCode, "no palette in the request: the store keeps the family")
}

// A field-tagged refusal from the store (T45: a changed SKU token) reaches the client as
// InvalidArgument with the field, not as a 500. MUTATION: drop the ValidationError branch in
// colorwayWriteError — the answer becomes Internal.
func TestUpdateColorwayStoreRefusalIsInvalidArgument(t *testing.T) {
	r := t45Server(t)
	r.products.EXPECT().UpdateColorway(mock.Anything, 41, 7, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(0, entity.NewFieldViolation("merchandising.sku_color_token", "immutable", "ABC (stored BLK)",
			"send it empty or unchanged")).Once()
	_, err := r.s.UpdateColorway(context.Background(), &pb_admin.UpdateColorwayRequest{
		ColorwayId: 41, ExpectedColorwayVersion: 7,
		Merchandising: &pb_common.ColorwayMerchandisingInsert{ColorCode: "BLK", SkuColorToken: "ABC"},
	})
	fv := t45Violation(t, err, codes.InvalidArgument)
	require.Equal(t, "merchandising.sku_color_token", fv.GetField())
	require.Contains(t, fv.GetDescription(), "immutable")
}

func TestColorwayWriteErrorMapsAFieldViolation(t *testing.T) {
	err := colorwayWriteError(context.Background(), "update", 41,
		fmt.Errorf("wrapped: %w", entity.NewFieldViolation("development.pantone", "derived_from_palette", "", "")))
	require.Equal(t, "development.pantone", t45Violation(t, err, codes.InvalidArgument).GetField())
	// The pre-T45 answers stand.
	require.Equal(t, codes.Aborted, status.Code(colorwayWriteError(context.Background(), "update", 41, entity.ErrTechCardConflict)))
	require.Equal(t, codes.FailedPrecondition, status.Code(colorwayWriteError(context.Background(), "create", 0, entity.ErrColorwayColorExists)))
}

// ─── ApplyColorwayPaletteToSlots (owner's decision 7) ───

func TestApplyColorwayPaletteToSlotsWritesThroughTheStore(t *testing.T) {
	r := t45Server(t)
	r.cards.EXPECT().ApplyColorwayPaletteToSlots(mock.Anything, 41, 7, []entity.ColorwayPaletteSlotAssignment{
		{BomLineKey: "BOM-SHELL", ColourPosition: 0}, {BomLineKey: "BOM-THREAD", ColourPosition: 1},
	}).Return(entity.ColorwayPaletteApplyResult{LockVersion: 8, RowsUpdated: 2, RowsCreated: 1}, nil).Once()
	resp, err := r.s.ApplyColorwayPaletteToSlots(context.Background(), &pb_admin.ApplyColorwayPaletteToSlotsRequest{
		ColorwayId: 41, ExpectedColorwayVersion: 7,
		Assignments: []*pb_admin.ColorwayPaletteSlotAssignment{
			{BomLineKey: " BOM-SHELL ", ColourPosition: 0}, {BomLineKey: "BOM-THREAD", ColourPosition: 1},
		},
	})
	require.NoError(t, err)
	require.Equal(t, int32(8), resp.GetLockVersion())
	require.Equal(t, int32(2), resp.GetRowsUpdated())
	require.Equal(t, int32(1), resp.GetRowsCreated())
}

func TestApplyColorwayPaletteToSlotsRefusesBeforeTheStore(t *testing.T) {
	r := t45Server(t)
	_, err := r.s.ApplyColorwayPaletteToSlots(context.Background(), &pb_admin.ApplyColorwayPaletteToSlotsRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = r.s.ApplyColorwayPaletteToSlots(context.Background(), &pb_admin.ApplyColorwayPaletteToSlotsRequest{ColorwayId: 41})
	require.Equal(t, "assignments", t45Violation(t, err, codes.InvalidArgument).GetField())

	_, err = r.s.ApplyColorwayPaletteToSlots(context.Background(), &pb_admin.ApplyColorwayPaletteToSlotsRequest{
		ColorwayId: 41, Assignments: []*pb_admin.ColorwayPaletteSlotAssignment{{BomLineKey: "A"}, {BomLineKey: "A", ColourPosition: 1}},
	})
	require.Equal(t, "assignments[1].bom_line_key", t45Violation(t, err, codes.InvalidArgument).GetField())
}

func TestApplyColorwayPaletteToSlotsMapsTheStoreAnswers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		code  codes.Code
		field string
	}{
		{"no palette", fmt.Errorf("%w: colourway 41", entity.ErrColorwayNoPalette), codes.FailedPrecondition, "colorway_id"},
		{"stale version", entity.ErrTechCardConflict, codes.Aborted, ""},
		{"released card", entity.ErrTechCardReleased, codes.FailedPrecondition, ""},
		{"absent colourway", sql.ErrNoRows, codes.NotFound, ""},
		{"unknown slot", entity.NewFieldViolation("assignments[0].bom_line_key", "unknown_slot", "X", ""), codes.InvalidArgument, "assignments[0].bom_line_key"},
		{"driver", errors.New("boom"), codes.Internal, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := t45Server(t)
			r.cards.EXPECT().ApplyColorwayPaletteToSlots(mock.Anything, 41, 7, mock.Anything).
				Return(entity.ColorwayPaletteApplyResult{}, tc.err).Once()
			_, err := r.s.ApplyColorwayPaletteToSlots(context.Background(), &pb_admin.ApplyColorwayPaletteToSlotsRequest{
				ColorwayId: 41, ExpectedColorwayVersion: 7,
				Assignments: []*pb_admin.ColorwayPaletteSlotAssignment{{BomLineKey: "X"}},
			})
			if tc.field != "" {
				require.Equal(t, tc.field, t45Violation(t, err, tc.code).GetField())
				return
			}
			require.Equal(t, tc.code, status.Code(err), "%v", err)
		})
	}
}

// ─── the archive import keys a card's colours by SKU token ───

// Since T45 a card's colourway OCCUPIES its SKU token, not its family: a palette colourway of the
// black family (token BKW) does not make an archive's BLK «already on the card», while a legacy
// colourway whose family moved to GRY still holds its BLK token. The create asks for the pre-T45
// refusal (RefuseTakenColourToken) so a token the style holds answers «exists».
func TestApplyImportColorwaysReadsTheCardByToken(t *testing.T) {
	t.Run("a shared family is not the same colour", func(t *testing.T) {
		r := tcacServer(t)
		card := tcacCard()
		card.Colorways = []entity.TechCardColorway{{Id: 777, ColorCode: "BLK", SkuColorToken: "BKW"}}
		r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
			Return(tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK")), "BLK"), nil).Once()
		r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(card, nil).Once()
		r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
		r.tcacArchiveGone()

		var created *entity.ColorwayInsert
		r.products.EXPECT().CreateColorway(mock.Anything, tcacCardID, mock.Anything, mock.Anything,
			mock.Anything, mock.Anything, mock.Anything).
			Run(func(_ context.Context, _ int, prd *entity.ColorwayInsert, _ []int,
				_ []entity.ColorwayTagInsert, _ []entity.ColorwayPriceInsert, _ *entity.ColorwayDevelopmentPatch) {
				created = prd
			}).Return(0, entity.ErrColorwayColorExists).Once()
		// The store refused (say, an archived BLK holds the token); the handler re-reads the card to
		// tell a race from an archived colourway — and the card still shows no BLK TOKEN.
		r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(card, nil).Once()
		r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

		resp, err := r.apply(t)
		require.NoError(t, err)
		require.NotNil(t, created, "BKW is a different colour than the archive's BLK: the create is attempted")
		require.True(t, created.RefuseTakenColourToken, "the archive keeps its colour-keyed idempotency")
		require.Equal(t, "BLK", created.ProductBodyInsert.ColorCode)
		line := tcacLineFor(t, resp.GetReport(), "color_code=BLK", techcardarchive.ReasonColorwayNotCreated)
		require.Contains(t, line.GetDetail(), "ARCHIVED", "a token held by no live colourway is the archived one")
	})

	t.Run("a legacy colourway keeps its token after a family change", func(t *testing.T) {
		r := tcacServer(t)
		card := tcacCard()
		card.Colorways = []entity.TechCardColorway{{Id: 778, ColorCode: "GRY", SkuColorToken: "BLK"}}
		r.cards.EXPECT().GetTechCardImportReport(mock.Anything, tcacCardID).
			Return(tcacImportRow(t, tcacPayloadOf(t, tcacColourway("BLK")), "BLK"), nil).Once()
		r.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, tcacCardID).Return(card, nil).Once()
		r.cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(tcacCatalogue(), nil).Once()
		r.tcacArchiveInBucket(t, tcacPassport())
		r.cards.EXPECT().StampTechCardImportReport(mock.Anything, tcacImportID, mock.Anything).Return(nil).Once()

		resp, err := r.apply(t)
		require.NoError(t, err)
		require.Empty(t, resp.GetCreatedColorwayIds(), "no CreateColorway expectation: the strict mock proves it")
		line := tcacLineFor(t, resp.GetReport(), "color_code=BLK", techcardarchive.ReasonColorwayExists)
		require.Contains(t, line.GetDetail(), "778")
	})
}
