package fileslibrary

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ОБСУЖДЕНИЕ ФАЙЛА (Ф5, миграция 0316). ПЛОСКАЯ ЛЕНТА.
//
// Веток нет ни здесь, ни в таблице: ни один запрос этого файла не читает и не пишет parent_id,
// потому что его не существует. Довод — в шапке 0316_file_comments.sql.
//
// ЧЕГО ЗДЕСЬ НЕТ И БЫТЬ НЕ ДОЛЖНО:
//
//   - ПРАВА. Стор не знает, кто зовущий. Правило «править и удалять можно только СВОЮ реплику,
//     супер — любую» живёт в хендлере (internal/apisrv/admin/files_comments.go) и сравнивает
//     author с username из JWT. Размажь это правило по двум слоям — и оно разойдётся на первой
//     же правке, причём разойдётся молча.
//   - РАЗБОР @упоминаний. Тело кладётся ровно таким, каким его набрали. Подсветка и поповер —
//     клиентские; серверная разметка всё равно потребовала бы экранирования у каждого читателя.
//
// ПРЕДИКАТ ВИДИМОСТИ (Ф7, T-7.3, точка 7) СТОИТ НА ВСЕХ ПЯТИ МЕТОДАХ. Он приходит сюда
// коррелированным EXISTS'ом (Viewer.ExistsFile, visibility.go) — самой library_file в этих
// запросах нет, а второй раз писать условие запрещено. Счётчик реплик предиката не требует: его
// зовут на файлы, которые уже прошли предикат в своей выдаче.

// commentColumns — единственный список колонок ленты. Звёздочку здесь писать нельзя: entity
// сканируется StructScan-ом, и добавленная в 0316+ колонка без поля в структуре уронила бы КАЖДОЕ
// чтение ленты, а не то место, где её забыли описать.
const commentColumns = `id, file_id, author, author_id, body, created_at, edited_at`

// ListComments returns the whole feed of one file, oldest first.
//
// ORDER BY id, А НЕ created_at. Индекс (file_id, id) обслуживает такую сортировку целиком, но
// главное не это: created_at имеет секундную гранулярность, поэтому две реплики, написанные в одну
// секунду, при сортировке по времени встали бы в произвольном порядке — и «ответ раньше вопроса»
// в переписке появлялся бы редко и невоспроизводимо. AUTO_INCREMENT это ровно порядок письма.
//
// Отсутствующий файл отдаёт ПУСТУЮ ленту, а не sql.ErrNoRows: карточка открывается через
// GetFileById, который про пропавший файл уже сказал, а второй запрос существования на каждое
// чтение ленты платит за случай, которого на живом клиенте не бывает. НЕВИДИМЫЙ файл отдаёт ту
// же пустую ленту тем же условием — «нет файла» и «не твой файл» обязаны быть неразличимы, а
// хендлер ленты переводит в Internal всё, что не пусто, поэтому отдельной ошибки здесь и не надо.
func (s *Store) ListComments(ctx context.Context, fileID int) ([]entity.LibraryFileComment, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"fileId": fileID}
	comments, err := storeutil.QueryListNamed[entity.LibraryFileComment](ctx, s.DB,
		`SELECT `+commentColumns+` FROM library_file_comment
		WHERE file_id = :fileId AND `+v.ExistsFile("library_file_comment.file_id", params)+`
		ORDER BY id`, params)
	if err != nil {
		return nil, fmt.Errorf("can't list library file comments: %w", err)
	}
	return comments, nil
}

// GetCommentById reads one remark — это то, что читает правило «только свою» ПЕРЕД правкой и
// удалением: автор хранится, а не приезжает в запросе. sql.ErrNoRows на пропавшей реплике И на
// реплике под невидимым файлом: иначе чужая переписка читалась бы по перебору id реплик, минуя
// файл целиком.
func (s *Store) GetCommentById(ctx context.Context, id int) (*entity.LibraryFileComment, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"id": id}
	c, err := storeutil.QueryNamedOne[entity.LibraryFileComment](ctx, s.DB,
		`SELECT `+commentColumns+` FROM library_file_comment
		WHERE id = :id AND `+v.ExistsFile("library_file_comment.file_id", params), params)
	if err != nil {
		return nil, err // sql.ErrNoRows passes through untouched
	}
	return &c, nil
}

// AddComment appends one remark and returns it AS STORED.
//
// ДВЕ ПОЛОВИНЫ АВТОРСТВА ЗАПИСЫВАЮТСЯ ОДНИМ ОПЕРАТОРОМ — тот же приём, что у uploaded_by_id файла
// (0314): живая ссылка ВЫВОДИТСЯ из той же строки username, а не приезжает вторым параметром.
// Дай вызывающему прислать их порознь — и однажды приедет реплика, у которой строка говорит
// «pasha», а ссылка ведёт на kirill. NULL, если аккаунта с таким именем нет: ссылка в никуда хуже
// её отсутствия, а строка-факт при этом остаётся.
//
// Существование файла проверяется ВНУТРИ транзакции. Пишущие транзакции стора идут в SERIALIZABLE,
// поэтому проверка реально закрывает гонку с удалением файла, а не просто сужает окно. Внешний
// ключ — второй рубеж: он бы тоже не дал вставить реплику в пустоту, но сказал бы об этом сырым
// нарушением констрейнта, которое хендлеру нечем превратить в NotFound.
func (s *Store) AddComment(ctx context.Context, fileID int, author, body string) (*entity.LibraryFileComment, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	var stored *entity.LibraryFileComment
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Существование И видимость — одной проверкой: написать в обсуждение файла, которого тебе
		// не показывают, нельзя, и отказ обязан быть NotFound, а не PermissionDenied.
		if err := EnsureVisible(ctx, rep.DB(), v, fileID); err != nil {
			return err // sql.ErrNoRows нетронутым
		}
		id, err := storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO library_file_comment (file_id, author, author_id, body)
			VALUES (:fileId, :author, (SELECT a.id FROM admins a WHERE a.username = :author), :body)`,
			map[string]any{"fileId": fileID, "author": author, "body": body})
		if err != nil {
			return fmt.Errorf("failed to insert library file comment: %w", err)
		}
		// Перечитываем ВНУТРИ той же транзакции: created_at ставит сервер БД, и лента обязана
		// отрисовать то, что легло, а не то, что клиент надеялся отправить.
		stored, err = readComment(ctx, rep.DB(), id)
		return err
	})
	if err != nil {
		return nil, err // sql.ErrNoRows passes through untouched
	}
	return stored, nil
}

// UpdateComment rewrites the body and STAMPS edited_at.
//
// Метка времени — не украшение: молча переписанная реплика это молча переписанный разговор, и
// человек, который на неё ссылался, не имеет способа узнать, что ссылается уже на другое.
//
// Ноль затронутых строк НЕ ОЗНАЧАЕТ «реплики нет»: тот же ноль возвращает повторное сохранение
// того же текста в ту же секунду (edited_at при этом тоже не меняется). Поэтому существование
// перепроверяется отдельно — иначе сохранение без изменений отвечало бы «реплика удалена», и
// человек искал бы её у себя на экране.
// Предикат стоит И в UPDATE, И в перепроверке существования. Только в UPDATE было бы мало: на
// невидимой реплике он затронул бы ноль строк, перепроверка нашла бы её и вернула «сохранено без
// изменений» — тот же ответ, что у настоящей повторной правки, то есть подтверждение, что реплика
// (а значит и файл) существует.
func (s *Store) UpdateComment(ctx context.Context, id int, body string) (*entity.LibraryFileComment, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	var stored *entity.LibraryFileComment
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		params := map[string]any{"id": id, "body": body}
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE library_file_comment SET body = :body, edited_at = CURRENT_TIMESTAMP
			WHERE id = :id AND `+v.ExistsFile("library_file_comment.file_id", params), params)
		if err != nil {
			return fmt.Errorf("failed to update library file comment: %w", err)
		}
		if rows == 0 {
			existsParams := map[string]any{"id": id}
			exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
				`SELECT COUNT(*) FROM library_file_comment
				WHERE id = :id AND `+v.ExistsFile("library_file_comment.file_id", existsParams), existsParams)
			if err != nil {
				return fmt.Errorf("failed to check library file comment existence: %w", err)
			}
			if exists == 0 {
				return sql.ErrNoRows
			}
		}
		stored, err = readComment(ctx, rep.DB(), id)
		return err
	})
	if err != nil {
		return nil, err // sql.ErrNoRows passes through untouched
	}
	return stored, nil
}

// DeleteComment removes one remark. sql.ErrNoRows, если удалять было нечего: «удалено» про то,
// чего нет, оставляет ленту на экране и человека в уверенности, что реплика ушла.
//
// Без транзакции сознательно: один оператор атомарен сам по себе, а лента ни на что не ссылается —
// каскад от файла и обнуление автора живут в схеме (0316), а не в этом коде.
func (s *Store) DeleteComment(ctx context.Context, id int) error {
	v, err := s.viewer(ctx)
	if err != nil {
		return err
	}
	params := map[string]any{"id": id}
	rows, err := storeutil.ExecNamedRows(ctx, s.DB,
		`DELETE FROM library_file_comment
		WHERE id = :id AND `+v.ExistsFile("library_file_comment.file_id", params), params)
	if err != nil {
		return fmt.Errorf("can't delete library file comment: %w", err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// AttachCommentsCount заполняет LibraryFile.CommentsCount для ЦЕЛОЙ страницы одним сгруппированным
// запросом — форма та же, что у attachTopics/attachOwners в fileslibrary.go.
//
// ЭТО ЕДИНСТВЕННЫЙ ДОПУСТИМЫЙ СПОСОБ СЧИТАТЬ РЕПЛИКИ В ВЫДАЧЕ. Сетка рисует до двухсот плиток;
// COUNT(*) на плитку — это двести round-trip'ов за числом, которое одним GROUP BY стоит один.
// Колонки-счётчика в library_file нет намеренно: её пришлось бы держать в согласии с лентой,
// которая каскадом умирает вместе с файлом, и она разошлась бы с правдой в первый же раз, когда
// этого не сделали.
//
// ГДЕ ЭТО ВЫЗЫВАЕТСЯ. Одной строкой в конце attachRelated (fileslibrary.go) — рядом с темами и
// владельцами, чтобы файл из витрины доступа выглядел так же, как файл из сетки:
//
//	if err := s.attachOwners(ctx, files); err != nil {
//	    return err
//	}
//	return s.AttachCommentsCount(ctx, files)
//
// Вставку делает автор предиката видимости (Ф7, T-7.3): fileslibrary.go правится в этой волне
// именно там, и параллельная правка одного файла двумя исполнителями сорвала бы обоих.
//
// Экспортирован ровно поэтому: вызывающий живёт в этом же пакете и в экспорте не нуждается, а вот
// контейнерный тест ленты лежит в пакете store и без экспортированного имени не смог бы доказать
// ни группировку, ни ноль у файла без обсуждения ДО того, как вставка состоится.
func (s *Store) AttachCommentsCount(ctx context.Context, files []*entity.LibraryFile) error {
	if len(files) == 0 {
		return nil
	}
	ids := make([]int, 0, len(files))
	byId := make(map[int]*entity.LibraryFile, len(files))
	for _, f := range files {
		ids = append(ids, f.Id)
		byId[f.Id] = f
	}
	type row struct {
		FileId int `db:"file_id"`
		// Имя колонки — comments_count, как поле в контракте: счётчик и его получатель обязаны
		// называться одинаково, иначе следующий читатель ищет между ними соответствие.
		CommentsCount int `db:"comments_count"`
	}
	rows, err := storeutil.QueryListNamed[row](ctx, s.DB, `
		SELECT file_id, COUNT(*) AS comments_count
		FROM library_file_comment
		WHERE file_id IN (:ids)
		GROUP BY file_id`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("failed to count library file comments: %w", err)
	}
	// Файл без обсуждения не приезжает из GROUP BY вовсе, и это правильно: ноль — значение по
	// умолчанию у поля, а не строка, которую надо было бы вернуть отдельным запросом.
	for _, r := range rows {
		if f, ok := byId[r.FileId]; ok {
			f.CommentsCount = r.CommentsCount
		}
	}
	return nil
}

// readComment — одно чтение реплики, общее для прямого запроса и для перечитывания после записи.
// sql.ErrNoRows проходит наружу нетронутым: по нему хендлер отличает NotFound от Internal.
func readComment(ctx context.Context, db dependency.DB, id int) (*entity.LibraryFileComment, error) {
	c, err := storeutil.QueryNamedOne[entity.LibraryFileComment](ctx, db,
		`SELECT `+commentColumns+` FROM library_file_comment WHERE id = :id`,
		map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	return &c, nil
}
