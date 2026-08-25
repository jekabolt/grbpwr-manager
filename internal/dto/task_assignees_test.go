package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// TestTaskAssigneeAliasMerge — правило слияния deprecated-поля 3 со списком исполнителей, ОБА
// направления.
//
// Зачем тест на три строки кода: алиас существует ровно на один релиз и ровно ради переходного окна
// между деплоем бека и клиента. Ошибка в любом из двух направлений не падает и не логируется — она
// просто теряет исполнителей: на входе молча выбрасывает того, кого назначили из старой вкладки; на
// выходе (забыли заполнить поле 3) рисует СТАРОМУ клиенту всю доску неназначенной.
func TestTaskAssigneeAliasMerge(t *testing.T) {
	t.Run("старый клиент: одно поле 3 становится списком из одного", func(t *testing.T) {
		got, err := ConvertPbTaskInsertToEntity(&pb_common.TaskInsert{
			Title:    "отшить семпл",
			Assignee: "pasha",
		})
		require.NoError(t, err)
		require.Equal(t, []string{"pasha"}, got.Assignees)
	})

	t.Run("новый клиент: непустой список выигрывает, алиас отбрасывается", func(t *testing.T) {
		got, err := ConvertPbTaskInsertToEntity(&pb_common.TaskInsert{
			Title:     "отшить семпл",
			Assignee:  "x",
			Assignees: []string{"olya", "kirill"},
		})
		require.NoError(t, err)
		require.Equal(t, []string{"olya", "kirill"}, got.Assignees)
		require.NotContains(t, got.Assignees, "x",
			"алиас при непустом списке — эхо старого поля, а не третий исполнитель")
	})

	t.Run("никто не назначен — пустой набор, а не набор из пустого имени", func(t *testing.T) {
		got, err := ConvertPbTaskInsertToEntity(&pb_common.TaskInsert{Title: "ничья", Assignee: "  "})
		require.NoError(t, err)
		require.Empty(t, got.Assignees)
	})

	t.Run("trim и дедуп — как у labels", func(t *testing.T) {
		got, err := ConvertPbTaskInsertToEntity(&pb_common.TaskInsert{
			Title:     "дубли",
			Assignees: []string{" olya ", "olya", "", "kirill"},
		})
		require.NoError(t, err)
		require.Equal(t, []string{"olya", "kirill"}, got.Assignees)
	})

	// НАЗАД НА ПРОВОД. Это и есть та забывчивость, ради которой тест написан: поле 3 обязано нести
	// первого исполнителя, иначе EmitUnpopulated отдаст "" и старая доска покажет всё неназначенным.
	t.Run("на выходе поле 3 = первый исполнитель", func(t *testing.T) {
		pb := ConvertEntityTaskToPb(&entity.Task{
			Id:         1,
			TaskInsert: entity.TaskInsert{Title: "отшить семпл", Assignees: []string{"olya", "kirill"}},
		})
		require.Equal(t, []string{"olya", "kirill"}, pb.Task.Assignees)
		require.Equal(t, "olya", pb.Task.Assignee)
	})

	t.Run("на выходе у никем не взятой задачи поле 3 пустое", func(t *testing.T) {
		pb := ConvertEntityTaskToPb(&entity.Task{Id: 1, TaskInsert: entity.TaskInsert{Title: "ничья"}})
		require.Empty(t, pb.Task.Assignees)
		require.Equal(t, "", pb.Task.Assignee)
	})
}

// TestTaskLinkKindPerspective — разворот ПЕРСПЕКТИВЫ в хранимую строку.
//
// В хранилище видов два, на проводе три. BLOCKED_BY обязан стать перевёрнутым blocks: сохрани его
// как есть — и «ту задачу надо закончить раньше этой» записалось бы как «эта блокирует ту», то есть
// с точностью до наоборот, и молча.
func TestTaskLinkKindPerspective(t *testing.T) {
	kind, swap, err := ConvertPbTaskLinkKindToEntity(pb_common.TaskLinkKind_TASK_LINK_KIND_BLOCKS)
	require.NoError(t, err)
	require.Equal(t, entity.TaskLinkKindBlocks, kind)
	require.False(t, swap)

	kind, swap, err = ConvertPbTaskLinkKindToEntity(pb_common.TaskLinkKind_TASK_LINK_KIND_BLOCKED_BY)
	require.NoError(t, err)
	require.Equal(t, entity.TaskLinkKindBlocks, kind, "BLOCKED_BY — это blocks, а не третий вид строки")
	require.True(t, swap, "у BLOCKED_BY блокером становится ВТОРАЯ задача")

	kind, swap, err = ConvertPbTaskLinkKindToEntity(pb_common.TaskLinkKind_TASK_LINK_KIND_RELATES)
	require.NoError(t, err)
	require.Equal(t, entity.TaskLinkKindRelates, kind)
	require.False(t, swap)

	// Незаданный вид — отказ, а не молчаливый дефолт: «связать как-нибудь» не значит ничего.
	_, _, err = ConvertPbTaskLinkKindToEntity(pb_common.TaskLinkKind_TASK_LINK_KIND_UNKNOWN)
	require.Error(t, err)
}

// TestTaskLinkRoleToPb — обратный ход: роль, вычисленная стором из того, с какой стороны прочитана
// строка, обязана доехать до контракта неперепутанной.
func TestTaskLinkRoleToPb(t *testing.T) {
	pb := ConvertEntityTaskLinksToPb([]entity.TaskLink{
		{TaskId: 2, Role: entity.TaskLinkRoleBlocks, Title: "а", Status: entity.TaskStatusTodo, Board: entity.TaskBoardDesign},
		{TaskId: 3, Role: entity.TaskLinkRoleBlockedBy, Title: "б", Status: entity.TaskStatusDone, Board: entity.TaskBoardProduction, Archived: true},
		{TaskId: 4, Role: entity.TaskLinkRoleRelates, Title: "в", Status: entity.TaskStatusReview, Board: entity.TaskBoardContent},
	})
	require.Len(t, pb, 3)
	require.Equal(t, pb_common.TaskLinkKind_TASK_LINK_KIND_BLOCKS, pb[0].Kind)
	require.Equal(t, pb_common.TaskLinkKind_TASK_LINK_KIND_BLOCKED_BY, pb[1].Kind)
	require.Equal(t, pb_common.TaskLinkKind_TASK_LINK_KIND_RELATES, pb[2].Kind)
	// Второй конец едет РАЗРЕШЁННЫМ — ради этого связь и вкладывается в карточку, а не отдаётся
	// вторым RPC: без заголовка и статуса бейдж «заблокирована» пришлось бы досчитывать N+1.
	require.Equal(t, "б", pb[1].Title)
	require.Equal(t, pb_common.TaskStatus_TASK_STATUS_DONE, pb[1].Status)
	require.Equal(t, pb_common.TaskBoard_TASK_BOARD_PRODUCTION, pb[1].Board)
	require.True(t, pb[1].Archived)
}
