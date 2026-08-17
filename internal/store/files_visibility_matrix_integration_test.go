package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// МАТРИЦА ВИДИМОСТИ (Ф7, T-7.9) — ПРИЁМКА ПРЕДИКАТА.
//
// Она проверяет ИСЧЕЗНОВЕНИЕ, а не отказ на открытии, и это главное её свойство. Имена файлов в
// этой библиотеке говорящие («смета подрядчику», «претензия фабрике»), поэтому ограниченный файл,
// который виден в сетке и отказывает при клике, УЖЕ УТЁК: утекло имя. Каждая проверка ниже
// сформулирована как «этой строки в ответе НЕТ», а не как «на эту строку ответили отказом».
//
// Матрица обязана быть контейнерной: предикат — это SQL и схема (живая ссылка uploaded_by_id,
// CASCADE у поимённого списка, ENUM уровня), и ни одно её утверждение не наблюдаемо в Go.
//
// ПЯТЬ РОЛЕЙ:
//
//	супер          — обходит предикат целиком;
//	загрузивший    — плечо 2, по ЖИВОЙ ссылке uploaded_by_id;
//	владелец       — плечо 4, иначе файл пропал бы у того, кто им распоряжается;
//	в списке people — плечо 3;
//	чужой          — единственный, у кого файл обязан ПРОПАСТЬ отовсюду.

// insertLibraryFileWithSha кладёт строку с ЗАДАННЫМ отпечатком: подсказка о дубликате при
// загрузке (точка 4) без него непроверяема — FindFilesBySha256 на пустой sha не ищет вовсе.
func insertLibraryFileWithSha(ctx context.Context, t *testing.T, name, uploader, sha string) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx, `INSERT INTO library_file
		(object_key, file_name, content_type, size_bytes, sha256, uploaded_by, uploaded_by_id)
		VALUES (CONCAT('files-library/test-', UUID_SHORT()), ?, 'application/pdf', 10, ?, ?,
			(SELECT a.id FROM admins a WHERE a.username = ?))`,
		name, sha, uploader, uploader)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM library_file WHERE id = ?`, id) })
	return int(id)
}

func containsFileID(files []entity.LibraryFile, id int) bool {
	for _, f := range files {
		if f.Id == id {
			return true
		}
	}
	return false
}

func TestLibraryFileVisibilityMatrix(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	admin := superCtx(ctx)

	_, uploader := insertAdminFixture(ctx, t, "test-vis-uploader")
	ownerID, owner := insertAdminFixture(ctx, t, "test-vis-owner")
	listedID, listed := insertAdminFixture(ctx, t, "test-vis-listed")
	_, stranger := insertAdminFixture(ctx, t, "test-vis-stranger")

	// Маркер делает выборки СВОИМИ: сетка и поиск идут по всей библиотеке, а в контейнере уже
	// лежат файлы соседних тестов. Без маркера «файла нет в ответе» доказывало бы что угодно.
	marker := fmt.Sprintf("vismatrix%d", time.Now().UnixNano())
	sha := fmt.Sprintf("%064d", time.Now().UnixNano()%1e10)

	restricted := insertLibraryFileWithSha(ctx, t, marker+"-secret.pdf", uploader, sha)
	teamFile := insertLibraryFileWithSha(ctx, t, marker+"-open.pdf", uploader, sha)

	// Тема несёт ОБА файла — на ней и видно, что счётчик у разных людей разный.
	topicID := insertFileTopicFixture(ctx, t, "vis-matrix")
	linkFileTopicFixture(ctx, t, restricted, topicID)
	linkFileTopicFixture(ctx, t, teamFile, topicID)

	// Владельцем и списком распоряжаемся под супером: SetFileOwners сам стоит под предикатом, и
	// голый контекст после переключения уровня до него уже не дотянулся бы.
	require.NoError(t, s.Files().SetFileOwners(admin, restricted, []int{ownerID}, uploader))
	_, err = s.Files().SetFileAccess(admin, restricted, entity.LibraryFileAccessUpdate{
		Level: entity.LibraryFileAccessPeople, AdminIDs: []int{listedID}, Actor: uploader,
	})
	require.NoError(t, err)

	// Обсуждение, задача и вложение заводятся ДО прогона ролей — их исчезновение и проверяется.
	comment, err := s.Files().AddComment(admin, restricted, uploader, "внутренняя пометка")
	require.NoError(t, err)
	res, err := testDB.ExecContext(ctx,
		`INSERT INTO task (title, board, status) VALUES (CONCAT(?, '-task'), 'design', 'todo')`, marker)
	require.NoError(t, err)
	taskID64, err := res.LastInsertId()
	require.NoError(t, err)
	taskID := int(taskID64)
	t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM task WHERE id = ?`, taskID) })
	require.NoError(t, s.Tasks().AttachFileToTask(admin, restricted, taskID))

	roles := []struct {
		name   string
		ctx    context.Context
		canSee bool
	}{
		{"супер", admin, true},
		{"загрузивший", viewerCtx(ctx, uploader), true},
		{"владелец", viewerCtx(ctx, owner), true},
		{"в списке people", viewerCtx(ctx, listed), true},
		{"чужой", viewerCtx(ctx, stranger), false},
	}

	// strangerTotal снимается ПЕРВЫМ и до супер-подсчёта: соседний тест может добавить файл между
	// двумя замерами, и тогда растёт ТОЛЬКО второе число — разница «супер видит хотя бы на один
	// больше» от этого не ломается, а обратный порядок ломался бы.
	var strangerTotal, superTotal int

	for _, role := range roles {
		t.Run(role.name, func(t *testing.T) {
			rctx := role.ctx

			// 1. СЕТКА. Точка 1.
			grid, _, err := s.Files().ListFiles(rctx, entity.LibraryFileListFilter{
				Search: marker, Limit: 100,
			})
			require.NoError(t, err)
			require.True(t, containsFileID(grid, teamFile),
				"обычный файл обязан быть виден всем — иначе предикат режет не то")
			require.Equal(t, role.canSee, containsFileID(grid, restricted),
				"ограниченный файл обязан ПРОПАСТЬ из сетки, а не отказывать на открытии")

			// 2. ПОИСК ПО АВТОРУ — дыра Р2. Поиск расширен на uploaded_by, и без предиката В ТОМ ЖЕ
			// WHERE запрос «что заливал N» вытащил бы имя закрытого файла мимо фильтра.
			byAuthor, _, err := s.Files().ListFiles(rctx, entity.LibraryFileListFilter{
				Search: uploader, Limit: 1000,
			})
			require.NoError(t, err)
			require.True(t, containsFileID(byAuthor, teamFile))
			require.Equal(t, role.canSee, containsFileID(byAuthor, restricted),
				"поиск по автору не имеет права быть обходом предиката")

			// 3. СЧЁТЧИКИ ТЕМ. Точка 5: у разных людей числа РАЗНЫЕ, и это принято сознательно.
			topics, _, total, err := s.Files().ListTopics(rctx)
			require.NoError(t, err)
			var seen *entity.FileTopicWithCount
			for i := range topics {
				if topics[i].Id == topicID {
					seen = &topics[i]
				}
			}
			require.NotNil(t, seen, "пустая тема обязана остаться в рельсе — в неё кладут новое")
			if role.canSee {
				require.Equal(t, 2, seen.FilesCount)
			} else {
				require.Equal(t, 1, seen.FilesCount,
					"счётчик темы обязан считаться ПОД предикатом: одинаковое у всех число само по себе рассказало бы, что в теме есть что-то закрытое")
			}
			if role.name == "чужой" {
				strangerTotal = total
			}
			if role.name == "супер" {
				superTotal = total
			}

			// 4. КАРТОЧКА. Точка 2 — отказ ДО минта presigned-ссылок.
			card, err := s.Files().GetFileById(rctx, restricted)
			if role.canSee {
				require.NoError(t, err)
				require.Equal(t, entity.LibraryFileAccessPeople, card.AccessLevel)
				require.Equal(t, 1, card.CommentsCount,
					"счётчик реплик обязан приезжать вместе с темами и владельцами (строка в attachRelated)")
			} else {
				require.ErrorIs(t, err, sql.ErrNoRows,
					"невидимый файл обязан быть неотличим от несуществующего")
			}

			// 5. ВЛОЖЕНИЯ ЗАДАЧИ. Точка 3 — резолв набора id.
			attached, err := s.Files().ListFilesByIds(rctx, []int{teamFile, restricted})
			require.NoError(t, err)
			require.True(t, containsFileID(attached, teamFile),
				"одно ограниченное вложение не повод не показать остальные")
			require.Equal(t, role.canSee, containsFileID(attached, restricted))

			// 6. ПОДСКАЗКА О ДУБЛИКАТЕ. Точка 4 — самая забываемая: она печатает ИМЯ файла.
			dupes, err := s.Files().FindFilesBySha256(rctx, sha)
			require.NoError(t, err)
			require.True(t, containsFileID(dupes, teamFile))
			require.Equal(t, role.canSee, containsFileID(dupes, restricted),
				"подобрав отпечаток, чужой узнал бы имя закрытого файла, не открывая библиотеку")

			// 7. ЛЕНТА ОБСУЖДЕНИЯ. Точка 7.
			feed, err := s.Files().ListComments(rctx, restricted)
			require.NoError(t, err)
			if role.canSee {
				require.Len(t, feed, 1)
			} else {
				require.Empty(t, feed, "лента закрытого файла обязана быть пустой, а не отказом")
			}
			one, err := s.Files().GetCommentById(rctx, comment.Id)
			if role.canSee {
				require.NoError(t, err)
				require.Equal(t, comment.Id, one.Id)
			} else {
				require.ErrorIs(t, err, sql.ErrNoRows,
					"перебор id реплик не имеет права обходить файл")
			}

			// 8. ЗАДАЧИ ФАЙЛА. Точка 8 — заголовки чужих задач рассказывают, чем занят закрывший.
			tasks, err := s.Tasks().ListTasksByFileId(rctx, restricted)
			require.NoError(t, err)
			if role.canSee {
				require.Len(t, tasks, 1)
			} else {
				require.Empty(t, tasks)
			}

			// 9. БЛОК ДОСТУПА. Точка 12 — и именно sql.ErrNoRows, из него хендлер делает NotFound.
			access, err := s.Files().GetFileAccess(rctx, restricted)
			if role.canSee {
				require.NoError(t, err)
				require.Equal(t, entity.LibraryFileAccessPeople, access.Level)
			} else {
				require.ErrorIs(t, err, sql.ErrNoRows)
				require.Nil(t, access, "пусто без ошибки молча открыло бы точку 12")
			}

			// 9б. ЖУРНАЛ ДОСТУПА. Предикат живёт ВНУТРИ ListFileAccessEvents, а не только у его
			// сегодняшнего вызывающего: журнал — это перечисление, кому и когда открывали файл,
			// ИМЕНАМИ, и «его зовут после гейта» — свойство порядка вызовов, а не метода.
			events, err := s.Files().ListFileAccessEvents(rctx, restricted, 0)
			if role.canSee {
				require.NoError(t, err)
				require.NotEmpty(t, events)
			} else {
				require.ErrorIs(t, err, sql.ErrNoRows)
				require.Empty(t, events)
			}

			// 10. ЗАМЕТКА. Точка 9 — родилась в Ф8 с оставленной точкой подстановки.
			note, err := s.Files().GetNote(rctx, restricted)
			if role.canSee {
				require.NoError(t, err)
				require.Equal(t, restricted, note.FileId)
			} else {
				require.ErrorIs(t, err, sql.ErrNoRows)
			}

			// 11. ВИТРИНА ОТКРЫТОГО. Точка 6 — и счёт, и страница под одним условием.
			shared, sharedTotal, err := s.Files().ListSharedFiles(rctx, entity.SharedLibraryFileFilter{Limit: 1000})
			require.NoError(t, err)
			found := false
			for _, r := range shared {
				if r.File.Id == restricted {
					found = true
				}
			}
			require.Equal(t, role.canSee, found,
				"витрина показывает ровно то, что человек и так видит")
			require.GreaterOrEqual(t, sharedTotal, len(shared),
				"счёт и страница обязаны описывать одну выборку")
		})
	}

	t.Run("общий счётчик библиотеки у разных людей разный", func(t *testing.T) {
		require.GreaterOrEqual(t, superTotal, strangerTotal+1,
			"total рельса считается под предикатом: закрытый файл не имеет права попадать в чужое число")
	})

	t.Run("запись в невидимый файл отвечает NotFound, а не PermissionDenied", func(t *testing.T) {
		alien := viewerCtx(ctx, stranger)

		// Точка 10 целиком: переименование, владельцы, превью, удаление.
		require.ErrorIs(t, s.Files().UpdateFile(alien, restricted, "переименовано-чужим", nil, nil), sql.ErrNoRows)
		require.ErrorIs(t, s.Files().SetFileOwners(alien, restricted, nil, stranger), sql.ErrNoRows)
		_, err := s.Files().SetFilePreview(alien, restricted, "files-library/hijacked-preview")
		require.ErrorIs(t, err, sql.ErrNoRows, "точка 13: перезаливка превью невидимого файла запрещена")
		_, err = s.Files().DeleteFile(alien, restricted)
		require.ErrorIs(t, err, sql.ErrNoRows)

		// Обсуждение: написать, поправить и удалить чужую закрытую переписку нельзя.
		_, err = s.Files().AddComment(alien, restricted, stranger, "я тут")
		require.ErrorIs(t, err, sql.ErrNoRows)
		_, err = s.Files().UpdateComment(alien, comment.Id, "переписано")
		require.ErrorIs(t, err, sql.ErrNoRows)
		require.ErrorIs(t, s.Files().DeleteComment(alien, comment.Id), sql.ErrNoRows)

		// Задачи: невидимый файл не прицепить.
		require.ErrorIs(t, s.Tasks().AttachFileToTask(alien, restricted, taskID), sql.ErrNoRows)

		// Заметка: CAS-запись тоже под предикатом.
		_, err = s.Files().SaveNoteContent(alien, entity.LibraryNoteSave{
			FileId: restricted, ObjectKey: "files-library/hijacked-note", Sha256: "deadbeef",
			EditedBy: stranger, Force: true,
		})
		require.ErrorIs(t, err, sql.ErrNoRows)

		// Ничего из перечисленного не состоялось: имя, реплика и превью на месте.
		stored, err := s.Files().GetFileById(admin, restricted)
		require.NoError(t, err)
		require.Equal(t, marker+"-secret.pdf", stored.FileName)
		require.False(t, stored.PreviewObjectKey.Valid)
		require.Equal(t, 1, stored.CommentsCount)
		require.Len(t, stored.Owners, 1)
	})

	t.Run("пачка отказывает целиком, если в ней хоть один невидимый id", func(t *testing.T) {
		alien := viewerCtx(ctx, stranger)
		bulkTopic := insertFileTopicFixture(ctx, t, "vis-bulk")

		// Частичное применение отвечало бы на видимый и невидимый id ПО-РАЗНОМУ — «проставилось
		// 1 из 2» и есть подтверждение, что второй файл существует.
		_, err := s.Files().AssignTopics(alien, []int{teamFile, restricted}, []int{bulkTopic}, nil)
		require.ErrorIs(t, err, sql.ErrNoRows)

		var linked int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM library_file_topic WHERE topic_id = ?`, bulkTopic).Scan(&linked))
		require.Zero(t, linked, "отказ обязан быть ПОЛНЫМ: видимая половина пачки тоже не проставилась")

		// Та же пачка без невидимого id проходит — предикат не сломал обычный путь.
		assigned, err := s.Files().AssignTopics(alien, []int{teamFile}, []int{bulkTopic}, nil)
		require.NoError(t, err)
		require.Equal(t, 1, assigned)
	})

	t.Run("тема, пустая в рельсе, не рассказывает числом о скрытом", func(t *testing.T) {
		// Рельс отдавал чужому files_count = 0, а DeleteTopic отвечал «topic still has files: 1».
		// Число скрытых файлов — ровно тот сигнал, ради устранения которого счётчики тем сделаны
		// персональными: «в этой теме есть что-то, чего тебе не показывают», сказанное числом.
		alien := viewerCtx(ctx, stranger)

		hidden := insertFileTopicFixture(ctx, t, "vis-hidden")
		linkFileTopicFixture(ctx, t, restricted, hidden)

		topics, _, _, err := s.Files().ListTopics(alien)
		require.NoError(t, err)
		var railCount = -1
		for _, tp := range topics {
			if tp.Id == hidden {
				railCount = tp.FilesCount
			}
		}
		require.Equal(t, 0, railCount, "рельс обязан показать чужому пустую тему — иначе подтест не про то")

		err = s.Files().DeleteTopic(alien, hidden)
		require.Error(t, err, "тему держит невидимая связь, и снимать её нельзя: это молча разметило бы чужой файл")
		require.NotErrorIs(t, err, entity.ErrFileTopicInUse,
			"отказ не имеет права называть число: считать надо ПОД предикатом, а остаток ловит внешний ключ, который хендлер переводит в тот же FailedPrecondition без числа")

		var alive int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM file_topic WHERE id = ?`, hidden).Scan(&alive))
		require.Equal(t, 1, alive, "тема обязана уцелеть: отказ полный")

		// Супер видит правду — и число получает.
		err = s.Files().DeleteTopic(admin, hidden)
		require.ErrorIs(t, err, entity.ErrFileTopicInUse)
		require.Contains(t, err.Error(), "1")

		// Обычный путь не сломан: пустую тему чужой удаляет.
		require.NoError(t, s.Files().DeleteTopic(alien, insertFileTopicFixture(ctx, t, "vis-empty")))
	})

	t.Run("слияние перевешивает всё, а в отчёт отдаёт видимое", func(t *testing.T) {
		// Слияние — операция над ЯРЛЫКОМ, и невидимый файл обязан переехать вместе со всеми:
		// оставленная связь уронила бы удаление темы о собственный внешний ключ. А вот число
		// читает человек, и «переехало 2» на теме, в которой он видит один файл, — тот же самый
		// сигнал числом.
		alien := viewerCtx(ctx, stranger)

		source := insertFileTopicFixture(ctx, t, "vis-merge-src")
		target := insertFileTopicFixture(ctx, t, "vis-merge-dst")
		linkFileTopicFixture(ctx, t, teamFile, source)
		linkFileTopicFixture(ctx, t, restricted, source)

		moved, err := s.Files().MergeTopics(alien, source, target)
		require.NoError(t, err)
		require.Equal(t, 1, moved, "в отчёте только видимое: чужому нельзя пересчитывать скрытое")

		// А переехали ОБА — иначе тема-источник не удалилась бы вовсе.
		var landed int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM library_file_topic WHERE topic_id = ?`, target).Scan(&landed))
		require.Equal(t, 2, landed, "перевешивается ВСЁ: ярлык не может остаться на невидимой строке")
		var sourceLeft int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM file_topic WHERE id = ?`, source).Scan(&sourceLeft))
		require.Zero(t, sourceLeft)
	})

	t.Run("невидимый файл с УДАЛЁННЫМ загрузившим отказывает пачке: `<=>`, а не `=`", func(t *testing.T) {
		// ЕДИНСТВЕННЫЙ ТЕСТ НА NULL-SAFE СРАВНЕНИЕ В ПРЕДИКАТЕ, и без него замена `<=>` на `=`
		// оставляла ВЕСЬ репозиторий зелёным.
		//
		// Почему остальная матрица этого не ловит: в обычной выдаче условие стоит в WHERE, где NULL
		// ведёт себя как ложь, — файл пропадает, и всё выглядит правильным. Но то же условие
		// используется ПОД ОТРИЦАНИЕМ (bulk-проверка AssignTopics считает НЕвидимые), а `NOT NULL` —
		// это снова NULL: строка не считается, invisible = 0, и чужой спокойно проставляет тему
		// файлу, которого не видит. Дыра открывается ровно на файлах с уволенным загрузившим, то
		// есть на самых старых, — и именно они переживают всех, кто помнил, что в них лежит.
		alien := viewerCtx(ctx, stranger)

		ghostID, ghost := insertAdminFixture(ctx, t, "test-vis-nullsafe")
		orphan := insertLibraryFileWithSha(ctx, t, marker+"-orphan.pdf", ghost, "")
		_, err := s.Files().SetFileAccess(admin, orphan, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, Actor: ghost,
		})
		require.NoError(t, err)

		_, err = testDB.ExecContext(ctx, `DELETE FROM admins WHERE id = ?`, ghostID)
		require.NoError(t, err)
		var uploadedByID sql.NullInt64
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT uploaded_by_id FROM library_file WHERE id = ?`, orphan).Scan(&uploadedByID))
		require.False(t, uploadedByID.Valid,
			"фикстура обязана дать NULL в uploaded_by_id — иначе подтест проверяет не то, что заявлено")

		// В обычной выдаче файл невидим и с `=` тоже: WHERE толкует NULL как ложь. Утверждение
		// здесь для полноты, а не как доказательство.
		_, err = s.Files().GetFileById(alien, orphan)
		require.ErrorIs(t, err, sql.ErrNoRows)

		// А ВОТ ЭТО — САМО ДОКАЗАТЕЛЬСТВО: то же условие под отрицанием.
		nullTopic := insertFileTopicFixture(ctx, t, "vis-nullsafe")
		_, err = s.Files().AssignTopics(alien, []int{orphan}, []int{nullTopic}, nil)
		require.ErrorIs(t, err, sql.ErrNoRows,
			"с `=` вместо `<=>` NOT(NULL) даёт NULL, невидимая строка не считается, и чужой проставляет тему файлу, которого не видит")

		var linked int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM library_file_topic WHERE topic_id = ?`, nullTopic).Scan(&linked))
		require.Zero(t, linked, "отказ обязан случиться ДО записи: ноль вставленных строк")
	})

	t.Run("удалённый аккаунт не передаёт видимость однофамильцу", func(t *testing.T) {
		// Дыра, уже закрытая в Ф3 и обязанная остаться закрытой: UNIQUE на admins.username
		// освобождает имя при удалении, строка uploaded_by при этом ЖИВЁТ. Опознавай предикат
		// загрузившего по имени — новый однофамилец унаследовал бы все файлы прежнего.
		ghostID, ghost := insertAdminFixture(ctx, t, "test-vis-ghost")
		ghostFile := insertLibraryFileWithSha(ctx, t, marker+"-ghost.pdf", ghost, "")
		_, err := s.Files().SetFileAccess(admin, ghostFile, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, Actor: ghost,
		})
		require.NoError(t, err)

		// Пока аккаунт жив, загрузивший файл видит.
		_, err = s.Files().GetFileById(viewerCtx(ctx, ghost), ghostFile)
		require.NoError(t, err)

		_, err = testDB.ExecContext(ctx, `DELETE FROM admins WHERE id = ?`, ghostID)
		require.NoError(t, err)
		// Живая ссылка обнулилась, строка имени осталась.
		var uploadedBy string
		var uploadedByID sql.NullInt64
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT uploaded_by, uploaded_by_id FROM library_file WHERE id = ?`, ghostFile).
			Scan(&uploadedBy, &uploadedByID))
		require.Equal(t, ghost, uploadedBy)
		require.False(t, uploadedByID.Valid)

		// Заводим НОВЫЙ аккаунт с тем же именем — и он не должен унаследовать ничего.
		res, err := testDB.ExecContext(ctx,
			`INSERT INTO admins (username, password_hash) VALUES (?, 'x')`, ghost)
		require.NoError(t, err)
		newID, err := res.LastInsertId()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM admins WHERE id = ?`, newID) })

		_, err = s.Files().GetFileById(viewerCtx(ctx, ghost), ghostFile)
		require.ErrorIs(t, err, sql.ErrNoRows,
			"опознание загрузившего обязано идти по ЖИВОЙ ссылке, а не по строке имени")
	})

	t.Run("возврат на team возвращает файл всем, а строка доступа переживает уровень", func(t *testing.T) {
		alien := viewerCtx(ctx, stranger)

		_, err := s.Files().SetFileAccess(admin, restricted, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessTeam, Actor: uploader,
		})
		require.NoError(t, err)

		back, err := s.Files().GetFileById(alien, restricted)
		require.NoError(t, err)
		require.Equal(t, entity.LibraryFileAccessTeam, back.AccessLevel)

		// Запись в поимённом списке НЕ УДАЛЕНА возвратом на team — поэтому предикат обязан
		// смотреть на access_level самой строки файла, а не на наличие записи о доступе.
		var kept int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM library_file_access_people WHERE file_id = ?`, restricted).Scan(&kept))
		require.Equal(t, 1, kept)

		// `link` внутри команды виден как `team`: уровень открывает наружу, а не закрывает внутрь.
		_, err = s.Files().SetFileAccess(admin, restricted, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessLink, LinkTTLHours: 24, Actor: uploader,
		})
		require.NoError(t, err)
		open, err := s.Files().GetFileById(alien, restricted)
		require.NoError(t, err)
		require.Equal(t, entity.LibraryFileAccessLink, open.AccessLevel)

		// Возвращаем закрытый уровень: следующие подтесты (если появятся) не должны зависеть от
		// порядка, а фикстура обязана остаться той, что описана в шапке.
		_, err = s.Files().SetFileAccess(admin, restricted, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, AdminIDs: []int{listedID}, Actor: uploader,
		})
		require.NoError(t, err)
	})
}
