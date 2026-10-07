package admin

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ═══ M14 (owner 07.10): «показывай в WORDS только то, что уходит» ═══
//
// A flat sends the description's class line and then the person's own flat words
// (tech_card.flat_words, typed in FLAT › WORDS). The model-written description never travels to a
// flat; the flat words never travel to any other kind. The preview (PreviewDesignRunInputs) and the
// run assemble through the same designAssembleInputs, so what the preview says is what is frozen.
//
// MUTATIONS IT CATCHES: the flat words dropped from a flat; the flat words reaching a render; the
// description's prose reaching a flat again; the ceiling turned into a trim or applied to a render.
func TestAFlatSendsTheClassLineAndThePersonsFlatWordsOnly(t *testing.T) {
	card := designMoodCard()
	card.GarmentDescription = sql.NullString{String: "garment: tank top\nfit: slim\nTwo-layer sleeveless top, slim body-hugging silhouette.", Valid: true}
	card.FlatWords = sql.NullString{String: "inner V neckline under the sheer layer\n\n crossed straps meet at the back neck ", Valid: true}
	band := designBandWith(false)
	params := &pb_common.DesignRunParams{Layout: designLayoutOne, Views: []string{"front", "back", "side_l", "side_r"}}

	flat, err := designAssembleInputs(designInputSources{Kind: entity.DesignRunKindFlat, Card: card, Refs: band.References, Bench: band.Bench, Params: params})
	require.NoError(t, err)
	require.Equal(t, "garment: tank top\ninner V neckline under the sheer layer\ncrossed straps meet at the back neck", flat.GetGarmentNote())

	render, err := designAssembleInputs(designInputSources{Kind: entity.DesignRunKindRender, Card: card, Refs: band.References, Bench: band.Bench, Params: params})
	require.NoError(t, err)
	require.NotContains(t, render.GetGarmentNote(), "crossed straps", "the flat words are a flat's only")
	require.Contains(t, render.GetGarmentNote(), "Two-layer sleeveless top", "the description stays where it lives for the other kinds")

	// no flat words → the flat sends what it sent before M14
	card.FlatWords = sql.NullString{}
	flat, err = designAssembleInputs(designInputSources{Kind: entity.DesignRunKindFlat, Card: card, Refs: band.References, Bench: band.Bench, Params: params})
	require.NoError(t, err)
	require.Equal(t, "garment: tank top", flat.GetGarmentNote())
}

// The flat words have a ceiling, refused before the reserve; at the ceiling they freeze whole.
func TestRunRefusesOverlongFlatWords(t *testing.T) {
	card := designGuardCard()
	card.FlatWords = sql.NullString{String: strings.Repeat("я", designMaxFlatWordsRunes+1), Valid: true}
	rig := newDesignGuardRig(t, card, designGuardBand())
	_, err := rig.srv.StartDesignRun(designGuardCtx(), designGuardStart(entity.DesignRunKindFlat))
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "flat's words")
	require.Nil(t, rig.sent, "the refusal stands before the reserve")

	// exactly at the ceiling (runes, not bytes) the words arrive whole
	card = designGuardCard()
	words := strings.Repeat("я", designMaxFlatWordsRunes)
	card.FlatWords = sql.NullString{String: words, Valid: true}
	rig = newDesignGuardRig(t, card, designGuardBand())
	_, err = rig.srv.StartDesignRun(designGuardCtx(), designGuardStart(entity.DesignRunKindFlat))
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.Contains(t, string(rig.sent.Inputs), words, "the words freeze whole — never trimmed")
}

// A rerun rides its parent's frozen snapshot: today's flat words — however long — neither reach it
// nor refuse it (Codex M14).
func TestARerunIsNotRefusedForTodaysFlatWords(t *testing.T) {
	card := designGuardCard()
	card.FlatWords = sql.NullString{String: strings.Repeat("a", designMaxFlatWordsRunes+1), Valid: true}
	rig := newDesignGuardRig(t, card, designGuardBand())
	rig.design.EXPECT().GetRun(mock.Anything, 12).Return(&entity.DesignRun{
		Id: 12, TechCardId: designGuardCardID, Kind: entity.DesignRunKindFlat,
		Params: entity.RawJSON(`{"views":["front","back","side_l","side_r"],"layout":"one"}`),
		Inputs: entity.RawJSON(`{"garment_note":"garment: top\nthe words of that day","refs":[{"media_id":700,"role":"front"}]}`),
	}, nil).Once()
	req := designGuardStart(entity.DesignRunKindFlat)
	req.Params = nil
	req.RerunOfRunId = 12
	_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.Contains(t, string(rig.sent.Inputs), "the words of that day")
	require.NotContains(t, string(rig.sent.Inputs), strings.Repeat("a", 50))
}
