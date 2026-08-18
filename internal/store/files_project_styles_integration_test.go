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

// ПРОЕКТ ↔ СТИЛЬ (0321) — ПРИЁМКА ОБРАТНОГО ВОПРОСА.
//
// Ф0 отвечала «что лежит в этой съёмке». Здесь проверяется обратное: человек стоит на карточке
// вещи и спрашивает, каким .zprj она сшита. Главных утверждений два, и оба непроверяемы в Go:
//
//	1. ЧИСЛО ФАЙЛОВ ПРОЕКТА, ПОКАЗАННОЕ С КАРТОЧКИ ВЕЩИ, ИДЁТ ПОД ПРЕДИКАТОМ ВИДИМОСТИ.
//	   Иначе карточка вещи становится боковым каналом: человек, которому в проекте видно два
//	   файла, читает там «7» и узнаёт, что от него что-то закрыто. Это дыра, а не неточность, и
//	   «предикат стоит на месте» — утверждение о сгенерированном SQL, а не о коде на Go.
//	2. СВЯЗЬ ЖИВЁТ РОВНО СТОЛЬКО, СКОЛЬКО ЖИВУТ ОБЕ ЕЁ СТОРОНЫ. Каскады, понижение проекта и
//	   слияние проектов — утверждения о схеме и о транзакции, наблюдаемые только на живой базе.
//
// Контейнерный прогон обязателен ещё и потому, что неправильный запрос здесь НЕ ВЫГЛЯДИТ
// сломанным: предикат, переехавший из ON внешнего соединения в WHERE, — рабочий SQL, который
// просто отвечает на другой вопрос (и выкидывает проекты с нулём файлов вместо того, чтобы
// показать в них ноль).

// insertTechCardFixture creates a style and registers its removal. Имя и артикул генерирует
// MySQL: два прогона набора по одному контейнеру иначе столкнулись бы на uniq_tech_card_style_number.
func insertTechCardFixture(ctx context.Context, t *testing.T, prefix string) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx,
		`INSERT INTO tech_card (style_number, name) VALUES (CONCAT(?, '-', UUID_SHORT()), ?)`,
		prefix, prefix)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.Exec(`DELETE FROM tech_card WHERE id = ?`, id)
	})
	return int(id)
}

func TestFilesProjectStyleLinks(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	admin := superCtx(ctx)

	_, pasha := insertAdminFixture(ctx, t, "test-ps-pasha")
	_, stranger := insertAdminFixture(ctx, t, "test-ps-stranger")

	sha := fmt.Sprintf("%064d", time.Now().UnixNano()%1e10)

	// styleIDs собирает id из ответа: подтесты утверждают СОСТАВ, и делать это по индексам
	// значило бы заодно утверждать порядок там, где он к делу не относится.
	styleIDs := func(styles []entity.FileTopicStyleRef) []int {
		out := make([]int, 0, len(styles))
		for _, st := range styles {
			out = append(out, st.TechCardId)
		}
		return out
	}
	projectIDs := func(links []entity.StyleProjectLink) []int {
		out := make([]int, 0, len(links))
		for _, l := range links {
			out = append(out, l.Id)
		}
		return out
	}

	t.Run("связь ставится, снимается и повторяется без отказа", func(t *testing.T) {
		// ПОВТОР — NO-OP, А НЕ 1062. Кнопка «привязать» живёт на двух экранах сразу, и второй
		// человек, нажавший её на уже привязанном стиле, получил ровно то, чего хотел. Отказ по
		// уникальному ключу сообщил бы об ошибке там, где ошибки нет.
		project := insertProjectTopicFixture(ctx, t, s, "test-ps-shoot")
		style := insertTechCardFixture(ctx, t, "test-ps-style")

		require.NoError(t, s.Files().LinkTopicStyle(admin, project, style))
		require.NoError(t, s.Files().LinkTopicStyle(admin, project, style),
			"повторная привязка обязана быть no-op: UNIQUE гасится INSERT IGNORE и не доезжает до отказа")

		rows, err := storeScalarInt(ctx,
			`SELECT COUNT(*) FROM file_topic_tech_card WHERE topic_id = ? AND tech_card_id = ?`,
			project, style)
		require.NoError(t, err)
		require.Equal(t, 1, rows, "и обязана оставить РОВНО ОДНУ строку, а не вторую такую же")

		styles, err := s.Files().ListTopicStyles(admin, project)
		require.NoError(t, err)
		require.Equal(t, []int{style}, styleIDs(styles))
		require.NotEmpty(t, styles[0].StyleNumber, "артикул обязан приезжать: им вещь и опознают")
		require.NotEmpty(t, styles[0].Name)
		require.False(t, styles[0].LinkedAt.IsZero(), "«привязано» печатается на карточке вещи")

		require.NoError(t, s.Files().UnlinkTopicStyle(admin, project, style))
		require.NoError(t, s.Files().UnlinkTopicStyle(admin, project, style),
			"повторная отвязка тоже no-op: связи нет, и просили именно этого")

		styles, err = s.Files().ListTopicStyles(admin, project)
		require.NoError(t, err)
		require.Empty(t, styles)
	})

	t.Run("связь с обычной темой отклоняется внятной ошибкой", func(t *testing.T) {
		// НЕ отказом внешнего ключа: FK здесь и не сработал бы — тема существует, просто она
		// ярлык, — а там, где он срабатывает, он отвечает номером ключа вместо фразы. Хендлер
		// переводит эту ошибку в InvalidArgument.
		plain := insertFileTopicFixture(ctx, t, "test-ps-plain")
		style := insertTechCardFixture(ctx, t, "test-ps-plain-style")

		err := s.Files().LinkTopicStyle(admin, plain, style)
		require.ErrorIs(t, err, entity.ErrStyleNeedsProjectTopic,
			"«эта вещь сделана ярлыком» не значит ничего: связь существует только у проекта")

		rows, err := storeScalarInt(ctx,
			`SELECT COUNT(*) FROM file_topic_tech_card WHERE topic_id = ?`, plain)
		require.NoError(t, err)
		require.Zero(t, rows, "отказ обязан быть ПОЛНЫМ: строки после него остаться не может")

		// Повышение той же темы до проекта делает ту же привязку законной — иначе отказ выше
		// доказывал бы только, что привязка сломана вообще.
		_, err = s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: plain, Kind: entity.FileTopicKindProject,
		})
		require.NoError(t, err)
		require.NoError(t, s.Files().LinkTopicStyle(admin, plain, style))
	})

	t.Run("заархивированный проект привязку ПРИНИМАЕТ", func(t *testing.T) {
		// В ОТЛИЧИЕ ОТ ЗААРХИВИРОВАННОЙ РОЛИ, которая назначение отвергает (Ф0). Разница по
		// существу: архив роли — вывод СЛОВА из оборота, и размечать им дальше значило бы
		// держать словарь мёртвым; архив проекта — «работа закончена», а бекап .zprj кладут как
		// раз ПОСЛЕ того, как отсняли. Запрет здесь требовал бы разархивировать съёмку, чтобы
		// записать про неё правду.
		//
		// Без этого подтеста утверждение живёт только в трёх комментариях: добавь кто-нибудь
		// проверку архива по образцу роли — весь набор остался бы зелёным.
		project := insertProjectTopicFixture(ctx, t, s, "test-ps-arch-accepts")
		style := insertTechCardFixture(ctx, t, "test-ps-arch-accepts-style")
		_, err := s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: project, Kind: entity.FileTopicKindProject, Archived: true,
		})
		require.NoError(t, err)

		require.NoError(t, s.Files().LinkTopicStyle(admin, project, style),
			"бекап кладут ПОСЛЕ съёмки: законченный проект обязан принимать привязку")
		styles, err := s.Files().ListTopicStyles(admin, project)
		require.NoError(t, err)
		require.Equal(t, []int{style}, styleIDs(styles))
	})

	t.Run("порядок стилей проекта — свежие сверху", func(t *testing.T) {
		// Список зачитывают сверху, и «последнее, что привязали» — самое интересное. Без
		// утверждения порядок был бы тем, что вернула база, то есть чем угодно.
		project := insertProjectTopicFixture(ctx, t, s, "test-ps-order")
		first := insertTechCardFixture(ctx, t, "test-ps-order-1")
		second := insertTechCardFixture(ctx, t, "test-ps-order-2")
		require.NoError(t, s.Files().LinkTopicStyle(admin, project, first))
		require.NoError(t, s.Files().LinkTopicStyle(admin, project, second))

		styles, err := s.Files().ListTopicStyles(admin, project)
		require.NoError(t, err)
		require.Equal(t, []int{second, first}, styleIDs(styles),
			"привязанный последним обязан стоять первым")
	})

	t.Run("несуществующие тема и стиль отвечают одинаково", func(t *testing.T) {
		project := insertProjectTopicFixture(ctx, t, s, "test-ps-missing")
		style := insertTechCardFixture(ctx, t, "test-ps-missing-style")

		require.ErrorIs(t, s.Files().LinkTopicStyle(admin, 2147483000, style), sql.ErrNoRows)
		require.ErrorIs(t, s.Files().LinkTopicStyle(admin, project, 2147483000), sql.ErrNoRows,
			"стиля нет — та же ошибка: различие кодов само подтверждало бы, какая из двух сущностей существует")
		require.ErrorIs(t, s.Files().UnlinkTopicStyle(admin, 2147483000, style), sql.ErrNoRows)
		_, listErr := s.Files().ListTopicStyles(admin, 2147483000)
		require.ErrorIs(t, listErr, sql.ErrNoRows,
			"тему проверяем на существование: темы и так перечисляются целиком любому с files:read, поэтому подтверждение не сообщает ничего нового")
	})

	t.Run("несуществующий стиль отдаёт пустой список, а не отказ", func(t *testing.T) {
		// ЭТО НЕ СНИСХОДИТЕЛЬНОСТЬ, А ЗАКРЫТЫЙ ОРАКУЛ. Отличимый отказ позволил бы обладателю
		// одного лишь files:read пересчитать тех-карты перебором id, ни разу не имея права их
		// читать. Тот же довод, по которому фильтр по человеку не проверяет существование
		// аккаунта.
		links, err := s.Files().ListStyleProjects(admin, 2147483000)
		require.NoError(t, err)
		require.Empty(t, links)
	})

	t.Run("карточка вещи собирает все свои проекты", func(t *testing.T) {
		// ГЛАВНЫЙ ВОПРОС ВСЕЙ ФАЗЫ. Один стиль лежит и в съёмке, и в лукбуке — ровно та
		// множественность, ради которой связь сделана многие-ко-многим, а не колонкой на теме.
		shoot := insertProjectTopicFixture(ctx, t, s, "test-ps-both-shoot")
		lookbook := insertProjectTopicFixture(ctx, t, s, "test-ps-both-lookbook")
		other := insertProjectTopicFixture(ctx, t, s, "test-ps-both-other")
		style := insertTechCardFixture(ctx, t, "test-ps-both-style")
		otherStyle := insertTechCardFixture(ctx, t, "test-ps-both-other-style")

		require.NoError(t, s.Files().LinkTopicStyle(admin, shoot, style))
		require.NoError(t, s.Files().LinkTopicStyle(admin, lookbook, style))
		// Контрольная пара: без неё «нашлось ровно два» давал бы и запрос, не фильтрующий вовсе.
		require.NoError(t, s.Files().LinkTopicStyle(admin, other, otherStyle))

		links, err := s.Files().ListStyleProjects(admin, style)
		require.NoError(t, err)
		require.ElementsMatch(t, []int{shoot, lookbook}, projectIDs(links),
			"карточка вещи обязана собрать ВСЕ свои проекты и ни одного чужого")
		for _, l := range links {
			require.NotEmpty(t, l.Name, "имя проекта печатается на карточке вещи, а не его номер")
			require.Equal(t, entity.FileTopicKindProject, l.Kind, "тип обязан приезжать: ярлык и проект рисуются по-разному")
			require.False(t, l.LinkedAt.IsZero())
		}

		// И обратная сторона: съёмка знает свои стили.
		styles, err := s.Files().ListTopicStyles(admin, shoot)
		require.NoError(t, err)
		require.Equal(t, []int{style}, styleIDs(styles))
	})

	t.Run("даты проекта доезжают до карточки вещи", func(t *testing.T) {
		// «Каким файлом сделана эта вещь» — вопрос исторический, и без дат ответ «съёмка» не
		// отличается от ответа «какая-то съёмка».
		project := insertProjectTopicFixture(ctx, t, s, "test-ps-dates")
		style := insertTechCardFixture(ctx, t, "test-ps-dates-style")
		require.NoError(t, s.Files().LinkTopicStyle(admin, project, style))

		_, err := s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId:  project,
			Kind:     entity.FileTopicKindProject,
			StartsAt: sql.NullTime{Time: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), Valid: true},
			EndsAt:   sql.NullTime{Time: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), Valid: true},
		})
		require.NoError(t, err)

		links, err := s.Files().ListStyleProjects(admin, style)
		require.NoError(t, err)
		require.Len(t, links, 1)
		require.True(t, links[0].StartsAt.Valid, "дата начала обязана доехать, а не потеряться в проекции")
		require.Equal(t, "2026-09-12", links[0].StartsAt.Time.Format("2006-01-02"))
		require.Equal(t, "2026-09-14", links[0].EndsAt.Time.Format("2006-01-02"))
	})

	t.Run("понижение проекта снимает привязки и говорит сколько", func(t *testing.T) {
		// Связь «проект ↔ стиль» существует только у проекта. Оставить её на ярлыке значило бы
		// завести состояние, невыразимое ни на одном экране; снять МОЛЧА — потерять ответ на
		// «каким файлом сделана эта вещь» у всех привязанных вещей в день, когда кто-то
		// переключил тип темы, и связать одно с другим было бы нечем.
		project := insertProjectTopicFixture(ctx, t, s, "test-ps-demote")
		one := insertTechCardFixture(ctx, t, "test-ps-demote-1")
		two := insertTechCardFixture(ctx, t, "test-ps-demote-2")
		require.NoError(t, s.Files().LinkTopicStyle(admin, project, one))
		require.NoError(t, s.Files().LinkTopicStyle(admin, project, two))

		res, err := s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: project, Kind: entity.FileTopicKindPlain,
		})
		require.NoError(t, err)
		require.Equal(t, 2, res.ClearedStyles, "понижение обязано СКАЗАТЬ, сколько привязок оно сняло")

		left, err := storeScalarInt(ctx,
			`SELECT COUNT(*) FROM file_topic_tech_card WHERE topic_id = ?`, project)
		require.NoError(t, err)
		require.Zero(t, left, "и снять их все")

		links, err := s.Files().ListStyleProjects(admin, one)
		require.NoError(t, err)
		require.Empty(t, links, "карточка вещи больше не показывает понижённый проект")

		// Повышение обратно НЕ воскрешает привязки: разметку, которой никто не ставил, вернуть
		// нельзя, и молчаливое воскрешение было бы хуже потери.
		res, err = s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: project, Kind: entity.FileTopicKindProject,
		})
		require.NoError(t, err)
		require.Zero(t, res.ClearedStyles, "повышение ничего не снимает")
		back, err := storeScalarInt(ctx,
			`SELECT COUNT(*) FROM file_topic_tech_card WHERE topic_id = ?`, project)
		require.NoError(t, err)
		require.Zero(t, back)
	})

	t.Run("два числа понижения считаются по-разному, и это видно только не-суперу", func(t *testing.T) {
		// ЗАЯВЛЕННАЯ АСИММЕТРИЯ: роли считаются ПОД предикатом видимости (число читает человек, и
		// «снято 7» в проекте, где ему видно два файла, само рассказало бы, что от него что-то
		// закрыто), а привязки стилей — ТОЧНО (стиль не файл библиотеки, он живёт под RBAC секции
		// techcards, и прятать его от того, кто и так правит проект, значило бы придумать
		// границу, которой в системе нет).
		//
		// ПРОВЕРЯТЬ ЭТО МОЖНО ТОЛЬКО ОБЫЧНЫМ СОТРУДНИКОМ. У супера предикат вырождается в
		// `1 = 1`, оба числа совпадают, и «починка» второго под предикат не сломала бы ни одного
		// утверждения.
		project := insertProjectTopicFixture(ctx, t, s, "test-ps-asym")
		role := insertFileRoleFixture(ctx, t, project, "test-ps-asym-role")
		open := insertLibraryFileWithSha(ctx, t, "ps-asym-open.pdf", pasha, sha)
		hidden := insertLibraryFileWithSha(ctx, t, "ps-asym-hidden.pdf", pasha, sha)
		_, err := s.Files().SetFileRoles(admin, []int{open, hidden}, project, role)
		require.NoError(t, err)
		_, err = s.Files().SetFileAccess(admin, hidden, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, Actor: pasha,
		})
		require.NoError(t, err)
		require.NoError(t, s.Files().LinkTopicStyle(admin,
			project, insertTechCardFixture(ctx, t, "test-ps-asym-1")))
		require.NoError(t, s.Files().LinkTopicStyle(admin,
			project, insertTechCardFixture(ctx, t, "test-ps-asym-2")))

		res, err := s.Files().UpdateTopicMeta(viewerCtx(ctx, stranger), entity.FileTopicMetaUpdate{
			TopicId: project, Kind: entity.FileTopicKindPlain,
		})
		require.NoError(t, err)
		require.Equal(t, 1, res.ClearedRoles,
			"роли обязаны считаться ПОД предикатом: закрытый файл не имеет права попасть в число, которое читает чужой")
		require.Equal(t, 2, res.ClearedStyles,
			"а привязки стилей — ТОЧНО: под предикат их подводить нечем, стиль не файл библиотеки")

		// Снялись при этом ВСЕ роли, а не только видимая: считается видимое, снимается всё.
		left, err := storeScalarInt(ctx,
			`SELECT COUNT(*) FROM library_file_topic WHERE topic_id = ? AND role_id IS NOT NULL`, project)
		require.NoError(t, err)
		require.Zero(t, left)
	})

	t.Run("удаление темы и удаление стиля уносят связь", func(t *testing.T) {
		// Висяк здесь был бы не косметикой: строка, ссылающаяся в никуда, доехала бы до JOIN'а и
		// молча выпала бы из выдачи — то есть выглядела бы как «связи и не было».
		t.Run("тема", func(t *testing.T) {
			project := insertProjectTopicFixture(ctx, t, s, "test-ps-drop-topic")
			style := insertTechCardFixture(ctx, t, "test-ps-drop-topic-style")
			require.NoError(t, s.Files().LinkTopicStyle(admin, project, style))

			// Через стор: пустая тема удаляется штатно, и связь со стилем не имеет права этому
			// мешать — RESTRICT сделал бы пустой проект неудаляемым из-за строки, которой на
			// экране тем не видно. Но удаление ОБЯЗАНО СКАЗАТЬ, сколько привязок оно унесло:
			// «убрал пустую съёмку с глаз» и «у вещи пропал ответ, каким файлом её сделали» —
			// одно и то же событие, и молчание развело бы их на месяц.
			second := insertTechCardFixture(ctx, t, "test-ps-drop-topic-style-2")
			require.NoError(t, s.Files().LinkTopicStyle(admin, project, second))

			res, err := s.Files().DeleteTopic(admin, project)
			require.NoError(t, err)
			require.Equal(t, 2, res.UnlinkedStyles,
				"удаление темы обязано вернуть число унесённых привязок: это единственный разрушительный путь фазы, и он не имеет права быть немым")
			require.Zero(t, res.UnlinkedTasks,
				"а число задач (0322) обязано остаться нулём там, где задач не было: два числа в одном ответе — это два места, где можно однажды подставить не то")

			left, err := storeScalarInt(ctx,
				`SELECT COUNT(*) FROM file_topic_tech_card WHERE topic_id = ?`, project)
			require.NoError(t, err)
			require.Zero(t, left, "каскад обязан унести связь вместе с темой")

			links, err := s.Files().ListStyleProjects(admin, style)
			require.NoError(t, err)
			require.Empty(t, links)
		})

		t.Run("стиль", func(t *testing.T) {
			project := insertProjectTopicFixture(ctx, t, s, "test-ps-drop-style")
			style := insertTechCardFixture(ctx, t, "test-ps-drop-style-tc")
			require.NoError(t, s.Files().LinkTopicStyle(admin, project, style))

			_, err := testDB.ExecContext(ctx, `DELETE FROM tech_card WHERE id = ?`, style)
			require.NoError(t, err,
				"служебная связь в библиотеке не имеет права блокировать удаление стиля: RESTRICT сделал бы заметку главнее сущности")

			left, err := storeScalarInt(ctx,
				`SELECT COUNT(*) FROM file_topic_tech_card WHERE tech_card_id = ?`, style)
			require.NoError(t, err)
			require.Zero(t, left, "каскад обязан унести связь вместе со стилем")

			styles, err := s.Files().ListTopicStyles(admin, project)
			require.NoError(t, err)
			require.Empty(t, styles)
		})
	})

	t.Run("слияние проектов переносит привязки стилей", func(t *testing.T) {
		// БЕЗ ЭТОГО ПЕРЕНОСА ПРИВЯЗКИ ИСЧЕЗЛИ БЫ МОЛЧА: внешний ключ на тему стоит с ON DELETE
		// CASCADE, и DELETE темы-источника внутри слияния унёс бы их без единого отказа. «Две
		// съёмки оказались одной» — штатный сценарий, и он стирал бы ответ на «каким файлом
		// сделана эта вещь» у всех вещей исходного проекта.
		source := insertProjectTopicFixture(ctx, t, s, "test-ps-merge-src")
		target := insertProjectTopicFixture(ctx, t, s, "test-ps-merge-dst")
		carried := insertTechCardFixture(ctx, t, "test-ps-merge-carried")
		shared := insertTechCardFixture(ctx, t, "test-ps-merge-shared")

		require.NoError(t, s.Files().LinkTopicStyle(admin, source, carried))
		require.NoError(t, s.Files().LinkTopicStyle(admin, source, shared))
		require.NoError(t, s.Files().LinkTopicStyle(admin, target, shared))

		_, err := s.Files().MergeTopics(admin, source, target)
		require.NoError(t, err)

		styles, err := s.Files().ListTopicStyles(admin, target)
		require.NoError(t, err)
		require.ElementsMatch(t, []int{carried, shared}, styleIDs(styles),
			"обе привязки обязаны оказаться в цели, а общая — не задвоиться")

		links, err := s.Files().ListStyleProjects(admin, carried)
		require.NoError(t, err)
		require.Equal(t, []int{target}, projectIDs(links),
			"и карточка вещи обязана показывать выживший проект, а не пустоту")
	})

	t.Run("архивный проект приезжает помеченным и в конце, а не прячется", func(t *testing.T) {
		// ЭТО ПРОТИВОПОЛОЖНО РЕЛЬСУ ТЕМ, И НАМЕРЕННО. Рельс прячет архив потому, что он
		// НАВИГАЦИЯ по живой работе. Карточка вещи задаёт ИСТОРИЧЕСКИЙ вопрос: съёмка, которой
		// эту вещь снимали, закончена по определению, и архивируют её именно поэтому. Спрятать
		// архив здесь значило бы спрятать ровно тот ответ, ради которого экран заведён.
		// ПОРЯДОК ЗАВЕДЕНИЯ ВЫБРАН ТАК, ЧТОБЫ «АРХИВНЫЕ В КОНЕЦ» БЫЛО НЕСУЩИМ. Живой проект
		// привязан ПЕРВЫМ, поэтому по одному лишь «свежие сверху» архивный оказался бы наверху —
		// и утверждение ниже краснеет ровно тогда, когда из ORDER BY убирают плечо архива. При
		// обратном порядке фикстуры тест был бы зелёным и на сломанной сортировке.
		live := insertProjectTopicFixture(ctx, t, s, "test-ps-arch-live")
		done := insertProjectTopicFixture(ctx, t, s, "test-ps-arch-done")
		style := insertTechCardFixture(ctx, t, "test-ps-arch-style")
		require.NoError(t, s.Files().LinkTopicStyle(admin, live, style))
		require.NoError(t, s.Files().LinkTopicStyle(admin, done, style))

		_, err := s.Files().UpdateTopicMeta(admin, entity.FileTopicMetaUpdate{
			TopicId: done, Kind: entity.FileTopicKindProject, Archived: true,
		})
		require.NoError(t, err)

		// Рельс его прячет — фиксируем контраст прямо здесь, иначе «показывается» ниже читалось
		// бы как «архив вообще не работает».
		topics, _, _, err := s.Files().ListTopics(admin, false)
		require.NoError(t, err)
		for _, tp := range topics {
			require.NotEqual(t, done, tp.Id, "в рельсе архивного проекта быть не должно")
		}

		links, err := s.Files().ListStyleProjects(admin, style)
		require.NoError(t, err)
		require.Equal(t, []int{live, done}, projectIDs(links),
			"архивный проект обязан ПРИСУТСТВОВАТЬ и стоять ПОСЛЕ живого: потерять законченную съёмку значит потерять ответ")
		require.False(t, links[0].ArchivedAt.Valid, "живой проект помечен как живой")
		require.True(t, links[1].ArchivedAt.Valid,
			"а архивный обязан приехать ПОМЕЧЕННЫМ — иначе он выглядит текущей работой")
	})

	t.Run("число файлов проекта считается под предикатом видимости", func(t *testing.T) {
		// ГЛАВНОЕ УТВЕРЖДЕНИЕ О БЕЗОПАСНОСТИ. Без предиката карточка вещи стала бы боковым
		// каналом: человек, которому в проекте видно два файла, прочёл бы «3» и узнал бы, что
		// от него что-то закрыто. Имена файлов в этой библиотеке говорящие, и число — та же
		// утечка, только выраженная цифрой.
		project := insertProjectTopicFixture(ctx, t, s, "test-ps-vis")
		style := insertTechCardFixture(ctx, t, "test-ps-vis-style")
		require.NoError(t, s.Files().LinkTopicStyle(admin, project, style))

		open1 := insertLibraryFileWithSha(ctx, t, "ps-open-1.pdf", pasha, sha)
		open2 := insertLibraryFileWithSha(ctx, t, "ps-open-2.pdf", pasha, sha)
		secret := insertLibraryFileWithSha(ctx, t, "ps-secret.pdf", pasha, sha)
		linkFileTopicFixture(ctx, t, open1, project)
		linkFileTopicFixture(ctx, t, open2, project)
		linkFileTopicFixture(ctx, t, secret, project)

		_, err := s.Files().SetFileAccess(admin, secret, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, Actor: pasha,
		})
		require.NoError(t, err)

		countFor := func(t *testing.T, ctx context.Context) int {
			t.Helper()
			links, err := s.Files().ListStyleProjects(ctx, style)
			require.NoError(t, err)
			require.Len(t, links, 1)
			return links[0].FilesCount
		}
		alien := viewerCtx(ctx, stranger)
		require.Equal(t, 2, countFor(t, alien),
			"чужой обязан видеть в счётчике ТОЛЬКО открытые файлы: иначе карточка вещи рассказывает, что в проекте есть что-то закрытое")
		require.Equal(t, 3, countFor(t, admin),
			"а тот, кому видно всё, — все три: иначе «у чужого меньше» доказывало бы лишь, что счётчик сломан вообще")

		// СЧЁТЧИК РЕЛЬСА И СЧЁТЧИК КАРТОЧКИ ВЕЩИ ОБЯЗАНЫ СОВПАДАТЬ У ОДНОГО И ТОГО ЖЕ ЧЕЛОВЕКА.
		// Расхождение и было бы дырой в чистом виде: два экрана называют разные числа про один
		// проект, и разность — это ровно то, что от человека закрыто.
		topics, _, _, err := s.Files().ListTopics(alien, true)
		require.NoError(t, err)
		railCount := -1
		for _, tp := range topics {
			if tp.Id == project {
				railCount = tp.FilesCount
			}
		}
		require.Equal(t, countFor(t, alien), railCount,
			"рельс и карточка вещи обязаны считать ОДНИМ билдером: расхождение чисел на двух экранах и есть размер утечки")
	})

	t.Run("пустой проект приезжает с нулём, а не пропадает", func(t *testing.T) {
		// Предикат стоит в ON внешнего соединения, а не в WHERE. В WHERE он превратил бы LEFT
		// JOIN в INNER, и проект, в котором смотрящему не видно НИ ОДНОГО файла, исчез бы из
		// ответа целиком — то есть карточка вещи соврала бы, что такого проекта нет.
		//
		// ПРОВЕРЯТЬ ЭТО ОБЯЗАТЕЛЬНО ОБЫЧНЫМ СОТРУДНИКОМ, А НЕ СУПЕРОМ: у супера предикат
		// вырождается в `1 = 1`, то есть в безобидное слагаемое, и перенос его из ON в WHERE не
		// меняет ничего — подтест под супером остался бы зелёным на сломанном запросе.
		project := insertProjectTopicFixture(ctx, t, s, "test-ps-empty")
		style := insertTechCardFixture(ctx, t, "test-ps-empty-style")
		require.NoError(t, s.Files().LinkTopicStyle(admin, project, style))

		hidden := insertLibraryFileWithSha(ctx, t, "ps-empty-hidden.pdf", pasha, sha)
		linkFileTopicFixture(ctx, t, hidden, project)
		_, err := s.Files().SetFileAccess(admin, hidden, entity.LibraryFileAccessUpdate{
			Level: entity.LibraryFileAccessPeople, Actor: pasha,
		})
		require.NoError(t, err)

		links, err := s.Files().ListStyleProjects(viewerCtx(ctx, stranger), style)
		require.NoError(t, err)
		require.Len(t, links, 1,
			"проект обязан остаться в ответе даже когда смотрящему в нём не видно ничего: «проект есть, показать нечего» — честный ответ, а отсутствие строки — враньё о том, что проекта нет")
		require.Zero(t, links[0].FilesCount)
	})
}

// TestFilesProjectStylesMigrationIsRerunnable re-applies 0321 over a schema it has
// ALREADY been applied to — exactly what a mid-file failure leaves behind: MySQL
// auto-commits DDL, so the half-applied schema keeps no gorp_migrations row and the
// next boot runs the file again from the top. A migration that cannot survive that
// does not fail in a test — it stops the process from starting, and DO then rolls
// the deploy back to an image that answers /readyz with 200 from the previous build.
//
// ЭТО НЕ ДУБЛИКАТ migrationlint. Тот читает ТЕКСТ и умеет ровно два правила (есть ли IF NOT
// EXISTS у создания таблицы, не сносится ли CHECK по позиционному имени); голый ALTER он
// пропустит, а на упоминание правила в комментарии выдаст false-positive. Здесь файл реально
// применяется второй раз к реальной схеме — и, что важнее, проверяется, что УЖЕ СТОЯЩИЕ связи
// повтор переживают: миграция, которая пересоздала бы таблицу, прошла бы «идемпотентность» по
// букве и стёрла бы данные по существу.
func TestFilesProjectStylesMigrationIsRerunnable(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	admin := superCtx(ctx)

	project := insertProjectTopicFixture(ctx, t, s, "test-ps-rerun")
	style := insertTechCardFixture(ctx, t, "test-ps-rerun-style")
	require.NoError(t, s.Files().LinkTopicStyle(admin, project, style))

	for i := range 2 {
		_, err := testDB.ExecContext(ctx,
			`DELETE FROM gorp_migrations WHERE id = '0321_files_project_styles.sql'`)
		require.NoError(t, err)
		require.NoError(t, Migrate(testDB), "re-applying 0321 over an applied schema (pass %d)", i+1)

		kept, err := storeScalarInt(ctx,
			`SELECT COUNT(*) FROM file_topic_tech_card WHERE topic_id = ? AND tech_card_id = ?`,
			project, style)
		require.NoError(t, err)
		require.Equal(t, 1, kept,
			"повтор обязан быть no-op ПО СУЩЕСТВУ, а не только по коду возврата: пересозданная таблица стёрла бы уже заведённые связи")

		fks, err := storeScalarInt(ctx, `
			SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'file_topic_tech_card'
			  AND CONSTRAINT_TYPE = 'FOREIGN KEY'`)
		require.NoError(t, err)
		require.Equal(t, 2, fks, "оба каскада обязаны стоять после повтора: связь без каскадов оставляет висяки")
	}
}
