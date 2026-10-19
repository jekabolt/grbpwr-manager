package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/router"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeFittingDraftModel struct {
	result   *aiprov.ChatResult
	err      error
	models   []string
	requests []aiprov.ChatRequest
}

func (f *fakeFittingDraftModel) Chat(_ context.Context, model string, req aiprov.ChatRequest) (*aiprov.ChatResult, error) {
	f.models = append(f.models, model)
	f.requests = append(f.requests, req)
	return f.result, f.err
}

func newFittingDraftServer(t *testing.T, answer string, fitting *entity.Fitting) (*Server, *fakeFittingDraftModel) {
	t.Helper()
	model := &fakeFittingDraftModel{result: &aiprov.ChatResult{Text: answer, Provider: "fake", Model: "test/model"}}
	fittings := mocks.NewMockFittings(t)
	fittings.EXPECT().GetFittingById(mock.Anything, fitting.Id).Return(fitting, nil).Once()
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Fittings().Return(fittings).Once()
	return &Server{repo: repo, ai: router.NewSingle("fake", model, "test/model")}, model
}

func TestDraftFittingEditsDraftsAndDeduplicates(t *testing.T) {
	fitting := &entity.Fitting{Id: 42, FittingInsert: entity.FittingInsert{
		ChangeRequests: []entity.FittingChangeRequest{{Note: "Shorten the right sleeve by 2 cm."}},
	}}
	answer := `{"edits":[
		{"note":"shorten right sleeve 2cm.","target":"construction"},
		{"note":"take in collar at CB 5 mm, it stands away from the neck.","target":"PATTERN"},
		{"note":"Rotate left sleeve pitch back","target":"not-a-target"},
		{"note":"Recheck sleeve length after steaming.","target":"other"}
	]}`
	s, model := newFittingDraftServer(t, answer, fitting)

	resp, err := s.DraftFittingEdits(context.Background(), &pb_admin.DraftFittingEditsRequest{
		FittingId: 42,
		Note: "Overall reads right. Collar stands away from the neck at CB, take in 5 mm. " +
			"Left sleeve pitch twists forward. Fabric relaxes after 20 min wear, " +
			"recheck sleeve length after steaming.",
	})
	require.NoError(t, err)
	require.Equal(t, []*pb_admin.DraftFittingEdit{
		{Note: "Take in collar at CB 5 mm, it stands away from the neck", Target: "pattern"},
		{Note: "Rotate left sleeve pitch back", Target: ""},
		{Note: "Recheck sleeve length after steaming", Target: "other"},
	}, resp.GetEdits())

	require.Equal(t, []string{"test/model"}, model.models)
	require.Len(t, model.requests, 1)
	call := model.requests[0]
	require.True(t, call.JSONMode)
	require.Equal(t, draftFittingEditsMaxTokens, call.MaxTokens)
	require.Equal(t, "none", call.Effort)
	require.Contains(t, call.System, "one edit for each actionable sentence or clause")
	require.Contains(t, call.User, "EXISTING CHANGE REQUESTS:")
	require.Contains(t, call.User, "Shorten the right sleeve by 2 cm.")
}

func TestDraftFittingEditsEmptyNoteMakesNoCalls(t *testing.T) {
	resp, err := (&Server{}).DraftFittingEdits(context.Background(), &pb_admin.DraftFittingEditsRequest{
		FittingId: 7,
		Note:      "  \n ",
	})
	require.NoError(t, err)
	require.Empty(t, resp.GetEdits())
}

func TestDraftFittingEditsValidatesBeforeCallingModel(t *testing.T) {
	for name, req := range map[string]*pb_admin.DraftFittingEditsRequest{
		"missing fitting": {Note: "Shorten sleeve"},
		"note too long":   {FittingId: 1, Note: strings.Repeat("ж", draftFittingEditsMaxNoteRunes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := (&Server{}).DraftFittingEdits(context.Background(), req)
			require.Nil(t, resp)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
			require.NotNil(t, fieldViolationOf(t, err))
		})
	}
}

func TestDraftFittingEditsRejectsMalformedModelAnswer(t *testing.T) {
	fitting := &entity.Fitting{Id: 9}
	s, model := newFittingDraftServer(t, `{"drafts":[]}`, fitting)

	resp, err := s.DraftFittingEdits(context.Background(), &pb_admin.DraftFittingEditsRequest{
		FittingId: 9,
		Note:      "Shorten sleeve by 1 cm",
	})
	require.Nil(t, resp)
	require.Equal(t, codes.Internal, status.Code(err), "%v", err)
	require.Len(t, model.requests, 1)
}

func TestDuplicateFittingEditKeepsDifferentLocations(t *testing.T) {
	require.True(t, duplicateFittingEdit("Take in collar at CB 5 mm", []string{"TAKE IN THE COLLAR AT CB 5MM."}))
	require.False(t, duplicateFittingEdit("Take in collar at CB 5 mm", []string{"Take in collar at CF 5 mm"}))
	require.True(t, duplicateFittingEdit("Recheck sleeve length after steaming", []string{"Checked the sleeve length after steaming"}))
}
