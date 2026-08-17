package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ЗАМЕТКА: СТОЛКНОВЕНИЕ ДВУХ ПРАВОК (Ф8).
//
// Правка содержимого без перезаливки впервые создаёт случай, когда двое пишут в один файл. Всё, что
// здесь проверяется, сводится к одному утверждению: ВТОРОЕ СОХРАНЕНИЕ НЕ МОЖЕТ ЗАТЕРЕТЬ ПЕРВОЕ
// МОЛЧА. Тест обязан быть контейнерным — вся защита живёт в перечитке строки внутри транзакции, и
// на моках «проверил и записал» выглядит ровно так же, как «записал вслепую».

// noteObjectKey mints a unique key for a fixture version. Ключ объекта не переиспользуется никогда
// (0312), поэтому каждая версия заметки в тесте получает свой.
func noteObjectKey(t *testing.T, version string) string {
	t.Helper()
	return fmt.Sprintf("files-library/test-notes/%d-%s.md", time.Now().UnixNano(), version)
}

func noteSha(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// saveNoteVersion is one full «залил объект → двинул строку» step, minus the bucket: стор в S3 не
// ходит, поэтому в тесте объект и не нужен — важны ключ, отпечаток и размер, которые он приносит.
func saveNoteVersion(ctx context.Context, t *testing.T, s *MYSQLStore, fileID int, content, base string, force bool) (*entity.LibraryNoteSaveResult, string) {
	t.Helper()
	key := noteObjectKey(t, "v")
	res, err := s.Files().SaveNoteContent(ctx, entity.LibraryNoteSave{
		FileId:         fileID,
		BaseSha256:     base,
		Force:          force,
		ObjectKey:      key,
		Sha256:         noteSha(content),
		SizeBytes:      int64(len(content)),
		ContentExcerpt: content,
		EditedBy:       "kirill",
	})
	require.NoError(t, err)
	return res, key
}

func TestLibraryNoteContentCAS(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	topic := insertFileTopicFixture(ctx, t, "test-note")

	const firstText = "# план\n\nснимаем в четверг"
	firstKey := noteObjectKey(t, "v1")
	insert := &entity.LibraryNoteInsert{
		LibraryFileInsert: entity.LibraryFileInsert{
			ObjectKey:   firstKey,
			FileName:    "план съёмки.md",
			ContentType: "text/markdown",
			SizeBytes:   int64(len(firstText)),
			Sha256:      noteSha(firstText),
			UploadedBy:  "pasha",
		},
		ContentExcerpt:   "план снимаем в четверг",
		ContentUpdatedBy: "pasha",
		ContentUpdatedAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}
	id, err := s.Files().CreateNote(ctx, insert, []int{topic}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM library_file WHERE id = ?`, id) })

	t.Run("a note is an ordinary library file", func(t *testing.T) {
		// Тема, автор и выдержка лежат на обычном файле — никакой параллельной сущности «заметка»
		// не заведено, и это ровно то решение, которое даёт заметке доступ, обсуждение и задачи даром.
		f, err := s.Files().GetFileById(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "план съёмки.md", f.FileName)
		require.Equal(t, "text/markdown", f.ContentType)
		require.Equal(t, "план снимаем в четверг", f.ContentExcerpt)
		require.Equal(t, "pasha", f.ContentUpdatedBy)
		require.True(t, f.ContentUpdatedAt.Valid)
		require.Len(t, f.Topics, 1)
		require.Equal(t, topic, f.Topics[0].Id)
	})

	t.Run("the row carries the pointer, never the text", func(t *testing.T) {
		note, err := s.Files().GetNote(ctx, id)
		require.NoError(t, err)
		require.Equal(t, firstKey, note.ObjectKey)
		require.Equal(t, noteSha(firstText), note.Sha256)
		require.Equal(t, "pasha", note.ContentUpdatedBy)
		require.Equal(t, "pasha", note.UploadedBy)
	})

	const secondText = "# план\n\nснимаем в пятницу"
	var secondKey string

	t.Run("a save from the current base moves the row and retires the old object", func(t *testing.T) {
		res, key := saveNoteVersion(ctx, t, s, id, secondText, noteSha(firstText), false)
		secondKey = key
		require.False(t, res.Conflict)
		require.Equal(t, noteSha(secondText), res.CurrentSha256)
		// Старый ключ уносится наружу — уборка идёт ПОСЛЕ коммита и только там.
		require.Equal(t, firstKey, res.PreviousObjectKey)
		require.Equal(t, "kirill", res.LastEditedBy)
		require.True(t, res.LastEditedAt.Valid)

		note, err := s.Files().GetNote(ctx, id)
		require.NoError(t, err)
		require.Equal(t, secondKey, note.ObjectKey)
		require.Equal(t, noteSha(secondText), note.Sha256)
		require.Equal(t, int64(len(secondText)), note.SizeBytes)
	})

	t.Run("a save from a stale base is refused with the other version, and writes nothing", func(t *testing.T) {
		// Паша всё это время правил ту версию, которую открыл ДО сохранения кирилла.
		staleKey := noteObjectKey(t, "stale")
		res, err := s.Files().SaveNoteContent(ctx, entity.LibraryNoteSave{
			FileId:         id,
			BaseSha256:     noteSha(firstText), // база устарела
			ObjectKey:      staleKey,
			Sha256:         noteSha("моя версия"),
			SizeBytes:      11,
			ContentExcerpt: "моя версия",
			EditedBy:       "pasha",
		})
		require.NoError(t, err, "конфликт — это ДАННЫЕ ответа, а не ошибка")
		require.True(t, res.Conflict)
		require.Equal(t, noteSha(secondText), res.CurrentSha256)
		require.Equal(t, secondKey, res.CurrentObjectKey, "по этому ключу вызывающий читает чужой текст")
		require.Equal(t, "kirill", res.LastEditedBy)
		require.Empty(t, res.PreviousObjectKey, "на конфликте удалять нечего — старого объекта не было")

		// И главное: строка НЕ СДВИНУЛАСЬ. Именно это отличает конфликт от тихой перезаписи.
		note, err := s.Files().GetNote(ctx, id)
		require.NoError(t, err)
		require.Equal(t, secondKey, note.ObjectKey)
		require.Equal(t, noteSha(secondText), note.Sha256)
		require.Equal(t, "kirill", note.ContentUpdatedBy)
	})

	const forcedText = "# план\n\nсъёмка отменена"
	var forcedKey string

	t.Run("force writes over that other version deliberately", func(t *testing.T) {
		res, key := saveNoteVersion(ctx, t, s, id, forcedText, noteSha(firstText), true)
		forcedKey = key
		require.False(t, res.Conflict)
		require.Equal(t, noteSha(forcedText), res.CurrentSha256)
		require.Equal(t, secondKey, res.PreviousObjectKey)

		note, err := s.Files().GetNote(ctx, id)
		require.NoError(t, err)
		require.Equal(t, forcedKey, note.ObjectKey)
	})

	t.Run("two concurrent saves from one base: exactly one wins", func(t *testing.T) {
		// САМАЯ ВАЖНАЯ ПРОВЕРКА ФАЗЫ. Оба сохранения стартуют с одной базы и уходят в базу
		// одновременно; проиграть обязан ровно один, и проигравший обязан УЗНАТЬ об этом.
		base := noteSha(forcedText)
		type outcome struct {
			res *entity.LibraryNoteSaveResult
			key string
			err error
		}
		results := make([]outcome, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range results {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				key := noteObjectKey(t, fmt.Sprintf("race%d", i))
				text := fmt.Sprintf("версия %d", i)
				<-start
				res, err := s.Files().SaveNoteContent(ctx, entity.LibraryNoteSave{
					FileId:         id,
					BaseSha256:     base,
					ObjectKey:      key,
					Sha256:         noteSha(text),
					SizeBytes:      int64(len(text)),
					ContentExcerpt: text,
					EditedBy:       fmt.Sprintf("editor%d", i),
				})
				results[i] = outcome{res: res, key: key, err: err}
			}(i)
		}
		close(start)
		wg.Wait()

		var winners, losers []outcome
		for _, o := range results {
			require.NoError(t, o.err)
			require.NotNil(t, o.res)
			if o.res.Conflict {
				losers = append(losers, o)
			} else {
				winners = append(winners, o)
			}
		}
		require.Len(t, winners, 1, "ровно одно сохранение проходит")
		require.Len(t, losers, 1, "второе получает конфликт, а не тихую перезапись")

		note, err := s.Files().GetNote(ctx, id)
		require.NoError(t, err)
		require.Equal(t, winners[0].key, note.ObjectKey, "в строке лежит версия победителя")
		require.Equal(t, forcedKey, winners[0].res.PreviousObjectKey)
		// Проигравший видит ЧУЖУЮ версию (ту, что победила), а не ту, с которой начинал.
		require.Equal(t, note.Sha256, losers[0].res.CurrentSha256)
		require.Equal(t, winners[0].key, losers[0].res.CurrentObjectKey)
		require.NotEqual(t, losers[0].key, note.ObjectKey, "объект проигравшего в строку не попал")
	})

	t.Run("two concurrent FORCED saves both write — the control for the case above", func(t *testing.T) {
		// Негативный контроль. Без него предыдущая проверка доказывала бы только то, что тест
		// зелёный: два одновременных сохранения ЗДЕСЬ проходят оба, потому что force сравнение
		// отпечатков пропускает. Значит, разницу между «CAS работает» и «CAS отсутствует» этот тест
		// видеть умеет, а не наблюдает случайную сериализацию горутин.
		note, err := s.Files().GetNote(ctx, id)
		require.NoError(t, err)
		base := note.Sha256

		type outcome struct {
			res *entity.LibraryNoteSaveResult
			err error
		}
		results := make([]outcome, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range results {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				text := fmt.Sprintf("форсированная версия %d", i)
				<-start
				res, err := s.Files().SaveNoteContent(ctx, entity.LibraryNoteSave{
					FileId:         id,
					BaseSha256:     base,
					Force:          true,
					ObjectKey:      noteObjectKey(t, fmt.Sprintf("forcerace%d", i)),
					Sha256:         noteSha(text),
					SizeBytes:      int64(len(text)),
					ContentExcerpt: text,
					EditedBy:       fmt.Sprintf("editor%d", i),
				})
				results[i] = outcome{res: res, err: err}
			}(i)
		}
		close(start)
		wg.Wait()

		for i, o := range results {
			require.NoError(t, o.err)
			require.NotNil(t, o.res)
			require.False(t, o.res.Conflict, "сохранение %d просило force и обязано было записать", i)
		}
	})

	t.Run("a save without an uploaded object is refused outright", func(t *testing.T) {
		// Пустой ключ обнулил бы указатель на текст и оставил бы живую строку без содержимого.
		_, err := s.Files().SaveNoteContent(ctx, entity.LibraryNoteSave{FileId: id, Sha256: "x"})
		require.Error(t, err)
		_, err = s.Files().SaveNoteContent(ctx, entity.LibraryNoteSave{FileId: id, ObjectKey: "files-library/x.md"})
		require.Error(t, err)
	})

	t.Run("a note that is gone is not found on either path", func(t *testing.T) {
		_, err := s.Files().GetNote(ctx, 2147483600)
		require.ErrorIs(t, err, sql.ErrNoRows)

		_, err = s.Files().SaveNoteContent(ctx, entity.LibraryNoteSave{
			FileId:    2147483600,
			ObjectKey: noteObjectKey(t, "ghost"),
			Sha256:    noteSha("ghost"),
		})
		require.ErrorIs(t, err, sql.ErrNoRows)
	})
}
