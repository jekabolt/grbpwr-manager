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

// ДОСТУП К ФАЙЛУ (Ф7, миграция 0317) — контейнерный тест.
//
// Он обязан быть интеграционным целиком: каждое утверждение здесь — утверждение про SQL и про
// правила удаления схемы (CASCADE журнала и списков, ON DELETE SET NULL у актора), и ни одно из
// них не наблюдаемо в Go. Проверка «событие записалось» на моке доказывала бы, что мы вызвали
// свою же функцию.
//
// ВЕСЬ ТЕСТ ИДЁТ ПОД СУПЕРОМ, И ЭТО НЕ ПОБЛАЖКА. С приходом предиката (T-7.3) файл уровня
// `people` пропадает у того, кто его не видит, а голый контекст без JWT — как раз такой
// смотрящий: витрина не показала бы ограниченный файл, а GetFileAccess ответил бы NotFound. Здесь
// проверяется МЕХАНИКА доступа (журнал, поколение ссылки, каскады), и смешивать её с вопросом
// «кому видно» значило бы иметь два теста, каждый из которых красный по чужой причине. Вопрос
// «кому видно» целиком принадлежит матрице T-7.9 (files_visibility_matrix_integration_test.go).
func TestLibraryFileAccess(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	ctx = superCtx(ctx)

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	pashaID, pasha := insertAdminFixture(ctx, t, "test-access-pasha")
	kirillID, kirill := insertAdminFixture(ctx, t, "test-access-kirill")
	_ = pashaID

	t.Run("a fresh file is team, with no people and no link", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "fresh.pdf", 10, pasha)

		access, err := s.Files().GetFileAccess(ctx, fileID)
		require.NoError(t, err)
		require.Equal(t, entity.LibraryFileAccessTeam, access.Level)
		require.Empty(t, access.People)
		require.Nil(t, access.Link, "a file nobody shared by link must have no link row at all")

		// Файла нет — NotFound, а не пустое состояние: карточка на удалённом файле обязана
		// сказать об этом, а не показать «доступ у всей команды».
		_, err = s.Files().GetFileAccess(ctx, 2147483600)
		require.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("a level change writes exactly one machine-readable journal line", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "levels.pdf", 10, pasha)

		access, err := s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level:    entity.LibraryFileAccessPeople,
			AdminIDs: []int{kirillID},
			Actor:    pasha,
		})
		require.NoError(t, err)
		require.Equal(t, entity.LibraryFileAccessPeople, access.Level)
		require.Len(t, access.People, 1)
		require.Equal(t, kirill, access.People[0].Username)

		events, err := s.Files().ListFileAccessEvents(ctx, fileID, 0)
		require.NoError(t, err)
		require.Len(t, events, 2, "one line for the level, one for the person added")
		// Журнал новейшим сверху: ответ на «кто это открыл» почти всегда в последней строке.
		require.Equal(t, "+ "+kirill, events[0].What)
		require.True(t, strings.HasPrefix(events[1].What, entity.LibraryFileAccessLevelEventPrefix+"people"),
			"the level event must carry the machine prefix the витрина matches on, got %q", events[1].What)
		// Обе половины авторства: строка-факт и живая ссылка, выведенная стором из неё.
		require.Equal(t, pasha, events[1].Actor)
		require.True(t, events[1].ActorId.Valid)

		// Повтор того же уровня и того же списка — НЕ событие: журнал, полный «сохранил то же
		// самое», перестают читать.
		_, err = s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level:    entity.LibraryFileAccessPeople,
			AdminIDs: []int{kirillID},
			Actor:    pasha,
		})
		require.NoError(t, err)
		events, err = s.Files().ListFileAccessEvents(ctx, fileID, 0)
		require.NoError(t, err)
		require.Len(t, events, 2)

		// Список ЗАМЕЩАЕТСЯ, а уход человека попадает в журнал.
		_, err = s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level:    entity.LibraryFileAccessPeople,
			AdminIDs: []int{pashaID},
			Actor:    kirill,
		})
		require.NoError(t, err)
		events, err = s.Files().ListFileAccessEvents(ctx, fileID, 0)
		require.NoError(t, err)
		require.Equal(t, "- "+kirill, events[0].What)
		require.Equal(t, "+ "+pasha, events[1].What)
	})

	t.Run("the people list survives a trip through team and back", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "roundtrip.pdf", 10, pasha)

		_, err := s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, AdminIDs: []int{kirillID}, Actor: pasha,
		})
		require.NoError(t, err)

		back, err := s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessTeam, Actor: pasha,
		})
		require.NoError(t, err)
		require.Equal(t, entity.LibraryFileAccessTeam, back.Level)
		// СПИСОК НЕ СТИРАЕТСЯ: «показать всем на минуту» не должно заставлять набирать его заново.
		require.Len(t, back.People, 1)
		require.Equal(t, kirill, back.People[0].Username)
	})

	t.Run("link: the row is created, the ttl lands, and the row outlives the level", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "shared.pdf", 10, pasha)

		access, err := s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessLink, LinkTTLHours: 168, Actor: pasha,
		})
		require.NoError(t, err)
		require.NotNil(t, access.Link)
		require.Equal(t, 1, access.Link.Epoch, "the first link of a file starts at epoch 1")
		require.True(t, access.Link.ExpiresAt.Valid)
		require.WithinDuration(t, time.Now().UTC().Add(168*time.Hour), access.Link.ExpiresAt.Time, 5*time.Minute)
		require.False(t, access.Link.RevokedAt.Valid)

		// Бессрочно = NULL, а не «очень далеко».
		access, err = s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessLink, Actor: pasha,
		})
		require.NoError(t, err)
		require.False(t, access.Link.ExpiresAt.Valid)

		// Возврат в team НЕ удаляет строку (счётчик и история остаются) и НЕ двигает поколение
		// сам по себе — это и есть то, из-за чего маршрут обязан сверять уровень на строке файла,
		// а не наличие строки доступа. Но ссылка ВЫКЛЮЧАЕТСЯ, и это записано.
		team, err := s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessTeam, Actor: pasha,
		})
		require.NoError(t, err)
		require.NotNil(t, team.Link)
		require.Equal(t, 1, team.Link.Epoch)
		require.True(t, team.Link.RevokedAt.Valid,
			"уход с link обязан штамповать revoked_at: иначе колонка не пишется нигде, `revoked` на проводе вечно false, а ветка отзыва в маршруте — мёртвый код")

		// А узкое чтение публичного маршрута видит и уровень, и поколение — и отвечает на
		// вопрос «жива ли ссылка» само.
		target, err := s.Files().GetFileByPublicLink(ctx, fileID)
		require.NoError(t, err)
		require.Equal(t, entity.LibraryFileAccessTeam, target.AccessLevel)
		require.Equal(t, 1, target.Epoch)
		require.NotEmpty(t, target.ObjectKey, "the presigner's key must come from this row and nowhere else")
	})

	t.Run("возврат на link выдаёт НОВУЮ ссылку, а правка срока — ту же", func(t *testing.T) {
		// Эпоха не двигалась ни при уходе с уровня, ни при возврате, поэтому ссылка бывшего
		// подрядчика оживала ровно в ту минуту, когда файл снова открывали по ссылке — другим
		// людям и обычно много позже. «Вернули уровень» никто не читает как «снова раздали ту же
		// ссылку», а в журнале об этом стояла одна строка `level:link`, из которой такого вывода
		// не сделать.
		fileID := insertLibraryFileFixture(ctx, t, "reissue.pdf", 10, pasha)

		first, err := s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessLink, LinkTTLHours: 24, Actor: pasha,
		})
		require.NoError(t, err)
		require.Equal(t, 1, first.Link.Epoch, "первая ссылка файла начинается с поколения 1")
		require.False(t, first.Link.RevokedAt.Valid)

		// ПРАВКА ОДНОГО ЛИШЬ СРОКА НА ЖИВОМ УРОВНЕ ССЫЛКУ НЕ УБИВАЕТ: иначе смена чипа гасила бы
		// ссылку, которую только что разослали.
		sameLink, err := s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessLink, LinkTTLHours: 168, Actor: pasha,
		})
		require.NoError(t, err)
		require.Equal(t, 1, sameLink.Link.Epoch)

		// Ушли с уровня — ссылка выключена и это в журнале.
		_, err = s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, AdminIDs: []int{kirillID}, Actor: pasha,
		})
		require.NoError(t, err)
		events, err := s.Files().ListFileAccessEvents(ctx, fileID, 0)
		require.NoError(t, err)
		require.Contains(t, journalTexts(events), "ссылка выключена, прежняя больше не работает")

		// «Пересоздать» на файле не по ссылке двигает поколение, но НЕ включает ссылку обратно:
		// снятый здесь штамп означал бы `revoked = false` на файле, у которого ссылка мертва по
		// уровню.
		rotated, err := s.Files().RotateFileLink(ctx, fileID, pasha)
		require.NoError(t, err)
		require.Equal(t, 2, rotated.Epoch)
		require.True(t, rotated.RevokedAt.Valid, "отзыв снимает только возврат на уровень")

		// Вернулись — НОВОЕ поколение, снятый отзыв и отдельная строка журнала, которая это
		// называет: сама по себе `level:link` о смерти прежнего адреса не говорит ничего.
		reissued, err := s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessLink, LinkTTLHours: 24, Actor: kirill,
		})
		require.NoError(t, err)
		require.Equal(t, 3, reissued.Link.Epoch,
			"возврат на link обязан выдать новый токен: прежняя ссылка ушла к людям, которых сегодня уже не звали (2 — от «пересоздать» выше, 3 — от возврата)")
		require.False(t, reissued.Link.RevokedAt.Valid)
		events, err = s.Files().ListFileAccessEvents(ctx, fileID, 0)
		require.NoError(t, err)
		require.Contains(t, journalTexts(events), "ссылка выдана заново, прежняя больше не работает")
	})

	t.Run("rotation bumps the epoch, and does it even for a file that never had a link", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "rotated.pdf", 10, pasha)

		first, err := s.Files().RotateFileLink(ctx, fileID, pasha)
		require.NoError(t, err)
		require.Equal(t, 1, first.Epoch, "rotating a file with no link means «выдай ссылку»")

		second, err := s.Files().RotateFileLink(ctx, fileID, pasha)
		require.NoError(t, err)
		require.Equal(t, 2, second.Epoch, "every rotation must out-generation every token minted so far")

		events, err := s.Files().ListFileAccessEvents(ctx, fileID, 0)
		require.NoError(t, err)
		require.Len(t, events, 2)
		require.Contains(t, events[0].What, "пересоздана")

		// Файла нет — NotFound, а не молча заведённая строка в никуда.
		_, err = s.Files().RotateFileLink(ctx, 2147483600, pasha)
		require.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("the витрина lists what is open and says who opened it", func(t *testing.T) {
		teamFile := insertLibraryFileFixture(ctx, t, "not-shared.pdf", 10, pasha)
		peopleFile := insertLibraryFileFixture(ctx, t, "by-list.pdf", 10, pasha)
		linkFile := insertLibraryFileFixture(ctx, t, "by-link.pdf", 10, pasha)

		_, err := s.Files().SetFileAccess(ctx, peopleFile, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, AdminIDs: []int{kirillID}, Actor: pasha,
		})
		require.NoError(t, err)
		// Открыл ссылкой ОДИН человек, а последним трогал другой — колонка отвечает за первое.
		_, err = s.Files().SetFileAccess(ctx, linkFile, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessLink, LinkTTLHours: 24, Actor: kirill,
		})
		require.NoError(t, err)
		_, err = s.Files().RotateFileLink(ctx, linkFile, pasha)
		require.NoError(t, err)

		rows, total, err := s.Files().ListSharedFiles(ctx, entity.SharedLibraryFileFilter{})
		require.NoError(t, err)
		require.GreaterOrEqual(t, total, 2)
		byID := map[int]entity.SharedLibraryFile{}
		for _, r := range rows {
			byID[r.File.Id] = r
		}
		require.NotContains(t, byID, teamFile, "the витрина is the list of what is NOT the default")

		p := byID[peopleFile]
		require.Equal(t, entity.LibraryFileAccessPeople, p.File.AccessLevel)
		require.Len(t, p.People, 1)
		require.Equal(t, kirill, p.People[0].Username)
		require.Equal(t, pasha, p.SharedBy)
		require.True(t, p.SharedAt.Valid)
		require.Nil(t, p.Link)

		l := byID[linkFile]
		require.Empty(t, l.People, "at level link the truthful answer is «кто угодно со ссылкой»")
		require.NotNil(t, l.Link)
		require.Equal(t, 2, l.Link.Epoch)
		require.Equal(t, kirill, l.SharedBy,
			"«кто открыл» is the actor of the last event that ESTABLISHED the current level, not the last toucher")

		// Фильтр по уровню сужает и выдачу, и счёт — иначе «3 из 40» не значило бы ни одного
		// из двух чисел.
		only, onlyTotal, err := s.Files().ListSharedFiles(ctx, entity.SharedLibraryFileFilter{
			Level: entity.LibraryFileAccessLink,
		})
		require.NoError(t, err)
		require.Equal(t, len(only), min(onlyTotal, len(only)))
		for _, r := range only {
			require.Equal(t, entity.LibraryFileAccessLink, r.File.AccessLevel)
		}

		// `team` — не фильтр витрины: это её отрицание.
		_, _, err = s.Files().ListSharedFiles(ctx, entity.SharedLibraryFileFilter{Level: entity.LibraryFileAccessTeam})
		require.Error(t, err)

		// Возврат файла в team убирает его из витрины целиком.
		_, err = s.Files().SetFileAccess(ctx, peopleFile, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessTeam, Actor: pasha,
		})
		require.NoError(t, err)
		rows, _, err = s.Files().ListSharedFiles(ctx, entity.SharedLibraryFileFilter{})
		require.NoError(t, err)
		for _, r := range rows {
			require.NotEqual(t, peopleFile, r.File.Id)
		}
	})

	t.Run("public access statistics fold in as a batch", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "counted.pdf", 10, pasha)
		_, err := s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessLink, Actor: pasha,
		})
		require.NoError(t, err)

		now := time.Now().UTC()
		require.NoError(t, s.Files().RecordPublicAccess(ctx,
			map[int]int64{fileID: 3}, map[int]time.Time{fileID: now}))
		// Вторая пачка СКЛАДЫВАЕТСЯ, а не замещает: это счётчик, а не снимок.
		require.NoError(t, s.Files().RecordPublicAccess(ctx, map[int]int64{fileID: 2}, nil))

		access, err := s.Files().GetFileAccess(ctx, fileID)
		require.NoError(t, err)
		require.EqualValues(t, 5, access.Link.AccessCount)
		require.True(t, access.Link.LastAccessAt.Valid,
			"a batch without a timestamp must still not wipe the one already stored")
	})

	t.Run("everything cascades with the file, and the journal survives its actor", func(t *testing.T) {
		leaverID, leaver := insertAdminFixture(ctx, t, "test-access-leaver")
		fileID := insertLibraryFileFixture(ctx, t, "cascade.pdf", 10, pasha)

		_, err := s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, AdminIDs: []int{leaverID}, Actor: leaver,
		})
		require.NoError(t, err)

		_, err = testDB.ExecContext(ctx, `DELETE FROM admins WHERE id = ?`, leaverID)
		require.NoError(t, err)

		events, err := s.Files().ListFileAccessEvents(ctx, fileID, 0)
		require.NoError(t, err)
		require.NotEmpty(t, events)
		// Строка actor ПЕРЕЖИВАЕТ аккаунт (журнал отвечает про файл, который живёт дольше
		// своих людей), а живая ссылка обнуляется вместе с ним.
		require.Equal(t, leaver, events[0].Actor)
		require.False(t, events[0].ActorId.Valid)
		// Связь «кому открыт» — это отношение, и она умирает вместе с человеком.
		access, err := s.Files().GetFileAccess(ctx, fileID)
		require.NoError(t, err)
		require.Empty(t, access.People)

		_, err = testDB.ExecContext(ctx, `DELETE FROM library_file WHERE id = ?`, fileID)
		require.NoError(t, err)
		for _, table := range []string{
			"library_file_access_people", "library_file_public_access", "library_file_access_event",
		} {
			var left int
			require.NoError(t, testDB.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM `+table+` WHERE file_id = ?`, fileID).Scan(&left))
			require.Zero(t, left, "%s must cascade with the file it describes", table)
		}
	})

	t.Run("an unknown level is refused and a non-existent account fails loudly", func(t *testing.T) {
		fileID := insertLibraryFileFixture(ctx, t, "guards.pdf", 10, pasha)

		_, err := s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessLevel("peoples"), Actor: pasha,
		})
		require.Error(t, err)

		// «доступ выдан» человеку, которого нет, — худший из ответов: в списке он будет, а
		// увидеть файл некому.
		_, err = s.Files().SetFileAccess(ctx, fileID, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, AdminIDs: []int{2147483600}, Actor: pasha,
		})
		require.Error(t, err)

		// Отказ ПОЛНЫЙ: уровень не переехал, потому что транзакция откатилась целиком.
		access, err := s.Files().GetFileAccess(ctx, fileID)
		require.NoError(t, err)
		require.Equal(t, entity.LibraryFileAccessTeam, access.Level)
	})
}

// journalTexts достаёт тексты строк журнала — утверждать «такая строка есть» надёжнее, чем
// сверяться с индексом: между интересными событиями встают строки о сроке и о людях, и их число
// зависит от того, менялся ли чип.
func journalTexts(events []entity.LibraryFileAccessEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.What)
	}
	return out
}

// TestLibraryFileAccessMigrationIsRerunnable: MySQL автокоммитит DDL, поэтому падение в
// середине 0317 оставляет схему полуприменённой БЕЗ строки в gorp_migrations, и следующая
// загрузка запускает файл с начала. Миграция, которая этого не переживает, не «падает в тесте» —
// она останавливает старт процесса.
func TestLibraryFileAccessMigrationIsRerunnable(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	_, uploader := insertAdminFixture(ctx, t, "test-0317-rerun")
	fileID := insertLibraryFileFixture(ctx, t, "rerun.pdf", 10, uploader)
	_, err := testDB.ExecContext(ctx,
		`UPDATE library_file SET access_level = 'link' WHERE id = ?`, fileID)
	require.NoError(t, err)

	for i := range 2 {
		_, err := testDB.ExecContext(ctx,
			`DELETE FROM gorp_migrations WHERE id = '0317_file_access.sql'`)
		require.NoError(t, err)
		require.NoError(t, Migrate(testDB), "re-applying 0317 over an applied schema (pass %d)", i+1)

		var level string
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT access_level FROM library_file WHERE id = ?`, fileID).Scan(&level))
		require.Equal(t, "link", level, "a re-run must never reset a level somebody set")
	}
}
