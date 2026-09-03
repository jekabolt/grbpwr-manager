package admin

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestEVERY_RUN_KIND_THE_DOOR_ACCEPTS_HAS_A_PRICE.
//
// ⚠ THIS IS A MONEY GUARD AND NOT A TIDINESS ONE. designEstimateFor returns an INVALID
// NullDecimal for a kind that is not in the table, StartRun stores that as a NULL price_estimate,
// and a run with no estimate RESERVES NOTHING AGAINST THE DAY. So a kind added to the vocabulary
// without a line in the table does not fail, does not log and does not look wrong anywhere — it
// simply spends the owner's money outside the daily cap, for as long as nobody notices. The two
// lists are joined here so that adding a kind and forgetting its price is a red test rather than a
// silent hole.
//
// draft_idea is included deliberately: it is refused by StartDesignRun and executed by
// DraftDesignIdea, which reserves against the same day through the same table.
func TestEVERY_RUN_KIND_THE_DOOR_ACCEPTS_HAS_A_PRICE(t *testing.T) {
	for _, kind := range []string{
		entity.DesignRunKindFlat, entity.DesignRunKindRender, entity.DesignRunKindThreed,
		entity.DesignRunKindVector, entity.DesignRunKindDraftIdea,
		entity.DesignRunKindRecolor, entity.DesignRunKindPattern,
	} {
		require.Truef(t, entity.IsDesignRunKind(kind), "kind %q left the vocabulary", kind)
		est := designEstimateFor(kind, 1)
		require.Truef(t, est.Valid, "kind %q reserves NOTHING against the day: it is missing from "+
			"designPriceEstimate, so its spend never reaches the daily cap", kind)
		require.Truef(t, est.Decimal.IsPositive(), "kind %q reserves zero, which is a claim that it "+
			"is free", kind)
	}
}

// TestARecolourReservesPER_PHOTOGRAPH.
//
// The number is not decoration: it is the denominator of `done · 2 of 3`, it draws the placeholder
// tiles, AND it multiplies the reservation. Four on-model photographs are four paid calls (the
// provider's `n` returns variants of ONE prompt, never four different frames), so a run that
// reserved one would let three times its own spend past the daily cap.
func TestARecolourReservesPER_PHOTOGRAPH(t *testing.T) {
	p := &pb_common.DesignRunParams{ExtraInputMediaIds: []int32{11, 12, 13, 14}}
	require.Equal(t, 4, designRequestedOutputs(entity.DesignRunKindRecolor, p))

	one := designEstimateFor(entity.DesignRunKindRecolor, 1)
	four := designEstimateFor(entity.DesignRunKindRecolor, 4)
	require.True(t, four.Valid && one.Valid)
	require.Truef(t, four.Decimal.Equal(one.Decimal.Mul(decimal.NewFromInt(4))),
		"four photographs must reserve four times one, got %s against %s",
		four.Decimal.String(), one.Decimal.String())

	// A TILE IS ONE PICTURE WHATEVER ELSE IS IN THE PARAMS. The count is not derived from the input
	// list here precisely because that list is required to be of length one, and that is checked
	// once, in its own place.
	require.Equal(t, 1, designRequestedOutputs(entity.DesignRunKindPattern, p))
}

// TestTHE_TWO_NEW_KINDS_REFUSE_BEFORE_ANY_MONEY_IS_RESERVED.
//
// ⚠ THE POSITION OF THIS GATE IS THE POINT. The worker refuses these too — it must, because a media
// row can vanish between the snapshot and the pass — but its refusal arrives AFTER the run row has
// reserved the day's budget and held it until midnight or a terminal transition. A click on a
// button with no photograph selected would cost one hanging reservation per click.
func TestTHE_TWO_NEW_KINDS_REFUSE_BEFORE_ANY_MONEY_IS_RESERVED(t *testing.T) {
	colour := &pb_common.DesignColourRecipe{Code: "OLV"}

	// ─── recolour ───
	err := designRefuseUnworkableSources(entity.DesignRunKindRecolor,
		&pb_common.DesignRunParams{Colour: colour})
	require.Error(t, err, "a recolour with nothing to recolour")
	require.Contains(t, err.Error(), "extra_input_media_ids")
	require.Contains(t, err.Error(), "nothing was charged")

	err = designRefuseUnworkableSources(entity.DesignRunKindRecolor,
		&pb_common.DesignRunParams{ExtraInputMediaIds: []int32{11}})
	require.Error(t, err, "«change the colour» with no colour named is answerable with any shade")
	require.Contains(t, err.Error(), "params.colour")

	// ANY ONE OF THE FOUR SPELLINGS SATISFIES IT — the three colour spellings reach the prompt as
	// one block, and a cloth WITH A PICTURE reaches every call as its second image (J-31), so
	// insisting on a particular one would refuse a legitimate ask.
	for _, c := range []*pb_common.DesignColourRecipe{
		{Code: "OLV"}, {Hex: "#4a5a3c"}, {Words: "deep olive, slightly grey"},
		{Fabrics: []*pb_common.DesignFabricUse{{MediaId: 9, Name: "floral", Kind: "pattern"}}},
	} {
		require.NoError(t, designRefuseUnworkableSources(entity.DesignRunKindRecolor,
			&pb_common.DesignRunParams{ExtraInputMediaIds: []int32{11, 12}, Colour: c}))
	}

	// ⚠ A CLOTH NAMED IN WORDS ALONE IS ITS OWN REFUSAL, NOT «nothing was stated». There is nothing
	// to LAY on the photograph — clothPictures selects on media_id — and a cloth described in words
	// inside a recolour prompt is an invitation to redraw the frame at full price. The sentinel is
	// separate so the screen sends the person to the right fix.
	err = designRefuseUnworkableSources(entity.DesignRunKindRecolor, &pb_common.DesignRunParams{
		ExtraInputMediaIds: []int32{11},
		Colour:             &pb_common.DesignColourRecipe{Fabrics: []*pb_common.DesignFabricUse{{Name: "floral"}}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be laid on a photograph")
	require.Contains(t, err.Error(), "nothing was charged")

	// ─── pattern ───
	named := &pb_common.DesignPatternParams{Name: "chevron"}
	for _, ids := range [][]int32{nil, {11, 12}, {11, 12, 13}} {
		err := designRefuseUnworkableSources(entity.DesignRunKindPattern,
			&pb_common.DesignRunParams{ExtraInputMediaIds: ids, Pattern: named})
		require.Errorf(t, err, "%d sources: a tile glued out of several swatches cannot join to itself",
			len(ids))
	}
	require.NoError(t, designRefuseUnworkableSources(entity.DesignRunKindPattern,
		&pb_common.DesignRunParams{ExtraInputMediaIds: []int32{90}, Pattern: named}))

	// ⚠ AND A PATTERN WITHOUT A NAME IS REFUSED FREE, BECAUSE THE LANDING NEEDS ONE. The tile is
	// filed on the card's shelf in the transaction that closes the run, and design_asset.name is
	// NOT NULL — so a nameless run either breaks the filing of a picture already paid for, or
	// files a row that reaches the next prompt as the bare word «pattern».
	for _, pp := range []*pb_common.DesignPatternParams{nil, {}, {Name: "   "}, {RepeatMm: 120}} {
		err := designRefuseUnworkableSources(entity.DesignRunKindPattern,
			&pb_common.DesignRunParams{ExtraInputMediaIds: []int32{90}, Pattern: pp})
		require.Error(t, err, "a tile with no name has nowhere to land")
		require.Contains(t, err.Error(), "params.pattern.name")
		require.Contains(t, err.Error(), "nothing was charged")
	}

	// THE LENGTH IS THE COLUMN'S, and it is the SAME rule UpsertDesignAsset.name obeys — 60 runes
	// counted in RUNES, because the column is 60 characters and «Ф» is one of them.
	require.NoError(t, designRefuseUnworkableSources(entity.DesignRunKindPattern,
		&pb_common.DesignRunParams{
			ExtraInputMediaIds: []int32{90},
			Pattern:            &pb_common.DesignPatternParams{Name: strings.Repeat("ф", 60)},
		}))
	err = designRefuseUnworkableSources(entity.DesignRunKindPattern, &pb_common.DesignRunParams{
		ExtraInputMediaIds: []int32{90},
		Pattern:            &pb_common.DesignPatternParams{Name: strings.Repeat("ф", 61)},
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "61 characters")

	// ⚠ AND THE REPEAT IS BOUNDED FOR THE SAME REASON THE NAME IS: it lands in a column. Since
	// round 15 `params.pattern.repeat_mm` is copied into design_asset.repeat_mm, which is
	// SMALLINT UNSIGNED — so a five-digit number would fail the FILING of a picture already paid
	// for, with a raw 1264. Before round 15 the number only reached the prompt and needed no bound.
	for _, mm := range []int32{-1, entity.MaxDesignAssetRepeatMm + 1, 70000} {
		err := designRefuseUnworkableSources(entity.DesignRunKindPattern, &pb_common.DesignRunParams{
			ExtraInputMediaIds: []int32{90},
			Pattern:            &pb_common.DesignPatternParams{Name: "chevron", RepeatMm: mm},
		})
		require.Errorf(t, err, "repeat_mm %d", mm)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "params.pattern.repeat_mm")
	}
	for _, mm := range []int32{0, 120, entity.MaxDesignAssetRepeatMm} {
		require.NoErrorf(t, designRefuseUnworkableSources(entity.DesignRunKindPattern,
			&pb_common.DesignRunParams{
				ExtraInputMediaIds: []int32{90},
				Pattern:            &pb_common.DesignPatternParams{Name: "chevron", RepeatMm: mm},
			}), "repeat_mm %d is legal", mm)
	}

	// ─── AND THE GATE IS SILENT FOR EVERY OTHER KIND. A flat with no extra media is the ordinary
	// case, and a rule that leaked onto it would close the band's main route.
	for _, kind := range []string{
		entity.DesignRunKindFlat, entity.DesignRunKindRender,
		entity.DesignRunKindThreed, entity.DesignRunKindVector,
	} {
		require.NoErrorf(t, designRefuseUnworkableSources(kind, &pb_common.DesignRunParams{}), "kind %s", kind)
	}
}

// TestTheNewKindsAreLIVE_IN_THE_VOCABULARY_AND_PRODUCE_THE_RIGHT_PICTURE.
//
// ⚠ THE PICTURE KIND IS NOT COSMETIC ON EITHER ROUTE. A tile that called itself `flat` would become
// selectable into a bench slot — «the front of the garment» would be a square of cloth — and one
// that called itself `render` would satisfy the «3D needs a fabric render first» gate on a card
// where nobody has drawn the garment at all.
func TestTheNewKindsAreLIVE_IN_THE_VOCABULARY_AND_PRODUCE_THE_RIGHT_PICTURE(t *testing.T) {
	require.True(t, entity.IsDesignRunKind(entity.DesignRunKindRecolor))
	require.True(t, entity.IsDesignRunKind(entity.DesignRunKindPattern))

	require.Equal(t, entity.DesignPictureKindPattern,
		entity.DesignPictureKindOfRun(entity.DesignRunKindPattern))
	require.NotEqual(t, entity.DesignPictureKindFlat,
		entity.DesignPictureKindOfRun(entity.DesignRunKindPattern))
	require.NotEqual(t, entity.DesignPictureKindRender,
		entity.DesignPictureKindOfRun(entity.DesignRunKindPattern))

	// A RECOLOURED PHOTOGRAPH IS A RENDER, AND THAT IS TRUE RATHER THAN CONVENIENT: the output is a
	// picture of the garment in a scene. The consequence — such a card passes the 3D gate — is
	// correct: a recoloured real photograph is a better basis for a build than an invented render.
	require.Equal(t, entity.DesignPictureKindRender,
		entity.DesignPictureKindOfRun(entity.DesignRunKindRecolor))

	// ⚠ AND A TILE IS NOT AN AXIS OF THE BENCH. The bench holds the STATE OF THE GARMENT by side;
	// a square of cloth is not a state of anything. The list of bench kinds is unchanged from what
	// it accepted before this wave, so no already-standing slot changed meaning.
	require.True(t, entity.IsDesignPictureKind(entity.DesignPictureKindPattern))
	require.False(t, entity.IsDesignBenchKind(entity.DesignPictureKindPattern))
	for _, k := range []string{
		entity.DesignPictureKindFlat, entity.DesignPictureKindRender, entity.DesignPictureKindThreed,
	} {
		require.Truef(t, entity.IsDesignBenchKind(k), "bench kind %q was accepted before this wave", k)
	}
}

// TestTheSnapshotOfANewKindClaimsONLY_WHAT_IT_ACTUALLY_ATE.
//
// ⚠ THE INPUT SNAPSHOT IS THE HISTORY'S ANSWER TO «what did this run feed the model», and it is the
// only answer there will ever be. A recolour that recorded four bench plates as its inputs would
// send a person looking for why the model used their flats — and there is nothing to find, because
// it never saw them: the worker narrows the list to the named pictures. Two halves of one rule, and
// they cannot disagree, because neither half has anything but the named pictures to offer.
func TestTheSnapshotOfANewKindClaimsONLY_WHAT_IT_ACTUALLY_ATE(t *testing.T) {
	bench := []entity.DesignBenchSlot{
		{Id: 1, TechCardId: 41, ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindFlat,
			PictureId: sql.NullInt32{Int32: 11, Valid: true},
			Picture:   &entity.DesignPicture{Id: 11, MediaId: 111, Kind: entity.DesignPictureKindFlat}},
		{Id: 2, TechCardId: 41, ViewKey: entity.DesignViewBack, Kind: entity.DesignPictureKindFlat,
			PictureId: sql.NullInt32{Int32: 12, Valid: true},
			Picture:   &entity.DesignPicture{Id: 12, MediaId: 112, Kind: entity.DesignPictureKindFlat}},
	}
	params := &pb_common.DesignRunParams{ExtraInputMediaIds: []int32{77}}

	for _, kind := range []string{entity.DesignRunKindRecolor, entity.DesignRunKindPattern} {
		slots, plates := designSelectBench(designInputSources{Kind: kind, Bench: bench, Params: params})
		require.Emptyf(t, slots, "kind %s must not claim bench plates it never sent", kind)
		require.Emptyf(t, plates, "kind %s stamps no source plates", kind)
	}

	// AND THE KIND THAT DOES EAT THE BENCH STILL DOES. A rule that leaked onto it would empty the
	// band's main route, which is the failure mode of every «narrow it for the new case» change.
	//
	// ⚠ КОНТРОЛЬ ПЕРЕВЕДЁН С `flat` НА `render`, И ЭТО НЕ ОСЛАБЛЕНИЕ. Правило имеет ровно двух
	// членов: рендер строится ИЗ ФЛЕТОВ, 3D — из рендеров. Флет не строится ни из чего — он и был
	// тем самым падением в умолчание, из-за которого модель получала свои же старые флеты
	// (K-1). Контроль обязан стоять у вида, который ест верстак ПО СМЫСЛУ, а не по недосмотру.
	slots, _ := designSelectBench(designInputSources{
		Kind: entity.DesignRunKindRender, Bench: bench, Params: params,
	})
	require.Len(t, slots, 2, "a render is built FROM the flat bench")

	// А флет берёт их ТОЛЬКО ПО ПРОСЬБЕ — обе половины гейта, иначе «не берёт» неотличимо от
	// «сборка сломана».
	plain, _ := designSelectBench(designInputSources{
		Kind: entity.DesignRunKindFlat, Bench: bench, Params: params,
	})
	require.Empty(t, plain, "флет без просьбы не берёт плиты флет-слотов")
	asked, _ := designSelectBench(designInputSources{
		Kind:   entity.DesignRunKindFlat,
		Bench:  bench,
		Params: &pb_common.DesignRunParams{ExtraInputMediaIds: []int32{77}, UseFlatSlots: true},
	})
	require.Len(t, asked, 2, "попросили — взял")
}
