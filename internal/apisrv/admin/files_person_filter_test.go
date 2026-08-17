package admin

import (
	"context"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// РАЗБОР ФИЛЬТРА ПО ЧЕЛОВЕКУ НА ГРАНИЦЕ RPC.
//
// Контейнерный тест стора проверяет, что фильтр ОТБИРАЕТ правильно; здесь проверяется то, чего он
// проверить не может, — что до стора вообще доезжает то, что прислал клиент, и что края (0,
// отрицательный id, незнакомая роль, несуществующий человек) разбираются как решено, а не
// превращаются в отказ.
//
// Проверка ловит именно ПРОВОДКУ: перепутанные местами id и роль, потерянное поле, «валидацию»
// существования аккаунта — то есть ошибки, при которых стор безупречен, а раздел всё равно
// отвечает не на тот вопрос.
func TestListLibraryFilesPersonFilterReachesTheStore(t *testing.T) {
	capture := func(t *testing.T, req *pb_admin.ListLibraryFilesRequest) entity.LibraryFileListFilter {
		t.Helper()
		var got entity.LibraryFileListFilter
		files := mocks.NewMockFiles(t)
		files.EXPECT().ListFiles(mock.Anything, mock.Anything).
			Run(func(_ context.Context, f entity.LibraryFileListFilter) { got = f }).
			Return(nil, 0, nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.ListLibraryFiles(ctxAs("pasha", false, filesReadOnly), req)
		require.NoError(t, err)
		return got
	}

	t.Run("человек и роль доезжают до стора порознь и на своих местах", func(t *testing.T) {
		for _, c := range []struct {
			name string
			pb   pb_admin.LibraryFilePersonRole
			want entity.LibraryFilePersonRole
		}{
			{"любая", pb_admin.LibraryFilePersonRole_LIBRARY_FILE_PERSON_ROLE_UNKNOWN, entity.LibraryFilePersonRoleAny},
			{"загрузил", pb_admin.LibraryFilePersonRole_LIBRARY_FILE_PERSON_ROLE_UPLOADED, entity.LibraryFilePersonRoleUploaded},
			{"ведёт", pb_admin.LibraryFilePersonRole_LIBRARY_FILE_PERSON_ROLE_OWNER, entity.LibraryFilePersonRoleOwner},
		} {
			t.Run(c.name, func(t *testing.T) {
				got := capture(t, &pb_admin.ListLibraryFilesRequest{PersonId: 42, PersonRole: c.pb})
				require.Equal(t, 42, got.PersonId)
				require.Equal(t, c.want, got.PersonRole)
			})
		}
	})

	t.Run("несуществующий человек — не ошибка, а обычный запрос", func(t *testing.T) {
		// Проверять существование здесь было бы ОРАКУЛОМ: перебрав id и различая отказ от
		// пустой выдачи, можно пересчитать аккаунты, не имея права читать admins. Поэтому id
		// уезжает в стор как есть — и просто ничему не совпадает.
		got := capture(t, &pb_admin.ListLibraryFilesRequest{PersonId: 2147483000})
		require.Equal(t, 2147483000, got.PersonId)
	})

	t.Run("неположительный id — отсутствие фильтра, а не «никто»", func(t *testing.T) {
		for _, id := range []int32{0, -1, -2147483648} {
			got := capture(t, &pb_admin.ListLibraryFilesRequest{
				PersonId:   id,
				PersonRole: pb_admin.LibraryFilePersonRole_LIBRARY_FILE_PERSON_ROLE_OWNER,
			})
			require.Zero(t, got.PersonId, "нулевой и отрицательный id обязаны СНИМАТЬ фильтр, а не показывать пустую библиотеку")
			require.Equal(t, entity.LibraryFilePersonRoleAny, got.PersonRole,
				"роль без человека обязана обнуляться вместе с ним: иначе она пережила бы снятие фильтра и однажды применилась бы к чужому id")
		}
	})

	t.Run("незнакомая роль расширяется до «любой», а не отказывает", func(t *testing.T) {
		// Роль СУЖАЕТ выборку. Неузнанное сужение показало бы меньше, чем человек просил, и
		// ничего бы об этом не сказало; расширение до «где он числится вообще» — честный
		// ответ на «в какой-то роли».
		got := capture(t, &pb_admin.ListLibraryFilesRequest{PersonId: 7, PersonRole: pb_admin.LibraryFilePersonRole(99)})
		require.Equal(t, 7, got.PersonId)
		require.Equal(t, entity.LibraryFilePersonRoleAny, got.PersonRole)
	})

	t.Run("фильтр по человеку не вытесняет остальные", func(t *testing.T) {
		// Поля живут рядом, а не вместо друг друга: строковый поиск, темы и человек — три
		// разных вопроса, и сужают они вместе.
		got := capture(t, &pb_admin.ListLibraryFilesRequest{
			PersonId:   5,
			PersonRole: pb_admin.LibraryFilePersonRole_LIBRARY_FILE_PERSON_ROLE_UPLOADED,
			TopicIds:   []int32{3, 4},
			Search:     "смета",
			Limit:      50,
			Offset:     10,
		})
		require.Equal(t, 5, got.PersonId)
		require.Equal(t, entity.LibraryFilePersonRoleUploaded, got.PersonRole)
		require.Equal(t, []int{3, 4}, got.TopicIds)
		require.Equal(t, "смета", got.Search)
		require.Equal(t, 50, got.Limit)
		require.Equal(t, 10, got.Offset)
	})
}
