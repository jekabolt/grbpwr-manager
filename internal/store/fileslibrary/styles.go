package fileslibrary

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ПРОЕКТ ↔ СТИЛЬ (0321): «КАКИМ ФАЙЛОМ СДЕЛАНА ЭТА ВЕЩЬ».
//
// Ф0 отвечала на «что лежит в этой съёмке». Здесь закрывается обратный вопрос: человек стоит на
// карточке вещи и спрашивает, каким .zprj она сшита и какая раскладка в неё уехала. Связь
// многие-ко-многим потому, что множественны ОБЕ стороны: съёмка покрывает капсулу (один проект →
// восемь стилей), а бекап CLO покрывает одну вещь, которая при этом попадает и в съёмку, и в
// лукбук (один стиль → несколько проектов).
//
// ТРИ ГРАНИЦЫ, КОТОРЫЕ ЗДЕСЬ ДЕРЖАТСЯ И КОТОРЫЕ ЛЕГКО ПОТЕРЯТЬ:
//
//  1. СВЯЗЬ ТОЛЬКО У ПРОЕКТА. Проверяется ВНУТРИ пишущей транзакции, а не перед ней: транзакции
//     стора идут в SERIALIZABLE, поэтому чтение `kind` запирает строку темы, и параллельное
//     понижение проекта не может проскочить между проверкой и вставкой. Снаружи это было бы
//     фикцией — под обычным снимком повторное чтение вернуло бы ту же устаревшую картину.
//  2. ЧИСЛО ФАЙЛОВ ПРОЕКТА, ВИДИМОЕ С КАРТОЧКИ ВЕЩИ, СЧИТАЕТСЯ ПОД ПРЕДИКАТОМ ВИДИМОСТИ, тем же
//     билдером Viewer.Where, что и рельс тем. Иначе карточка вещи стала бы боковым каналом:
//     человек, которому в проекте видно два файла, прочёл бы там «7» и узнал бы, что от него
//     что-то закрыто. Это дыра, а не неточность — счётчики тем сделаны персональными ровно ради
//     устранения этого сигнала.
//  3. СТИЛЬ — НЕ ФАЙЛ. У списка стилей проекта предиката видимости нет и быть не может: тех-карта
//     живёт под собственным RBAC секции techcards, и накладывать на неё правила библиотеки
//     значило бы придумать границу, которой в системе не существует.
//
// АСИММЕТРИЯ ПРОВЕРОК СУЩЕСТВОВАНИЯ — НАМЕРЕННАЯ, И ЛИНИЯ ПРОХОДИТ МЕЖДУ ЧТЕНИЕМ И ЗАПИСЬЮ.
//
//   - ЧТЕНИЕ ОРАКУЛОМ БЫТЬ НЕ ИМЕЕТ ПРАВА. ListStyleProjects на несуществующем стиле отвечает
//     ПУСТЫМ СПИСКОМ: отличимый отказ позволил бы перебором id и различением ответов пересчитать
//     тех-карты обладателю одного лишь files:read, ни разу не имеющему права их читать. Тот же
//     довод, по которому фильтр по человеку не проверяет существование аккаунта.
//   - ЗАПИСЬ ОРАКУЛ ПО ПОСТРОЕНИЮ, и спрятать это нечем. LinkTopicStyle на несуществующем стиле
//     обязан отказать (иначе связь молча не заведётся, см. про INSERT IGNORE ниже), а успех сам
//     по себе сообщает, что стиль есть. Требовать от записи неразличимости значило бы требовать,
//     чтобы она не сообщала о собственном результате. Планка тут другая: коды отказа не должны
//     различать ТЕМУ и СТИЛЬ — и не различают, оба уезжают одним sql.ErrNoRows.
//   - ListTopicStyles на несуществующей теме отвечает sql.ErrNoRows, и это не третий подход:
//     темы целиком перечисляются любому с files:read (ListFileTopics), поэтому подтверждение
//     существования темы не сообщает ничего нового.

// LinkTopicStyle attaches a style to a PROJECT topic. Idempotent: linking twice
// is a no-op, not a unique-key refusal.
//
// ПОВТОР — NO-OP, А НЕ 1062, и это не снисходительность к клиенту. Кнопка «привязать» живёт на
// двух экранах сразу (страница проекта и карточка вещи), и второй человек, нажавший её на уже
// привязанном стиле, сделал ровно то, чего хотел, — связь есть. Отказ по уникальному ключу
// сообщил бы об ошибке там, где ошибки нет. Сравни с UpsertRole, где совпадение имени ОТКАЗЫВАЕТ:
// там повтор создаёт ВТОРУЮ сущность и молча отдаёт чужую, здесь повтор не создаёт ничего.
//
// ЗААРХИВИРОВАННЫЙ ПРОЕКТ ПРИВЯЗКУ ПРИНИМАЕТ, в отличие от заархивированной РОЛИ, которая
// назначение отвергает. Разница по существу: архив роли — это вывод СЛОВА из оборота, и продолжать
// им размечать значило бы держать словарь мёртвым; архив проекта — это «работа закончена», а
// бекап .zprj кладут как раз ПОСЛЕ того, как отсняли. Запретить здесь значило бы требовать
// разархивировать съёмку, чтобы записать про неё правду.
func (s *Store) LinkTopicStyle(ctx context.Context, topicID, techCardID int) error {
	if topicID <= 0 || techCardID <= 0 {
		return sql.ErrNoRows
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		kind, err := storeutil.QueryNamedOne[struct {
			Kind entity.FileTopicKind `db:"kind"`
		}](ctx, rep.DB(),
			`SELECT kind FROM file_topic WHERE id = :id`, map[string]any{"id": topicID})
		if err != nil {
			return err // sql.ErrNoRows нетронутым: темы нет
		}
		if kind.Kind != entity.FileTopicKindProject {
			// ВНЯТНАЯ ОШИБКА, А НЕ ОТКАЗ ВНЕШНЕГО КЛЮЧА. Внешний ключ здесь и не сработал бы —
			// тема существует, просто она ярлык, — но даже там, где он срабатывает, он отвечает
			// номером ключа, а не фразой про то, что человек сделал не так.
			return entity.ErrStyleNeedsProjectTopic
		}
		exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM tech_card WHERE id = :id`, map[string]any{"id": techCardID})
		if err != nil {
			return fmt.Errorf("failed to check tech card existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		// ПРОВЕРКА ВЫШЕ НЕ ДУБЛИРУЕТ ВНЕШНИЙ КЛЮЧ, И СНЯТЬ ЕЁ НЕЛЬЗЯ. `INSERT IGNORE` в MySQL
		// глушит не только 1062 (повтор — ровно то, ради чего он здесь), но и 1452
		// ER_NO_REFERENCED_ROW_2: без пред-проверки привязка к несуществующему стилю прошла бы
		// «успешно» и не завела бы ни строки. Обе проверки стоят ВНУТРИ транзакции, а она
		// SERIALIZABLE, поэтому чтение запирает строки и удаление стиля не проскакивает между
		// проверкой и вставкой.
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			INSERT IGNORE INTO file_topic_tech_card (topic_id, tech_card_id)
			VALUES (:topicId, :techCardId)`,
			map[string]any{"topicId": topicID, "techCardId": techCardID}); err != nil {
			return fmt.Errorf("failed to link a style to a project: %w", err)
		}
		return nil
	})
}

// UnlinkTopicStyle detaches a style from a project topic. Idempotent: unlinking
// what is not linked succeeds.
//
// ТИП ТЕМЫ ЗДЕСЬ НЕ ПРОВЕРЯЕТСЯ, и это не пропущенная симметрия. Отвязка — единственный способ
// убрать связь, а связь на теме, которую УЖЕ понизили до ярлыка, теоретически может существовать
// (хотя понижение их и снимает). Требовать «сначала повысь обратно, потом отвязывай» значило бы
// запереть уборку за восстановлением того самого состояния, из которого уходили.
//
// Существование темы при этом проверяется: «отвязал у несуществующего проекта» — это опечатка в
// id, и отвечать на неё «готово» значило бы убедить человека, что он что-то сделал.
func (s *Store) UnlinkTopicStyle(ctx context.Context, topicID, techCardID int) error {
	if topicID <= 0 || techCardID <= 0 {
		return sql.ErrNoRows
	}
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM file_topic WHERE id = :id`, map[string]any{"id": topicID})
		if err != nil {
			return fmt.Errorf("failed to check file topic existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			DELETE FROM file_topic_tech_card
			WHERE topic_id = :topicId AND tech_card_id = :techCardId`,
			map[string]any{"topicId": topicID, "techCardId": techCardID}); err != nil {
			return fmt.Errorf("failed to unlink a style from a project: %w", err)
		}
		return nil
	})
}

// ListTopicStyles returns the styles a project is about — the project page's list
// of garments. Returns sql.ErrNoRows when no such topic exists.
//
// Стадия едет вместе с номером и именем, потому что «бекап CLO» у идеи и у стиля в производстве
// значат разное, и без неё список выглядит плоским перечнем имён.
//
// Поле PreviewURL этот запрос ОСТАВЛЯЕТ ПУСТЫМ — его заполняет вызывающий из
// TechCards().PreviewURLsByTechCardIds. Правило выбора картинки (стадия × категория медиа × вид)
// живёт в сторе тех-карт, и вторая его реализация здесь разошлась бы с первой молча — на экране
// это выглядело бы как «у одной и той же вещи в двух местах разные картинки», и искать причину
// пришлось бы в SQL.
func (s *Store) ListTopicStyles(ctx context.Context, topicID int) ([]entity.FileTopicStyleRef, error) {
	if topicID <= 0 {
		return nil, sql.ErrNoRows
	}
	exists, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM file_topic WHERE id = :id`, map[string]any{"id": topicID})
	if err != nil {
		return nil, fmt.Errorf("failed to check file topic existence: %w", err)
	}
	if exists == 0 {
		return nil, sql.ErrNoRows
	}
	styles, err := storeutil.QueryListNamed[entity.FileTopicStyleRef](ctx, s.DB, `
		SELECT tc.id AS tech_card_id,
		       COALESCE(tc.style_number, '') AS style_number,
		       tc.name,
		       tc.stage,
		       ftc.created_at AS linked_at
		FROM file_topic_tech_card ftc
		JOIN tech_card tc ON tc.id = ftc.tech_card_id
		WHERE ftc.topic_id = :topicId
		ORDER BY ftc.created_at DESC, tc.id DESC`,
		map[string]any{"topicId": topicID})
	if err != nil {
		return nil, fmt.Errorf("failed to list project styles: %w", err)
	}
	return styles, nil
}

// ListStyleProjects returns the projects a style is mentioned in — THE call this
// whole phase exists for: «какие проекты меня касаются», asked from the garment's
// own card.
//
// СЧЁТЧИК ФАЙЛОВ ИДЁТ ПОД ПРЕДИКАТОМ ВИДИМОСТИ, И ЭТО ГЛАВНОЕ СВОЙСТВО ЗАПРОСА. Предикат стоит в
// ON внешнего соединения, а не в WHERE, по той же причине, что в ListTopics и ListRoles: в WHERE
// он превратил бы LEFT JOIN в INNER и выкинул бы проекты, в которых спрашивающему не видно НИ
// ОДНОГО файла, — а такой проект обязан быть в ответе с нулём. Ноль — честный ответ («проект есть,
// показать нечего»); отсутствие строки было бы враньём о том, что проекта нет.
//
// АРХИВ ПОКАЗЫВАЕТСЯ, А НЕ ПРЯЧЕТСЯ, И ЭТО ПРОТИВОПОЛОЖНО РЕЛЬСУ ТЕМ — намеренно. Рельс и пикеры
// прячут архив потому, что они НАВИГАЦИЯ по живой работе, и законченная съёмка там только мешает.
// Карточка вещи задаёт ИСТОРИЧЕСКИЙ вопрос: съёмка, которой эту вещь снимали, закончена по
// определению, и архивируют её именно поэтому. Спрятать архив здесь значило бы спрятать ровно тот
// ответ, ради которого экран заведён. Поэтому архивный проект приезжает ПОМЕЧЕННЫМ (archived) и
// уходит в конец списка — отличить его от живого можно, потерять нельзя.
func (s *Store) ListStyleProjects(ctx context.Context, techCardID int) ([]entity.StyleProjectLink, error) {
	if techCardID <= 0 {
		return nil, nil
	}
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"techCardId": techCardID}
	links, err := storeutil.QueryListNamed[entity.StyleProjectLink](ctx, s.DB, `
		SELECT ft.*, COUNT(lf.id) AS files_count, ftc.created_at AS linked_at
		FROM file_topic_tech_card ftc
		JOIN file_topic ft ON ft.id = ftc.topic_id
		LEFT JOIN library_file_topic lft ON lft.topic_id = ft.id
		LEFT JOIN library_file lf ON lf.id = lft.file_id AND `+v.Where("lf", params)+`
		WHERE ftc.tech_card_id = :techCardId
		GROUP BY ft.id, ftc.created_at
		ORDER BY (ft.archived_at IS NOT NULL) ASC, ftc.created_at DESC, ft.id DESC`, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list style projects: %w", err)
	}
	return links, nil
}
