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

// ФИЛЬТР «ФАЙЛЫ КОНКРЕТНОГО ЧЕЛОВЕКА» — ПРИЁМКА.
//
// У файла ДВЕ РАЗНЫЕ роли человека, и весь смысл фильтра в том, что они не схлопнуты:
//
//	ЗАГРУЗИЛ — исторический факт. Строка `uploaded_by` живёт РЯДОМ с живой ссылкой
//	           `uploaded_by_id`, потому что имя освобождается при удалении аккаунта
//	           (UNIQUE на admins.username) и достаётся следующему однофамильцу.
//	ВЕДЁТ    — текущая ответственность, `library_file_owner`.
//
// Тест обязан быть контейнерным: каждое его утверждение — утверждение о СГЕНЕРИРОВАННОМ SQL и о
// правилах удаления в схеме (ON DELETE SET NULL у ссылки загрузившего, CASCADE у владения). Ни
// одно из них не наблюдаемо в Go: и «по имени», и «по id» — рабочие запросы, возвращающие строки,
// и неправильный не выглядит сломанным — он просто молча отвечает на другой вопрос.
//
// ЧТО ЗДЕСЬ СТОРОЖИТСЯ (и чем доказано, что сторожится):
//
//  1. три роли × три значения фильтра — плечо владельца и плечо загрузившего по отдельности;
//  2. total считается ТЕМ ЖЕ условием, что и страница, иначе «N из M» разъезжается;
//  3. УДАЛЁННЫЙ аккаунт не передаёт свои файлы однофамильцу — единственная проверка, которая
//     краснеет на подмене живого id строкой имени;
//  4. фильтр СКЛАДЫВАЕТСЯ с темами и стоит ПОД предикатом видимости: закрытый файл не
//     появляется в выдаче фильтра по человеку даже у того, кто спрашивает про его владельца.
func TestLibraryFilesPersonFilter(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	admin := superCtx(ctx)

	pashaID, pasha := insertAdminFixture(ctx, t, "test-pf-pasha")
	kirillID, kirill := insertAdminFixture(ctx, t, "test-pf-kirill")
	_, stranger := insertAdminFixture(ctx, t, "test-pf-stranger")

	// Маркер-тема делает выборку СВОЕЙ: сетка идёт по всей библиотеке, а в контейнере лежат
	// файлы соседних тестов. Заодно это и есть проверка сложения с фильтром тем — фильтр по
	// человеку обязан пересекаться с ним, а не заменять его.
	marker := insertFileTopicFixture(ctx, t, "test-pf-marker")
	sideTopic := insertFileTopicFixture(ctx, t, "test-pf-side")

	sha := fmt.Sprintf("%064d", time.Now().UnixNano()%1e10)

	// ТРИ РОЛИ ПАШИ ПЛЮС КОНТРОЛЬ. Без контрольного файла «фильтр вернул три строки» доказывало
	// бы ровно ничего: тот же ответ даёт и фильтр, не отфильтровавший вообще ничего.
	uploadedOnly := insertLibraryFileWithSha(ctx, t, "pf-uploaded-only.pdf", pasha, sha)
	ownedOnly := insertLibraryFileWithSha(ctx, t, "pf-owned-only.pdf", kirill, sha)
	both := insertLibraryFileWithSha(ctx, t, "pf-both.pdf", pasha, sha)
	neither := insertLibraryFileWithSha(ctx, t, "pf-neither.pdf", kirill, sha)

	linkFileTopicFixture(ctx, t, uploadedOnly, marker)
	linkFileTopicFixture(ctx, t, ownedOnly, marker)
	linkFileTopicFixture(ctx, t, both, marker, sideTopic)
	linkFileTopicFixture(ctx, t, neither, marker)

	require.NoError(t, s.Files().SetFileOwners(admin, ownedOnly, []int{pashaID}, kirill))
	require.NoError(t, s.Files().SetFileOwners(admin, both, []int{pashaID}, pasha))
	require.NoError(t, s.Files().SetFileOwners(admin, neither, []int{kirillID}, kirill))

	// list возвращает И страницу, И счётчик: они снимаются одним вызовом и сравниваются с ОДНИМ
	// ожиданием — расхождение «N из M» иначе прошло бы мимо любого утверждения о составе.
	list := func(ctx context.Context, t *testing.T, f entity.LibraryFileListFilter) ([]int, int) {
		t.Helper()
		if len(f.TopicIds) == 0 {
			f.TopicIds = []int{marker}
		}
		if f.Limit == 0 {
			f.Limit = 1000
		}
		files, total, err := s.Files().ListFiles(ctx, f)
		require.NoError(t, err)
		return libraryFileIDs(files), total
	}

	// requireSet сверяет состав И счётчик одним утверждением. Счётчик — не довесок: страница и
	// total обязаны описывать одну выборку, и это тот случай, который уже ловили на витрине.
	requireSet := func(t *testing.T, want []int, ids []int, total int, msg string) {
		t.Helper()
		require.ElementsMatch(t, want, ids, msg)
		require.Equal(t, len(want), total,
			"total обязан считаться ТЕМ ЖЕ условием, что и страница: иначе «N из M» врёт, и человек листает за числом, которого нет")
	}

	t.Run("матрица: три роли × три значения фильтра", func(t *testing.T) {
		cases := []struct {
			name string
			role entity.LibraryFilePersonRole
			want []int
		}{
			{
				// «Любая» = ЗАГРУЗИЛ ИЛИ ВЕДЁТ. Убери плечо владельца — и ownedOnly выпадет
				// ровно здесь, а два других значения фильтра останутся зелёными.
				name: "любая роль — где он числится вообще",
				role: entity.LibraryFilePersonRoleAny,
				want: []int{uploadedOnly, ownedOnly, both},
			},
			{
				name: "только загрузил",
				role: entity.LibraryFilePersonRoleUploaded,
				want: []int{uploadedOnly, both},
			},
			{
				name: "только ведёт",
				role: entity.LibraryFilePersonRoleOwner,
				want: []int{ownedOnly, both},
			},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				ids, total := list(admin, t, entity.LibraryFileListFilter{
					PersonId: pashaID, PersonRole: c.role,
				})
				requireSet(t, c.want, ids, total, "фильтр по человеку обязан отвечать ровно на свою роль")
				require.NotContains(t, ids, neither,
					"контрольный файл, к которому человек не имеет отношения ни в одной роли, не имеет права попадать в выдачу")
			})
		}
	})

	t.Run("владельцев несколько — строка файла не размножается", func(t *testing.T) {
		// EXISTS, а не JOIN. Соединение с library_file_owner дало бы по строке на каждого
		// совпавшего владельца: страница отдала бы дубли, а total их сосчитал бы.
		require.NoError(t, s.Files().SetFileOwners(admin, both, []int{pashaID, kirillID}, pasha))
		t.Cleanup(func() { _ = s.Files().SetFileOwners(admin, both, []int{pashaID}, pasha) })

		ids, total := list(admin, t, entity.LibraryFileListFilter{
			PersonId: pashaID, PersonRole: entity.LibraryFilePersonRoleOwner,
		})
		requireSet(t, []int{ownedOnly, both}, ids, total, "второй владелец не имеет права удваивать строку файла")
	})

	t.Run("несуществующий человек — пустая выдача, а не ошибка", func(t *testing.T) {
		// Отказ здесь был бы ОРАКУЛОМ: перебирая id и различая «не найден» от «ничего не
		// нашлось», можно пересчитать аккаунты, ни разу не имея права читать admins.
		ids, total := list(admin, t, entity.LibraryFileListFilter{PersonId: 2147483000})
		require.Empty(t, ids)
		require.Zero(t, total)
	})

	t.Run("неположительный id — отсутствие фильтра, а не «никто»", func(t *testing.T) {
		ids, total := list(admin, t, entity.LibraryFileListFilter{PersonId: 0})
		requireSet(t, []int{uploadedOnly, ownedOnly, both, neither}, ids, total,
			"нулевой id обязан означать «фильтра нет»: контрол необязателен, и 0 из url'а не повод показать пустую библиотеку")

		ids, total = list(admin, t, entity.LibraryFileListFilter{
			PersonId: -1, PersonRole: entity.LibraryFilePersonRoleOwner,
		})
		requireSet(t, []int{uploadedOnly, ownedOnly, both, neither}, ids, total,
			"отрицательный id — то же самое: роль без человека одна ничего не сужает")
	})

	t.Run("складывается с фильтром тем пересечением", func(t *testing.T) {
		// Обе стороны обязаны сужать: фильтр по человеку не заменяет тему, а тема не отменяет
		// человека. UNION по ролям или отдельный проход дали бы здесь uploadedOnly.
		ids, total := list(admin, t, entity.LibraryFileListFilter{
			PersonId: pashaID,
			TopicIds: []int{marker, sideTopic},
		})
		requireSet(t, []int{both}, ids, total, "тема обязана сужать выдачу фильтра по человеку")

		// И наоборот: тема без человека шире.
		ids, total = list(admin, t, entity.LibraryFileListFilter{TopicIds: []int{marker, sideTopic}})
		requireSet(t, []int{both}, ids, total, "фикстура: sideTopic несёт ровно один файл")

		// «Разобрать» и фильтр по человеку тоже пересекаются, а не спорят.
		untopiced, _ := list(admin, t, entity.LibraryFileListFilter{
			PersonId: pashaID, Untopiced: true, TopicIds: []int{marker},
		})
		require.Empty(t, untopiced,
			"все файлы фикстуры несут маркер, значит в «Разобрать» у Паши пусто — фильтр по человеку не имеет права это переопределять")
	})

	t.Run("поиск и фильтр по человеку сужают вместе", func(t *testing.T) {
		ids, total := list(admin, t, entity.LibraryFileListFilter{
			PersonId: pashaID, Search: "pf-owned-only",
		})
		requireSet(t, []int{ownedOnly}, ids, total,
			"строковый поиск и выбор аккаунта — разные вопросы, и они обязаны складываться")
	})

	t.Run("удалённый аккаунт не передаёт свои файлы однофамильцу", func(t *testing.T) {
		// ЕДИНСТВЕННАЯ ПРОВЕРКА, КОТОРАЯ КРАСНЕЕТ НА ПОДМЕНЕ ЖИВОГО id СТРОКОЙ ИМЕНИ, и ради
		// неё всё остальное написано. Дыра закрывалась в волне уже дважды (Ф3 и плечо 2
		// предиката видимости), и открыть её заново стоит одной «безобидной» строки вида
		// `lf.uploaded_by = (SELECT username FROM admins WHERE id = :personId)`: такой запрос
		// возвращает строки, ничем не выглядит сломанным — и отдаёт новому сотруднику ВСЮ
		// историю уволенного однофамильца.
		ghostID, ghost := insertAdminFixture(ctx, t, "test-pf-ghost")
		ghostFile := insertLibraryFileWithSha(ctx, t, "pf-ghost.pdf", ghost, sha)
		linkFileTopicFixture(ctx, t, ghostFile, marker)

		// Пока аккаунт жив, фильтр его файл находит — иначе подтест проверял бы не то.
		ids, _ := list(admin, t, entity.LibraryFileListFilter{PersonId: ghostID})
		require.Contains(t, ids, ghostFile, "фикстура: у живого аккаунта фильтр обязан работать")

		_, err := testDB.ExecContext(ctx, `DELETE FROM admins WHERE id = ?`, ghostID)
		require.NoError(t, err)

		// Живая ссылка обнулилась, историческая строка осталась — на этом и держится подмена.
		var uploadedBy string
		var uploadedByID sql.NullInt64
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT uploaded_by, uploaded_by_id FROM library_file WHERE id = ?`, ghostFile).
			Scan(&uploadedBy, &uploadedByID))
		require.Equal(t, ghost, uploadedBy)
		require.False(t, uploadedByID.Valid,
			"фикстура обязана дать NULL в uploaded_by_id — иначе подтест проверяет не то, что заявлено")

		// НОВЫЙ аккаунт с ТЕМ ЖЕ именем: UNIQUE освободился вместе с удалением.
		res, err := testDB.ExecContext(ctx,
			`INSERT INTO admins (username, password_hash) VALUES (?, 'x')`, ghost)
		require.NoError(t, err)
		newID64, err := res.LastInsertId()
		require.NoError(t, err)
		newID := int(newID64)
		t.Cleanup(func() { _, _ = testDB.Exec(`DELETE FROM admins WHERE id = ?`, newID) })
		require.NotEqual(t, ghostID, newID)

		// Свой собственный файл однофамилец видеть обязан — иначе «фильтр вернул пусто»
		// доказывало бы только, что он сломан.
		ownFile := insertLibraryFileWithSha(ctx, t, "pf-namesake-own.pdf", ghost, sha)
		linkFileTopicFixture(ctx, t, ownFile, marker)

		for _, role := range []entity.LibraryFilePersonRole{
			entity.LibraryFilePersonRoleAny,
			entity.LibraryFilePersonRoleUploaded,
		} {
			ids, total := list(admin, t, entity.LibraryFileListFilter{PersonId: newID, PersonRole: role})
			requireSet(t, []int{ownFile}, ids, total,
				"опознание загрузившего обязано идти по ЖИВОЙ ссылке: по строке имени однофамилец унаследовал бы всю историю уволенного")
		}

		// А файл уволенного не находится теперь НИКЕМ — и это правильный ответ: живого
		// человека за ним больше нет, а имя ему больше не принадлежит.
		ids, _ = list(admin, t, entity.LibraryFileListFilter{PersonId: ghostID})
		require.NotContains(t, ids, ghostFile)
	})

	t.Run("закрытый файл не появляется в выдаче фильтра по человеку", func(t *testing.T) {
		// Фильтр стоит ПОД предикатом видимости, а не рядом с ним. Иначе «покажи файлы Паши»
		// стало бы обходом: имя файла в этой библиотеке говорящее, и утечка здесь — это утечка
		// имени, которую отказ на открытии уже не закрывает.
		secret := insertLibraryFileWithSha(ctx, t, "pf-secret.pdf", pasha, sha)
		linkFileTopicFixture(ctx, t, secret, marker)
		require.NoError(t, s.Files().SetFileOwners(admin, secret, []int{pashaID}, pasha))
		_, err := s.Files().SetFileAccess(admin, secret, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, Actor: pasha,
		})
		require.NoError(t, err)

		alien := viewerCtx(ctx, stranger)
		for _, role := range []entity.LibraryFilePersonRole{
			entity.LibraryFilePersonRoleAny,
			entity.LibraryFilePersonRoleUploaded,
			entity.LibraryFilePersonRoleOwner,
		} {
			ids, total := list(alien, t, entity.LibraryFileListFilter{PersonId: pashaID, PersonRole: role})
			require.NotContains(t, ids, secret,
				"фильтр по человеку не имеет права быть обходом предиката видимости")
			require.NotEmpty(t, ids,
				"открытые файлы того же человека обязаны остаться — предикат режет закрытое, а не фильтр целиком")
			require.Equal(t, len(ids), total,
				"счёт чужому обязан идти под тем же предикатом, что и страница: иначе число само рассказало бы, что от него что-то закрыли")
		}

		// Тот же вопрос от того, кому файл виден, находит его — иначе «не появился» ничего бы
		// не значило.
		for _, seer := range []struct {
			name string
			ctx  context.Context
		}{
			{"супер", admin},
			{"сам загрузивший", viewerCtx(ctx, pasha)},
		} {
			ids, _ := list(seer.ctx, t, entity.LibraryFileListFilter{PersonId: pashaID})
			require.Contains(t, ids, secret, "тому, кто файл видит, фильтр обязан его отдать: "+seer.name)
		}
	})
}
