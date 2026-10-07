package admin

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// M6 (07.10, flat-consistency 107): the PARTS pieces list is read from the accepted FRONT/BACK flats
// from a fixed vocabulary, never from the join list, and the designer's edits are never overwritten.
//
// MUTATIONS THEY CATCH: a name outside the vocabulary kept («inner front v», «right armhole»); a
// pair not split into left/right; a repeat kept; views other than front/back kept; the labeller's
// block without the closed names or with the old CONSTRUCTION words; a designer name «opening»
// accepted; a stale flag that ignores the proposal's plates.

func TestParseDesignPiecesVocabulary(t *testing.T) {
	raw := "```json\n" + `{"pieces":[
		{"name":"Front Body","pair":false,"views":["front","side_l","front"]},
		{"name":"back body","views":["back"]},
		{"name":"strap","pair":true,"views":["back"]},
		{"name":"left strap","pair":true,"views":["back"]},
		{"name":"inner front v","views":["front"]},
		{"name":"right armhole binding","views":["front"]},
		{"name":"armhole panel","pair":"true"},
		{"name":"neckband","views":["front","back"]},
		{"name":"left chest pocket","pair":false},
		"sleeve",
		{"name":"opening"},
		{"name":"collar stand pocket flap"},
		{"name":"inner front layer","views":["front"]}
	],"openings":["Open back", "gap between the two crossed straps here", {"name":"keyhole"}, 7]}` + "\n```"
	doc, dropped, ok := parseDesignPieces(raw)
	require.True(t, ok)
	require.Equal(t, []string{"front body", "back body", "left strap", "right strap", "neckband",
		"left chest pocket", "sleeve", "inner front layer"}, doc.Names())
	require.Equal(t, []string{"front"}, doc.Pieces[0].Views, "views: front/back only, once")
	require.Equal(t, []string{"front", "back"}, doc.Pieces[4].Views)
	require.Equal(t, []string{"open back", "keyhole"}, doc.Openings, "an opening of more than five words is dropped")
	for _, d := range []string{"inner front v", "right armhole binding", "armhole panel", "opening", "collar stand pocket flap"} {
		require.Contains(t, dropped, d)
	}

	for _, bad := range []string{``, `{}`, `{"pieces":[]}`, `{"pieces":[{"name":"armhole binding"}]}`, `not json`} {
		_, _, ok := parseDesignPieces(bad)
		require.False(t, ok, bad)
	}
	many := `{"pieces":[` + strings.Repeat(`{"name":"sleeve","pair":true},{"name":"pocket","pair":true},`, 1) +
		strings.TrimSuffix(strings.Repeat(`{"name":"panel"},`, 3), ",") + `]}`
	doc, _, ok = parseDesignPieces(many)
	require.True(t, ok)
	require.Equal(t, []string{"left sleeve", "right sleeve", "left pocket", "right pocket", "panel"}, doc.Names())
}

func TestDesignPiecesNameOK(t *testing.T) {
	for _, ok := range []string{"front body", "left strap", "collar stand", "pocket flap", "inner front layer",
		"back yoke", "hem band", "right chest pocket", "neckband", "waistband"} {
		require.True(t, designPiecesNameOK(ok), ok)
	}
	for _, bad := range []string{"", "left", "front", "inner front v", "right armhole", "armhole binding",
		"front armhole panel", "upper back opening", "body front", "a b c body"} {
		require.False(t, designPiecesNameOK(bad), bad)
	}
}

func TestDesignPiecesPromptAndBlock(t *testing.T) {
	for _, must := range []string{
		"ACCEPTED technical flats of ONE garment",
		"Read only what is drawn",
		"AN ARMHOLE HAS NO PIECE OF ITS OWN",
		`"pair": true`,
		"NOUNS: body, bodice, panel",
		"QUALIFIERS: front, back",
		"Answer with JSON only",
	} {
		require.Contains(t, designPiecesSystem, must)
	}
	require.NotContains(t, designPiecesSystem, "%s")
	require.NotContains(t, strings.ToLower(designPiecesSystem), "photo")
	require.Equal(t, "image 1: the FRONT flat\nimage 2: the BACK flat\nList the garment's pieces and openings.",
		designPiecesUserPrompt([]string{"front", "back"}))
	require.Contains(t, designPiecesUserPrompt([]string{"back"}), "There is no FRONT flat")

	require.Equal(t, "", designPartsPiecesBlock(nil))
	require.Equal(t, "", designPartsPiecesBlock(&entity.DesignPartsPieces{}))
	p := &entity.DesignPartsPieces{Rev: 4, Doc: entity.DesignPartsPiecesDoc{
		Pieces:   []entity.DesignPartsPiece{{Name: "front body", Views: []string{"front"}}, {Name: "left strap", Views: []string{"back"}}, {Name: "mesh panel"}},
		Openings: []string{"open back"},
	}}
	want := "PIECES of this garment — the CLOSED list, read from its accepted front and back flats. " +
		"Label every region with exactly one of these names, or \"opening\":\n" +
		"- front body\n- left strap\n- mesh panel\n" +
		"Openings (no cloth): open back.\n" +
		"A side view shows only pieces of this list — never a piece of its own. A piece need not have a region on every view: where its lines are not closed it has no region there, and its name never goes to another region instead."
	require.Equal(t, want, designPartsPiecesBlock(p))
	p.EditedAt = sql.NullTime{Time: time.Now(), Valid: true}
	require.Contains(t, designPartsPiecesBlock(p), "and corrected by the designer.")
	require.Equal(t, []string{"front body", "left strap", "mesh panel"}, designPartsPiecesVocab(p))
	require.Nil(t, designPartsPiecesVocab(nil))
	require.Equal(t, 4, designPartsPiecesRev(p))

	// staleness follows the newest read: the proposal's plates when there is one
	require.False(t, designPartsPiecesStale(p, map[string]int{}), "no plate, nothing to be stale against")
	p.Front, p.Back = 11, 12
	require.False(t, designPartsPiecesStale(p, map[string]int{"front": 11, "back": 12, "side_l": 9}))
	require.True(t, designPartsPiecesStale(p, map[string]int{"front": 11, "back": 13}))
	p.Proposal = &entity.DesignPartsPiecesProposal{Front: 11, Back: 13}
	require.False(t, designPartsPiecesStale(p, map[string]int{"front": 11, "back": 13}))
	pb := designPartsPiecesToPb(p, map[string]int{"front": 11, "back": 13})
	require.True(t, pb.GetEdited())
	require.Equal(t, int32(13), pb.GetProposal().GetBackMediaId())
	require.Equal(t, []string{"front"}, pb.GetPieces()[0].GetViews())
}

func TestCleanDesignPartsPieceNames(t *testing.T) {
	got, err := entity.CleanDesignPartsPieceNames([]string{"  Front  Body ", "front body", "Neck-Band", "", "left_strap", "--x--"})
	require.NoError(t, err)
	require.Equal(t, []string{"front body", "neck-band", "left strap", "x"}, got)
	for _, bad := range [][]string{{"opening"}, {"Opening back"}, {"unnamed"}, {strings.Repeat("a", 41)}, {}, {" ", "!"}} {
		_, err := entity.CleanDesignPartsPieceNames(bad)
		require.Error(t, err, bad)
	}
	many := make([]string, entity.DesignPartsPiecesMax+1)
	for i := range many {
		many[i] = "piece " + strings.Repeat("x", i+1)
	}
	_, err = entity.CleanDesignPartsPieceNames(many)
	require.Error(t, err)
}

func TestSetDesignPartsPiecesDoors(t *testing.T) {
	const card = 7
	repo := mocks.NewMockRepository(t)
	design := mocks.NewMockDesign(t)
	repo.EXPECT().Design().Return(design).Maybe()
	srv := &Server{repo: repo}
	for _, r := range []*pb_admin.SetDesignPartsPiecesRequest{
		{TechCardId: 0, Names: []string{"a"}},
		{TechCardId: card, ExpectedRev: -1, Names: []string{"a"}},
		{TechCardId: card, Names: []string{"opening"}},
		{TechCardId: card},
	} {
		_, err := srv.SetDesignPartsPieces(context.Background(), r)
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", r)
	}
	design.EXPECT().SetPartsPieces(mock.Anything, mock.MatchedBy(func(s entity.DesignPartsPiecesSave) bool {
		return s.TechCardId == card && s.ExpectedRev == 3 && s.SettleProposal &&
			strings.Join(s.Names, "|") == "front body|left strap"
	})).Return(&entity.DesignPartsPieces{TechCardId: card, Rev: 4, Front: 11, Back: 12,
		Doc: entity.DesignPartsPiecesDoc{Pieces: []entity.DesignPartsPiece{{Name: "front body"}, {Name: "left strap"}}}}, nil).Once()
	design.EXPECT().FlatBenchMedia(mock.Anything, card).Return(map[string]int{"front": 11, "back": 12}, nil).Once()
	resp, err := srv.SetDesignPartsPieces(context.Background(), &pb_admin.SetDesignPartsPiecesRequest{
		TechCardId: card, ExpectedRev: 3, Names: []string{"Front Body", "left strap", "front body"}, SettleProposal: true})
	require.NoError(t, err)
	require.Equal(t, int32(4), resp.GetPieces().GetRev())
	require.False(t, resp.GetPieces().GetStale())

	design.EXPECT().SetPartsPieces(mock.Anything, mock.Anything).Return(nil, entity.ErrDesignPartsPiecesRevMismatch).Once()
	_, err = srv.SetDesignPartsPieces(context.Background(), &pb_admin.SetDesignPartsPiecesRequest{TechCardId: card, ExpectedRev: 1, Names: []string{"a"}})
	require.Equal(t, codes.Aborted, status.Code(err))
}

// TestDumpDesignPiecesPrompt writes the read's system prompt for the offline probe
// (tmp/plans/flat-consistency/m6_pieces.py): M6_DUMP=<path> go test -run TestDumpDesignPiecesPrompt.
func TestDumpDesignPiecesPrompt(t *testing.T) {
	path := os.Getenv("M6_DUMP")
	if path == "" {
		t.Skip("M6_DUMP not set")
	}
	require.NoError(t, os.WriteFile(path, []byte(designPiecesSystem), 0o644))
	if lp := os.Getenv("M6_DUMP_LABELLER"); lp != "" {
		require.NoError(t, os.WriteFile(lp, []byte(designPartsCardSystemPrompt), 0o644))
	}
}
