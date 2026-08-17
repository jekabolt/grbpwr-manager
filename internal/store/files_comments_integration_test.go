package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// commentsCounter — то, чем страница списка получит счётчик реплик, когда Ф7 вставит вызов в
// attachRelated. До вставки метод не достаётся ни через dependency.Files (его там нет намеренно —
// это деталь выдачи, а не операция ленты), ни через unexported-имя из чужого пакета; assert по
// узкому интерфейсу даёт проверить группировку УЖЕ СЕЙЧАС, не притаскивая сюда сам fileslibrary.
type commentsCounter interface {
	AttachCommentsCount(ctx context.Context, files []*entity.LibraryFile) error
}

// TestLibraryFileComments — обсуждение файла целиком на стороне стора (Ф5, 0316).
//
// Оно обязано быть интеграционным. Каждое утверждение здесь — утверждение о SQL и о правилах
// удаления В СХЕМЕ: ON DELETE CASCADE от файла, ON DELETE SET NULL на авторе, автоматический
// created_at, который не имеет права двигаться при правке. Ни одно из них не наблюдается в Go.
func TestLibraryFileComments(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	pashaID, pasha := insertAdminFixture(ctx, t, "test-talk-pasha")
	_, kirill := insertAdminFixture(ctx, t, "test-talk-kirill")

	t.Run("реплика записывает обе половины авторства и возвращается как легла", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "talk.pdf", 10, pasha)

		c, err := s.Files().AddComment(ctx, fileID, pasha, "@kirill это финальная версия?")
		require.NoError(t, err)
		require.Equal(t, fileID, c.FileId)
		require.Equal(t, pasha, c.Author)
		// Живая ссылка ВЫВЕДЕНА из той же строки имени одним оператором — вызывающий её не слал.
		require.True(t, c.AuthorId.Valid, "the id half of authorship must be derived from the username half")
		require.EqualValues(t, pashaID, c.AuthorId.Int64)
		// Упоминание хранится плоским текстом: сервер его не разбирает и не размечает.
		require.Equal(t, "@kirill это финальная версия?", c.Body)
		require.False(t, c.EditedAt.Valid, "новая реплика не может быть «изменена»")
		require.False(t, c.CreatedAt.IsZero())
	})

	t.Run("неизвестный автор оставляет ссылку пустой, но имя сохраняет", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "ghost-talk.pdf", 10, pasha)

		c, err := s.Files().AddComment(ctx, fileID, "nobody-with-this-name", "текст")
		require.NoError(t, err)
		require.Equal(t, "nobody-with-this-name", c.Author)
		require.False(t, c.AuthorId.Valid, "ссылка в никуда хуже её отсутствия")
	})

	t.Run("лента плоская и идёт в порядке письма", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "ordered.pdf", 10, pasha)

		for _, body := range []string{"первая", "вторая", "третья"} {
			_, err := s.Files().AddComment(ctx, fileID, pasha, body)
			require.NoError(t, err)
		}
		feed, err := s.Files().ListComments(ctx, fileID)
		require.NoError(t, err)
		require.Len(t, feed, 3)
		bodies := make([]string, 0, len(feed))
		for _, c := range feed {
			bodies = append(bodies, c.Body)
		}
		// Три реплики почти наверняка легли в одну СЕКУНДУ: created_at секундной гранулярности,
		// поэтому порядок держит id, а не время. Сортировка по времени дала бы «ответ раньше
		// вопроса» редко и невоспроизводимо — то есть худшим из возможных способов.
		require.Equal(t, []string{"первая", "вторая", "третья"}, bodies)

		// У файла без обсуждения лента пуста, а не отсутствует.
		empty, err := s.Files().ListComments(ctx, insertLibraryFileFixture(ctx, t, "silent.pdf", 10, pasha))
		require.NoError(t, err)
		require.Empty(t, empty)
	})

	t.Run("правка ставит edited_at и не двигает created_at", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "edited.pdf", 10, pasha)
		c, err := s.Files().AddComment(ctx, fileID, pasha, "как есть")
		require.NoError(t, err)

		// Секунда паузы — чтобы «время правки позже времени письма» было проверяемым, а не
		// совпадением: обе метки TIMESTAMP округлены до секунды.
		time.Sleep(1100 * time.Millisecond)

		edited, err := s.Files().UpdateComment(ctx, c.Id, "поправил")
		require.NoError(t, err)
		require.Equal(t, "поправил", edited.Body)
		require.True(t, edited.EditedAt.Valid, "метка «изменено» рисуется по непустому времени")
		require.True(t, edited.EditedAt.Time.After(edited.CreatedAt))
		// created_at не имеет права двигаться: в MySQL это ловушка объявления колонки (неявный
		// ON UPDATE CURRENT_TIMESTAMP у первой TIMESTAMP-колонки), а не свойство кода.
		require.Equal(t, c.CreatedAt.UTC(), edited.CreatedAt.UTC(), "время письма — факт, а не поле формы")

		// Повторное сохранение того же текста в ту же секунду даёт ноль затронутых строк — и
		// обязано отвечать «сохранено», а не «реплика удалена».
		again, err := s.Files().UpdateComment(ctx, c.Id, "поправил")
		require.NoError(t, err)
		require.Equal(t, "поправил", again.Body)

		// Реплики нет — правка обязана сказать это, а не отчитаться об успехе в пустоту.
		_, err = s.Files().UpdateComment(ctx, 2147483600, "в никуда")
		require.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("удаление реплики: второй раз — ErrNoRows", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "deleted.pdf", 10, pasha)
		c, err := s.Files().AddComment(ctx, fileID, pasha, "лишнее")
		require.NoError(t, err)

		require.NoError(t, s.Files().DeleteComment(ctx, c.Id))
		_, err = s.Files().GetCommentById(ctx, c.Id)
		require.ErrorIs(t, err, sql.ErrNoRows)
		require.ErrorIs(t, s.Files().DeleteComment(ctx, c.Id), sql.ErrNoRows)
	})

	t.Run("в несуществующий файл писать нечего", func(t *testing.T) {
		_, err := s.Files().AddComment(ctx, 2147483600, pasha, "в пустоту")
		require.ErrorIs(t, err, sql.ErrNoRows)

		var orphans int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM library_file_comment WHERE file_id = 2147483600`).Scan(&orphans))
		require.Zero(t, orphans, "отказ обязан быть полным: транзакция откатилась")
	})

	t.Run("удаление аккаунта зануляет ссылку и сохраняет имя", func(t *testing.T) {
		leaverID, leaver := insertAdminFixture(ctx, t, "test-talk-leaver")
		fileID := insertLibraryFileFixture(ctx, t, "leaver-talk.pdf", 10, pasha)

		c, err := s.Files().AddComment(ctx, fileID, leaver, "я это забираю")
		require.NoError(t, err)
		require.True(t, c.AuthorId.Valid)

		_, err = testDB.ExecContext(ctx, `DELETE FROM admins WHERE id = ?`, leaverID)
		require.NoError(t, err)

		stored, err := s.Files().GetCommentById(ctx, c.Id)
		require.NoError(t, err)
		// СТРОКА переживает аккаунт: без неё переписка задним числом теряет говорящих. ССЫЛКА не
		// переживает: связывать больше не с кем.
		require.Equal(t, leaver, stored.Author)
		require.False(t, stored.AuthorId.Valid)
		require.Equal(t, "я это забираю", stored.Body)
	})

	t.Run("удаление файла уносит его обсуждение", func(t *testing.T) {
		fileID, err := s.Files().AddFile(ctx, &entity.LibraryFileInsert{
			ObjectKey:   "files-library/test-talk-cascade",
			FileName:    "cascade.pdf",
			ContentType: "application/pdf",
			UploadedBy:  pasha,
		}, nil, nil)
		require.NoError(t, err)

		for _, body := range []string{"раз", "два"} {
			_, err := s.Files().AddComment(ctx, fileID, kirill, body)
			require.NoError(t, err)
		}

		_, err = s.Files().DeleteFile(ctx, fileID)
		require.NoError(t, err)

		// Обсуждение существует только про живой файл: удалили содержимое — обсуждать нечего.
		// Это граница истории, а не её потеря, и держит её схема (ON DELETE CASCADE), а не код.
		var left int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM library_file_comment WHERE file_id = ?`, fileID).Scan(&left))
		require.Zero(t, left)

		feed, err := s.Files().ListComments(ctx, fileID)
		require.NoError(t, err)
		require.Empty(t, feed)
	})

	t.Run("счётчик реплик берётся одним сгруппированным запросом на страницу", func(t *testing.T) {
		counter, ok := s.Files().(commentsCounter)
		require.True(t, ok, "счётчик страницы обязан существовать до того, как Ф7 вставит его вызов")

		talkative := insertLibraryFileFixture(ctx, t, "talkative.pdf", 10, pasha)
		quiet := insertLibraryFileFixture(ctx, t, "quiet.pdf", 10, pasha)
		for i := 0; i < 3; i++ {
			_, err := s.Files().AddComment(ctx, talkative, kirill, "реплика")
			require.NoError(t, err)
		}

		files := []*entity.LibraryFile{{Id: talkative}, {Id: quiet}}
		require.NoError(t, counter.AttachCommentsCount(ctx, files))
		require.Equal(t, 3, files[0].CommentsCount)
		// Файл без обсуждения не приезжает из GROUP BY вовсе — ноль обязан быть значением по
		// умолчанию поля, а не отдельным запросом.
		require.Zero(t, files[1].CommentsCount)

		// Счётчик совпадает с длиной ленты — иначе на плитке одно число, в карточке другое.
		feed, err := s.Files().ListComments(ctx, talkative)
		require.NoError(t, err)
		require.Len(t, feed, files[0].CommentsCount)

		// Пустая страница не ходит в БД вовсе.
		require.NoError(t, counter.AttachCommentsCount(ctx, nil))
	})

	t.Run("длинная кириллическая реплика влезает в колонку", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "long-talk.pdf", 10, pasha)
		// Предел dto — 10000 РУН; в utf8mb4 кириллица занимает два байта на знак, то есть 20 КБ
		// при 64 КБ колонки. Проверяется именно верхняя граница разрешённого: строгий режим MySQL
		// ответил бы на перебор 1406 Data too long, то есть отказом БД вместо понятной фразы.
		body := strings.Repeat("я", 10000)
		c, err := s.Files().AddComment(ctx, fileID, pasha, body)
		require.NoError(t, err)
		stored, err := s.Files().GetCommentById(ctx, c.Id)
		require.NoError(t, err)
		require.Equal(t, body, stored.Body)
	})
}
