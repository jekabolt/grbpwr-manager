package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newJoinsSrv(t *testing.T) (*Server, *mocks.MockDesign, *mocks.MockTechCards) {
	repo := mocks.NewMockRepository(t)
	design := mocks.NewMockDesign(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().Design().Return(design).Maybe()
	repo.EXPECT().TechCards().Return(cards).Maybe()
	return &Server{repo: repo}, design, cards
}

// The doors before money: a Server with no AI router (s.ai nil) answers every one of them without
// reaching it.
func TestGenerateDesignJoinsDoorsBeforeMoney(t *testing.T) {
	const card = 38
	refs := []entity.DesignReference{
		{MediaId: 12, Role: "back", Ordinal: 0},
		{MediaId: 11, Role: "front", Ordinal: 1, Note: sql.NullString{String: "the top", Valid: true}},
	}
	note := "a sleeveless top"
	fp := designJoinsFingerprint(designJoinsPhotos(refs), note)

	if _, err := (&Server{}).GenerateDesignJoins(context.Background(), &pb_admin.GenerateDesignJoinsRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("no card: %v", err)
	}
	t.Run("nothing to read", func(t *testing.T) {
		srv, design, cards := newJoinsSrv(t)
		cards.EXPECT().GetTechCardById(mock.Anything, card).Return(&entity.TechCard{}, nil)
		design.EXPECT().GetBand(mock.Anything, card, 1).Return(&entity.DesignBand{}, nil)
		_, err := srv.GenerateDesignJoins(context.Background(), &pb_admin.GenerateDesignJoinsRequest{TechCardId: card})
		if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), designJoinsNothingToReadMsg) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("same source is cached", func(t *testing.T) {
		srv, design, cards := newJoinsSrv(t)
		cards.EXPECT().GetTechCardById(mock.Anything, card).Return(&entity.TechCard{TechCardInsert: entity.TechCardInsert{GarmentDescription: sql.NullString{String: note, Valid: true}}}, nil)
		design.EXPECT().GetBand(mock.Anything, card, 1).Return(&entity.DesignBand{References: refs,
			Joins: &entity.DesignJoins{Rev: 3, SourceFingerprint: fp, Model: "m", CreatedAt: time.Now()}}, nil)
		resp, err := srv.GenerateDesignJoins(context.Background(), &pb_admin.GenerateDesignJoinsRequest{TechCardId: card})
		if err != nil || !resp.GetCached() || resp.GetJoins().GetRev() != 3 {
			t.Fatalf("got %+v %v", resp, err)
		}
	})
	t.Run("an edited list is kept unless force", func(t *testing.T) {
		srv, design, cards := newJoinsSrv(t)
		cards.EXPECT().GetTechCardById(mock.Anything, card).Return(&entity.TechCard{}, nil)
		design.EXPECT().GetBand(mock.Anything, card, 1).Return(&entity.DesignBand{References: refs,
			Joins: &entity.DesignJoins{Rev: 4, SourceFingerprint: "other", EditedAt: sql.NullTime{Time: time.Now(), Valid: true}}}, nil)
		resp, err := srv.GenerateDesignJoins(context.Background(), &pb_admin.GenerateDesignJoinsRequest{TechCardId: card})
		if err != nil || !resp.GetCached() || !resp.GetJoins().GetEdited() {
			t.Fatalf("got %+v %v", resp, err)
		}
	})
	for name, force := range map[string]bool{"a changed source": false, "force": true} {
		t.Run(name+" reaches the gate", func(t *testing.T) {
			srv, design, cards := newJoinsSrv(t)
			cards.EXPECT().GetTechCardById(mock.Anything, card).Return(&entity.TechCard{}, nil)
			design.EXPECT().GetBand(mock.Anything, card, 1).Return(&entity.DesignBand{References: refs,
				Joins: &entity.DesignJoins{Rev: 3, SourceFingerprint: fp}}, nil)
			_, err := srv.GenerateDesignJoins(context.Background(), &pb_admin.GenerateDesignJoinsRequest{TechCardId: card, Force: force})
			if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), designGenerationDisabledMsg) {
				t.Fatalf("a miss must reach the generation gate (off here): %v", err)
			}
		})
	}
}

func TestDesignJoinsPhotosAndPrompt(t *testing.T) {
	photos := designJoinsPhotos([]entity.DesignReference{
		{MediaId: 3, Role: "side_r"}, {MediaId: 2, Role: "back"}, {MediaId: 1, Role: "front", Note: sql.NullString{String: " only  the cut ", Valid: true}},
		{MediaId: 4, Role: ""}, {MediaId: 2, Role: "detail"},
	})
	require.Len(t, photos, 3, "a reference without a role is not read; a media once")
	got := designJoinsUserPrompt(photos, "white rib top")
	want := "image 1: front photo — only the cut\nimage 2: back photo\nimage 3: right side (shows the wearer's RIGHT flank) photo\ngarment note: white rib top\nWrite the join list."
	require.Equal(t, want, got)
	for _, must := range []string{"LAYERS. Garments can have several cloth layers", "SHEER", "DEPTH level per face, not a panel", `"caught_into"`, `"free_edge"`, `"uncertain"`, "starts with \"no\"", `"keep"`, "NP_L/NP_R neck points"} {
		require.Contains(t, designJoinsSystemPrompt, must)
	}
}

// SetDesignJoins cleans the designer's list with the model's cleaner and saves it under CAS.
func TestSetDesignJoinsCleansAndCASes(t *testing.T) {
	const card = 49
	srv, design, _ := newJoinsSrv(t)
	design.EXPECT().GetJoins(mock.Anything, card).Return(&entity.DesignJoins{Rev: 2, Model: "opus",
		Consistency: entity.DesignJoinsConsistency{Consistent: false, KeepMediaIDs: []int{1}, Groups: []entity.DesignJoinsGroup{{MediaIDs: []int{1}}, {MediaIDs: []int{2}}}}}, nil)
	var saved entity.DesignJoinsSave
	design.EXPECT().SaveJoins(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, s entity.DesignJoinsSave) (*entity.DesignJoins, error) {
		saved = s
		return &entity.DesignJoins{Rev: 3, Doc: s.Doc, Consistency: s.Consistency, EditedAt: sql.NullTime{Time: time.Now(), Valid: true}}, nil
	})
	resp, err := srv.SetDesignJoins(context.Background(), &pb_admin.SetDesignJoinsRequest{TechCardId: card, ExpectedRev: 2,
		Joins: &pb_common.DesignJoins{
			Items: []*pb_common.DesignJoinItem{
				{Id: "pocket L", Kind: "pocket", From: "CHEST_L", Text: "patch"},
				{Id: "x", Kind: "ribbon", From: "NP_L", To: "NP_R"},
				{Id: "y", Kind: "seam", From: "NOPE", To: "NP_R"},
				{Id: "yoke", Kind: "seam", From: "YOKE_R", Via: []string{"CBN_LOW", "MOON"}, To: "YOKE_L"},
			},
			Absences:    []string{"no pocket on the right", "edges are raw"},
			Consistency: &pb_common.DesignJoinsConsistency{KeepMediaIds: []int32{2, 99}},
		}})
	require.NoError(t, err)
	require.Equal(t, 2, saved.ExpectedRev)
	require.True(t, saved.Edited)
	require.Equal(t, "opus", saved.Model, "the model that wrote the list stays named")
	require.Len(t, saved.Doc.Items, 2, "an unknown kind and an unknown end drop the item")
	require.Equal(t, "pocket_L", saved.Doc.Items[0].ID)
	require.Equal(t, "L", saved.Doc.Items[0].Side)
	require.Equal(t, "front", saved.Doc.Items[0].View)
	require.Equal(t, []string{"CBN_LOW"}, saved.Doc.Items[1].Via, "an unknown via point is dropped")
	require.Equal(t, "back", saved.Doc.Items[1].View)
	require.Equal(t, []string{"no pocket on the right"}, saved.Doc.Absences, "a positive note is not an absence")
	require.Equal(t, []int{2}, saved.Consistency.KeepMediaIDs, "keep narrows to the photos the call read")
	require.Equal(t, int32(3), resp.GetJoins().GetRev())

	if _, err := srv.SetDesignJoins(context.Background(), &pb_admin.SetDesignJoinsRequest{TechCardId: card, ExpectedRev: -1, Joins: &pb_common.DesignJoins{}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("negative rev: %v", err)
	}
}

// The door of a flat run: flare is frozen when the run names no engine (and the table lists it), the
// join list rides a garment flat (a rerun: its parent's copy), a garment sheet buys candidates.
func TestFlatRouteDoor(t *testing.T) {
	srv := &Server{}
	srv.SetDesignEngines(func() []designgen.Engine { return designgen.EngineTable("") })

	p := &pb_common.DesignRunParams{Views: []string{"front", "back"}, Layout: designLayoutOne}
	srv.designFreezeFlatModel(entity.DesignRunKindFlat, p)
	require.Equal(t, designgen.FlatDefaultEngine, p.GetImage().GetModel())
	named := &pb_common.DesignRunParams{Image: &pb_common.DesignImageOptions{Model: designgen.EngineGPTImage2}}
	srv.designFreezeFlatModel(entity.DesignRunKindFlat, named)
	require.Equal(t, designgen.EngineGPTImage2, named.GetImage().GetModel(), "a named engine is the person's")
	render := &pb_common.DesignRunParams{}
	srv.designFreezeFlatModel(entity.DesignRunKindRender, render)
	require.Nil(t, render.GetImage(), "only flats")
	custom := &Server{}
	custom.SetDesignEngines(func() []designgen.Engine { return designgen.EngineTable("acme/custom") })
	q := &pb_common.DesignRunParams{}
	custom.designFreezeFlatModel(entity.DesignRunKindFlat, q)
	require.Nil(t, q.GetImage(), "a deployment whose table is empty keeps its own slug")

	doc := entity.DesignJoinsDoc{Items: []entity.DesignJoinItem{{ID: "hem", Kind: "edge", From: "HEM_L", To: "HEM_R"}}}
	band := &entity.DesignBand{Joins: &entity.DesignJoins{Doc: doc}}
	require.Equal(t, &doc, designRunJoins(entity.DesignRunKindFlat, p, band, nil))
	require.Nil(t, designRunJoins(entity.DesignRunKindFlat, &pb_common.DesignRunParams{Views: []string{"detail"}}, band, nil))
	require.Nil(t, designRunJoins(entity.DesignRunKindRender, p, band, nil))
	parentDoc := entity.DesignJoinsDoc{Items: []entity.DesignJoinItem{{ID: "old", Kind: "edge", From: "HEM_L", To: "HEM_R"}}}
	raw, _ := designSpliceJoins([]byte(`{"garment_note":"x"}`), &parentDoc)
	var probe map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &probe))
	require.Contains(t, probe, "garment_note")
	got := designRunJoins(entity.DesignRunKindFlat, p, band, &entity.DesignRun{Inputs: entity.RawJSON(raw)})
	require.Equal(t, "old", got.Items[0].ID, "a rerun carries its parent's list, not today's")

	require.Equal(t, designgen.FlatCandidates, designRequestedOutputs(entity.DesignRunKindFlat, p))
	require.Equal(t, 1, designImageVariantsPerCall(entity.DesignRunKindFlat, p), "the worker splits over an engine's n")
}
