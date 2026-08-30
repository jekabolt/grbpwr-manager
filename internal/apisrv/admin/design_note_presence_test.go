package admin

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// ТРИ СОСТОЯНИЯ ЗАПИСКИ ДОЕЗЖАЮТ ДО СТОРА РАЗЛИЧИМЫМИ.
//
// Это проба ШВА, а не колонки: колонку проверяет
// TestDesignDBReferenceNoteSurvivesASaveThatDoesNotMentionIt на живой базе. Здесь утверждается
// ровно одно — что хендлер читает ПРИСУТСТВИЕ поля с указателя, до всякого GetNote().
//
// ПОЧЕМУ ЭТО ОТДЕЛЬНОЕ УТВЕРЖДЕНИЕ. `req.GetNote()` возвращает "" и когда поле не пришло, и когда
// пришло пустым. Написать `NoteOmitted: req.GetNote() == ""` — правка на один символ, она
// компилируется, и она уничтожает ровно половину смысла: «сотри» становится неотличимо от
// «промолчи», и человек теряет законный жест. Ни одна проба колонки этого не поймает, потому что
// до колонки доедет уже схлопнутое значение.
func TestReferenceNoteCarriesItsThreeStatesToTheStore(t *testing.T) {
	capture := func(t *testing.T, req *pb_admin.SetDesignReferenceRoleRequest) entity.DesignReferenceRole {
		t.Helper()
		repo := mocks.NewMockRepository(t)
		dsg := mocks.NewMockDesign(t)
		repo.EXPECT().Design().Return(dsg).Once()
		var sent entity.DesignReferenceRole
		dsg.EXPECT().SetReferenceRole(mock.Anything, mock.AnythingOfType("entity.DesignReferenceRole")).
			Run(func(_ context.Context, r entity.DesignReferenceRole) { sent = r }).
			Return(&entity.DesignReference{TechCardId: designGuardCardID, MediaId: 100}, nil).Once()
		srv := &Server{repo: repo}
		_, err := srv.SetDesignReferenceRole(designGuardCtx(), req)
		require.NoError(t, err)
		return sent
	}

	base := func() *pb_admin.SetDesignReferenceRoleRequest {
		return &pb_admin.SetDesignReferenceRoleRequest{
			TechCardId: designGuardCardID, MediaId: 100,
			Role: entity.DesignViewFront, Ordinal: 1,
		}
	}

	t.Run("поля нет — про записку ничего не сказано", func(t *testing.T) {
		req := base() // Note остаётся nil
		sent := capture(t, req)
		require.True(t, sent.NoteOmitted,
			"отсутствие поля обязано доехать как «не трогать»: иначе вкладка со старым JS сотрёт чужие слова")
		require.Equal(t, "", sent.Note)
	})

	t.Run("поле есть и пусто — стереть", func(t *testing.T) {
		req := base()
		req.Note = proto.String("")
		sent := capture(t, req)
		require.False(t, sent.NoteOmitted,
			"явная пустая строка — это ЖЕСТ человека «сотри», а не молчание; схлопнув их, "+
				"починка отняла бы у него законное действие")
		require.Equal(t, "", sent.Note)
	})

	t.Run("поле есть с текстом — записать", func(t *testing.T) {
		req := base()
		req.Note = proto.String("  the fabric, not the cut  ")
		sent := capture(t, req)
		require.False(t, sent.NoteOmitted)
		require.Equal(t, "the fabric, not the cut", sent.Note,
			"пробелы по краям снимаются, как и у роли")
	})
}
