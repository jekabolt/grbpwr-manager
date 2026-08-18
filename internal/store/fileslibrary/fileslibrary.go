// Package fileslibrary implements storage for the files library: metadata of
// private S3 objects, the topic labels they carry, and the maintenance of both.
// The bytes themselves are the bucket's business; this package never touches S3.
package fileslibrary

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Pagination bounds for ListFiles, mirroring the task store: a topic view wants
// the whole set, so the default is generous; the cap still guards an unbounded
// scan. The client pages with infinite scroll — without paging, the tail beyond
// the default would vanish silently, which reads exactly like "the file is not
// there".
const (
	defaultPageLimit = 200
	maxPageLimit     = 1000
)

// TxFunc executes f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Files.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new files-library store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// AddFile inserts the metadata row and links its topics in one transaction.
// Names in newTopics are created on the fly; an existing name resolves to the
// existing topic rather than failing, so two people uploading into the same new
// topic at the same second both succeed.
func (s *Store) AddFile(ctx context.Context, f *entity.LibraryFileInsert, topicIDs []int, newTopics []string) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		// uploaded_by_id ВЫВОДИТСЯ ИЗ ТОГО ЖЕ ИМЕНИ ОДНИМ ОПЕРАТОРОМ, а не приходит
		// вторым параметром: две половины авторства (историческая строка и живая
		// ссылка) обязаны описывать одного человека, и единственный способ это
		// гарантировать — не давать вызывающему возможности прислать их порознь.
		// Тот же UPDATE ... JOIN admins, что делает backfill 0314 для истории.
		// NULL, если аккаунта с таким именем нет: ссылка в никуда была бы хуже.
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO library_file
				(object_key, preview_object_key, file_name, content_type, size_bytes, sha256, uploaded_by, uploaded_by_id)
			VALUES (:objectKey, :previewObjectKey, :fileName, :contentType, :sizeBytes, :sha256, :uploadedBy,
				(SELECT a.id FROM admins a WHERE a.username = :uploadedBy))`,
			map[string]any{
				"objectKey":        f.ObjectKey,
				"previewObjectKey": f.PreviewObjectKey,
				"fileName":         f.FileName,
				"contentType":      f.ContentType,
				"sizeBytes":        f.SizeBytes,
				"sha256":           f.Sha256,
				"uploadedBy":       f.UploadedBy,
			})
		if err != nil {
			return fmt.Errorf("failed to insert library file: %w", err)
		}
		return linkTopics(ctx, rep.DB(), id, topicIDs, newTopics)
	})
	if err != nil {
		return 0, fmt.Errorf("can't add library file: %w", err)
	}
	return id, nil
}

// UpdateFile renames the file and REPLACES its topic set (task_media semantics:
// the caller sends the full set it wants, not a delta). Returns sql.ErrNoRows
// when no such file exists.
//
// ТОЧКА 10 ПРЕДИКАТА (запись). Проверка стоит ПЕРВОЙ и внутри транзакции: невидимый файл обязан
// ответить NotFound, а не PermissionDenied, — «нет прав» подтвердило бы, что файл существует.
// Rows-affected здесь ничего бы не рассказал: UPDATE на невидимой строке прошёл бы успешно.
//
// ЗАМЕНА СЧИТАЕТСЯ РАЗНИЦЕЙ, А НЕ «СНЕСТИ И ЗАВЕСТИ ЗАНОВО», И ЭТО НЕ ОПТИМИЗАЦИЯ. С 0320 на
// строке связи живёт РОЛЬ файла в проекте, и прежний `DELETE ... WHERE file_id` + вставка стирал
// бы её МОЛЧА при любом сохранении карточки — переименовал файл, потерял «исходники» во всех
// съёмках, и ни одного сообщения об этом. Уцелевшие темы теперь не трогаются вовсе, поэтому роль
// переживает переименование; побочно перестаёт скакать created_at связи.
func (s *Store) UpdateFile(ctx context.Context, id int, fileName string, topicIDs []int, newTopics []string) error {
	v, err := s.viewer(ctx)
	if err != nil {
		return err
	}
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Существование И видимость — одной проверкой, ВНУТРИ SERIALIZABLE-транзакции: она же
		// снимает нужду перепроверять существование после UPDATE (ноль затронутых строк дальше
		// может означать только «имя не изменилось»).
		if err := EnsureVisible(ctx, rep.DB(), v, id); err != nil {
			return err // sql.ErrNoRows нетронутым
		}
		if _, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE library_file SET file_name = :fileName WHERE id = :id`,
			map[string]any{"id": id, "fileName": fileName}); err != nil {
			return fmt.Errorf("failed to update library file: %w", err)
		}
		return syncTopics(ctx, rep.DB(), id, topicIDs, newTopics)
	})
	if err != nil {
		return fmt.Errorf("can't update library file: %w", err)
	}
	return nil
}

// DeleteFile removes the metadata row and returns the S3 object keys behind it
// so the caller can clean the bucket. It REFUSES while any task still holds the
// file, returning entity.ErrLibraryFileInUse naming the holders: the FK is the
// backstop, but a bare constraint error would only say "cannot delete", and the
// person would have no way to find out why.
//
// ТОЧКА 10 ПРЕДИКАТА (запись). Проверка видимости стоит ПЕРЕД списком держателей, и порядок здесь
// содержательный: отказ ErrLibraryFileInUse НАЗЫВАЕТ номера задач, то есть на невидимом файле
// подтвердил бы его существование и заодно рассказал, где он используется.
func (s *Store) DeleteFile(ctx context.Context, id int) ([]string, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	var keys []string
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := EnsureVisible(ctx, rep.DB(), v, id); err != nil {
			return err // sql.ErrNoRows нетронутым
		}
		// Скалярный вариант обязателен: QueryListNamed сканирует StructScan-ом и на
		// int ПАНИКУЕТ — но только когда строки есть, то есть ровно в отказе, ради
		// которого этот запрос и написан. Отказ без покрытия читался как рабочий.
		holders, err := storeutil.QueryScalarListNamed[int](ctx, rep.DB(),
			`SELECT task_id FROM task_file WHERE file_id = :id ORDER BY task_id`,
			map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to list holding tasks: %w", err)
		}
		if len(holders) > 0 {
			return entity.NewErrLibraryFileInUse(holders)
		}
		f, err := storeutil.QueryNamedOne[entity.LibraryFile](ctx, rep.DB(),
			`SELECT * FROM library_file WHERE id = :id`, map[string]any{"id": id})
		if err != nil {
			return err // sql.ErrNoRows passes through untouched
		}
		keys = append(keys, f.ObjectKey)
		if f.PreviewObjectKey.Valid && f.PreviewObjectKey.String != "" {
			keys = append(keys, f.PreviewObjectKey.String)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM library_file WHERE id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to delete library file: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// GetFileById returns one file with its topics resolved.
//
// ТОЧКА 2 ПРЕДИКАТА. Отказ приходит ОТСЮДА, то есть ДО того, как хендлер минтит presigned-ссылки:
// невидимый файл неотличим от несуществующего (sql.ErrNoRows → NotFound), и ни имени, ни ссылки
// на байты за ним не уезжает.
func (s *Store) GetFileById(ctx context.Context, id int) (*entity.LibraryFile, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"id": id}
	f, err := storeutil.QueryNamedOne[entity.LibraryFile](ctx, s.DB,
		`SELECT lf.* FROM library_file lf WHERE lf.id = :id AND `+v.Where("lf", params), params)
	if err != nil {
		return nil, err
	}
	if err := s.attachRelated(ctx, []*entity.LibraryFile{&f}); err != nil {
		return nil, err
	}
	return &f, nil
}

// ListFilesByIds resolves an explicit set (task attachments). Order follows the
// given ids so the card shows attachments in the order they were attached.
//
// ТОЧКА 3 ПРЕДИКАТА. Невидимый файл ПРОСТО ВЫПАДАЕТ из резолва, а не роняет карточку задачи:
// у задачи может быть десяток вложений, и одно ограниченное не повод не показать девять
// остальных. Голые id в Task.file_ids при этом остаются — числа без имён, решение плана
// (§Ф7, точка 3), и оно защищает от худшего: отфильтруй мы их, ближайшее сохранение формы
// задачи (замещающий набор file_ids) МОЛЧА ОТЦЕПИЛО БЫ невидимое вложение.
func (s *Store) ListFilesByIds(ctx context.Context, ids []int) ([]entity.LibraryFile, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"ids": ids}
	files, err := storeutil.QueryListNamed[entity.LibraryFile](ctx, s.DB,
		`SELECT lf.* FROM library_file lf WHERE lf.id IN (:ids) AND `+v.Where("lf", params), params)
	if err != nil {
		return nil, fmt.Errorf("failed to list library files by ids: %w", err)
	}
	byId := make(map[int]entity.LibraryFile, len(files))
	for _, f := range files {
		byId[f.Id] = f
	}
	out := make([]entity.LibraryFile, 0, len(ids))
	for _, id := range ids {
		if f, ok := byId[id]; ok {
			out = append(out, f)
		}
	}
	if err := s.attachRelatedSlice(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// FindFilesBySha256 backs the duplicate hint in the upload response. It is a
// hint, not a guard: an identical file is allowed in, it is just called out.
//
// ТОЧКА 4 ПРЕДИКАТА, И ЭТО САМАЯ ЗАБЫВАЕМАЯ ИЗ ТРИНАДЦАТИ. Подсказка печатает ИМЯ найденного
// дубликата — то есть без предиката любой, у кого случайно оказались те же байты, узнавал бы
// имя ограниченного файла, ни разу не открыв библиотеку. Отпечаток сюда приносит загружающий,
// значит подобрать его можно намеренно.
func (s *Store) FindFilesBySha256(ctx context.Context, sha256 string) ([]entity.LibraryFile, error) {
	if sha256 == "" {
		return nil, nil
	}
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"sha": sha256}
	files, err := storeutil.QueryListNamed[entity.LibraryFile](ctx, s.DB,
		`SELECT lf.* FROM library_file lf WHERE lf.sha256 = :sha AND `+v.Where("lf", params)+
			` ORDER BY lf.id`, params)
	if err != nil {
		return nil, fmt.Errorf("failed to find library files by sha256: %w", err)
	}
	return files, nil
}

// ListFiles returns a page of the library plus the total matching count.
//
// ОДНО УСЛОВИЕ НА СЧЁТ И НА СТРАНИЦУ (`clause` ниже строится один раз и подставляется в оба
// запроса). Это не экономия строк: разойдись они хоть одним плечом — и «показано N из M»
// начинает врать, а человек листает страницы за числом, которого нет. Ту же ошибку уже ловили на
// витрине открытого. Любой НОВЫЙ фильтр обязан дописываться в `where` ДО этой строки.
func (s *Store) ListFiles(ctx context.Context, f entity.LibraryFileListFilter) ([]entity.LibraryFile, int, error) {
	if len(f.TopicIds) > entity.MaxLibraryTopicFilters {
		return nil, 0, fmt.Errorf("%w: at most %d topics can be combined in one filter, got %d",
			entity.ErrLibraryBatchTooLarge, entity.MaxLibraryTopicFilters, len(f.TopicIds))
	}
	// НЕВОЗМОЖНЫЕ КОМБИНАЦИИ ОТВЕРГАЮТСЯ, А НЕ ИГНОРИРУЮТСЯ. Молча отброшенное плечо показало бы
	// БОЛЬШЕ, чем просили, а лишняя строка в этой библиотеке — это чьё-то говорящее имя файла.
	//
	// «Без роли» без проекта означало бы «почти вся библиотека» и не отвечало бы ни на один
	// вопрос. Проект и роль вместе с «разобрать» — прямое противоречие: файл в проекте несёт
	// строку связи, значит в «разобрать» его нет по построению, и пустая выдача читалась бы как
	// «в этом проекте ничего нет».
	if f.WithoutRole && f.ProjectTopicId <= 0 {
		return nil, 0, fmt.Errorf("%w: without_role is only meaningful together with a project", entity.ErrLibraryFilterInvalid)
	}
	if f.Untopiced && (f.ProjectTopicId > 0 || f.RoleId > 0 || f.WithoutRole) {
		return nil, 0, fmt.Errorf("%w: untopiced cannot be combined with a project or a role", entity.ErrLibraryFilterInvalid)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultPageLimit
	}
	limit = min(limit, maxPageLimit)
	offset := max(f.Offset, 0)
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, 0, err
	}

	// ТОЧКА 1 ПРЕДИКАТА, И ОН СТОИТ В ТОМ ЖЕ WHERE, ЧТО ФИЛЬТРЫ И ПОИСК — не отдельным проходом
	// и не UNION'ом. Иначе расширение поиска по автору (LIKE на uploaded_by ниже) вытащило бы
	// ограниченный файл мимо фильтра видимости: «что заливал Паша» показало бы имя файла, который
	// Паша от спрашивающего закрыл.
	where := []string{"1 = 1"}
	params := map[string]any{}
	where = append(where, v.Where("lf", params))
	switch {
	case f.Untopiced:
		where = append(where, `NOT EXISTS (SELECT 1 FROM library_file_topic lft WHERE lft.file_id = lf.id)`)
	case len(f.TopicIds) > 0:
		// Отдельный EXISTS НА КАЖДУЮ тему — это и есть пересечение. Через
		// `topic_id IN (...)` получилось бы ИЛИ: каждый следующий выбранный чип
		// РАСШИРЯЛ бы выдачу, а чипы нажимают ровно затем, чтобы её сузить.
		for i, id := range f.TopicIds {
			key := fmt.Sprintf("topicId%d", i)
			where = append(where, fmt.Sprintf(
				`EXISTS (SELECT 1 FROM library_file_topic lft%[1]d WHERE lft%[1]d.file_id = lf.id AND lft%[1]d.topic_id = :%[2]s)`,
				i, key))
			params[key] = id
		}
	case f.TopicId > 0:
		where = append(where, `EXISTS (SELECT 1 FROM library_file_topic lft WHERE lft.file_id = lf.id AND lft.topic_id = :topicId)`)
		params["topicId"] = f.TopicId
	}
	// ПРОЕКТ И РОЛЬ. Оба плеча дописываются В ТОТ ЖЕ where, что темы, поиск и предикат
	// видимости, — то есть складываются с ними пересечением и никогда не обходят предикат.
	//
	// «ПРОЕКТ × РОЛЬ» — ЭТО ОДИН EXISTS С ДВУМЯ УСЛОВИЯМИ НА ОДНОЙ СТРОКЕ, и в этом вся разница
	// между правильной моделью и молчаливо ложной. Два независимых EXISTS (один про проект,
	// другой про роль) — это в точности плоские метки: файл, лежащий в съёмке как «отобранное» и
	// в лукбуке как «референс», удовлетворил бы обоим, и запрос «съёмка × референс» нашёл бы его,
	// хотя референсом он был в лукбуке. Ошибка выглядит правдоподобно и проверить её нечем.
	//
	// Роль БЕЗ проекта — законный и другой вопрос: «все исходники по всем съёмкам». Там условие
	// одно, и строка, на которой оно выполняется, может быть любой.
	//
	// Предел 20 меряется по-прежнему ТОЛЬКО по TopicIds: проект и роль в него не входят, максимум
	// становится 21 EXISTS на запрос.
	switch {
	case f.ProjectTopicId > 0:
		params["projectTopicId"] = f.ProjectTopicId
		cond := `EXISTS (SELECT 1 FROM library_file_topic lft_proj
			WHERE lft_proj.file_id = lf.id AND lft_proj.topic_id = :projectTopicId`
		switch {
		case f.WithoutRole:
			cond += ` AND lft_proj.role_id IS NULL`
		case f.RoleId > 0:
			cond += ` AND lft_proj.role_id = :roleId`
			params["roleId"] = f.RoleId
		}
		where = append(where, cond+`)`)
	case f.RoleId > 0:
		where = append(where, `EXISTS (SELECT 1 FROM library_file_topic lft_role
			WHERE lft_role.file_id = lf.id AND lft_role.role_id = :roleId)`)
		params["roleId"] = f.RoleId
	}
	// ФИЛЬТР ПО ЧЕЛОВЕКУ. Он ДОПИСЫВАЕТСЯ в тот же where, что темы, поиск и предикат
	// видимости, — то есть складывается с ними пересечением и НИКОГДА не обходит предикат.
	// Отдельный проход (или UNION по двум ролям) вытащил бы мимо предиката ровно то, что
	// фильтр по человеку и ищет: файлы, которые этот человек закрыл от спрашивающего.
	//
	// ЖИВОЙ id, А НЕ СТРОКА ИМЕНИ. Строка `uploaded_by` переживает аккаунт, UNIQUE на
	// admins.username освобождает имя при удалении, и следующий однофамилец получил бы всю
	// историю прежнего. Ту же дыру уже закрывали дважды — в Ф3 (mayEditLibraryFileOwners) и в
	// предикате видимости (плечо 2). Здесь она закрыта тем же способом: сравнивается ссылка.
	//
	// `<=>`, А НЕ `=`: `uploaded_by_id` NULLable (уволенный загрузивший). В ЭТОЙ позиции
	// разницы в ответе нет — условие стоит в положительном WHERE, где NULL и так ведёт себя
	// как ложь, а :personId всегда > 0. Пишется NULL-safe сравнение ради одного: то же
	// выражение однажды окажется под отрицанием (так уже случилось с предикатом видимости в
	// bulk-проверке AssignTopics, где `NOT NULL` — это снова NULL, и невидимая строка не
	// считалась), и тогда правка «а тут можно проще» стоила бы молчаливой дыры.
	//
	// «Ведёт» — через EXISTS, а не JOIN: у файла несколько владельцев, и соединение
	// РАЗМНОЖИЛО БЫ строку файла по числу совпадений — страница отдала бы дубли, а total их
	// сосчитал бы.
	if f.PersonId > 0 {
		params["personId"] = f.PersonId
		uploaded := `lf.uploaded_by_id <=> :personId`
		owns := `EXISTS (SELECT 1 FROM library_file_owner lfo_person
			WHERE lfo_person.file_id = lf.id AND lfo_person.admin_id = :personId)`
		switch f.PersonRole {
		case entity.LibraryFilePersonRoleUploaded:
			where = append(where, uploaded)
		case entity.LibraryFilePersonRoleOwner:
			where = append(where, owns)
		default:
			// «Любая» = ИЛИ, и это единственное место во всей выборке, где ИЛИ уместно:
			// две роли — не два сужающих чипа, а два способа одному человеку числиться у
			// файла. Скобки обязательны — без них ИЛИ разошлось бы по всему AND-списку и
			// разнесло бы и предикат видимости, и фильтр тем.
			where = append(where, `(`+uploaded+` OR `+owns+`)`)
		}
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		// Matching topic names as well as file names is what makes a single input
		// enough: "фурнитура" has to find the topic even when not one file carries
		// that word in its name — and graphic PDFs have no extractable text at all,
		// so names and topics are all there is to match on. The uploader joins them
		// for the same reason: in a team of six "что заливал Паша" is a real query,
		// and a search narrower than its own label reads as broken.
		//
		// Расширение сидит ВНУТРИ того же предиката, что и остальная выборка, а не
		// отдельным UNION: иначе файл, скрытый фильтром видимости, всплыл бы в
		// поиске по автору — ровно та щель, через которую утекают имена.
		//
		// ИМЯ РОЛИ ИЩЕТСЯ ВТОРЫМ EXISTS'ОМ, И ЭТО НЕ ДОВЕСОК. Роль печатается на плитке рядом с
		// темой и выглядит ровно таким же ярлыком; человек, набравший «исходники», ждёт файлы, а
		// не пустой экран. До 0320 такой ярлык был темой и находился первым EXISTS'ом даром —
		// переезд роли в свою таблицу забрал бы это свойство молча.
		where = append(where, `(lf.file_name LIKE :search ESCAPE '\\' OR lf.uploaded_by LIKE :search ESCAPE '\\' OR EXISTS (
			SELECT 1 FROM library_file_topic lft
			JOIN file_topic ft ON ft.id = lft.topic_id
			WHERE lft.file_id = lf.id AND ft.name LIKE :search ESCAPE '\\') OR EXISTS (
			SELECT 1 FROM library_file_topic lft_rs
			JOIN file_role fr_rs ON fr_rs.id = lft_rs.role_id
			WHERE lft_rs.file_id = lf.id AND fr_rs.name LIKE :search ESCAPE '\\'))`)
		params["search"] = "%" + escapeLike(q) + "%"
	}
	clause := strings.Join(where, " AND ")

	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM library_file lf WHERE `+clause, params)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count library files: %w", err)
	}
	if total == 0 {
		return nil, 0, nil
	}

	// Newest first by default: a library is read from the most recent thing
	// backwards. id breaks ties so paging can never repeat or skip a row.
	//
	// order_factor управляет ТОЛЬКО хронологией. У имени и размера направление
	// зафиксировано (А→Я и «крупное сверху»), потому что обратное никому не нужно,
	// а два независимых контрола дали бы состояния вроде «по имени, но в обратную
	// сторону от того, что написано на кнопке».
	orderBy := ""
	switch f.SortBy {
	case entity.LibraryFileSortName:
		orderBy = "lf.file_name ASC, lf.id ASC"
	case entity.LibraryFileSortSize:
		orderBy = "lf.size_bytes DESC, lf.id DESC"
	default:
		direction := "DESC"
		if f.OrderFactor == entity.Ascending {
			direction = "ASC"
		}
		orderBy = "lf.created_at " + direction + ", lf.id " + direction
	}
	params["limit"] = limit
	params["offset"] = offset
	files, err := storeutil.QueryListNamed[entity.LibraryFile](ctx, s.DB,
		`SELECT lf.* FROM library_file lf WHERE `+clause+
			` ORDER BY `+orderBy+
			` LIMIT :limit OFFSET :offset`, params)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list library files: %w", err)
	}
	if err := s.attachRelatedSlice(ctx, files); err != nil {
		return nil, 0, err
	}
	return files, total, nil
}

// ListTopics returns the rail: every topic with how many files carry it, plus
// the two badges that would otherwise cost an extra round-trip each — how many
// files carry no topic at all, and how many files exist in total.
//
// ТОЧКА 5 ПРЕДИКАТА — ВСЕ ТРИ ЧИСЛА СЧИТАЮТСЯ ПОД НИМ, поэтому у разных людей рельс показывает
// РАЗНЫЕ числа. Это принято сознательно (контекст §3.4), и «починка» здесь ломает саму фазу:
// одинаковый у всех счётчик означал бы «в этой теме есть что-то, чего тебе не показывают», то
// есть ту же утечку, только выраженную числом.
//
// Предикат стоит в ON внешнего соединения, а не в WHERE: в WHERE он превратил бы LEFT JOIN в
// INNER и выкинул из рельса ПУСТЫЕ темы, которые обязаны быть видны (в них кладут новое).
//
// АРХИВ ЖЕ ФИЛЬТРУЕТСЯ ИМЕННО В WHERE, и это не противоречие, а разные условия: архив —
// свойство САМОЙ темы (и WHERE по ft ведущую таблицу не схлопывает), предикат — свойство файлов.
// Перепутать места здесь стоит ровно того, от чего предыдущий абзац предостерегает.
//
// includeArchived = false по умолчанию, и это единственное изменение поведения для уже
// отгруженного клиента: холст и пикеры архив не показывают, экран тем показывает. Пока никто
// ничего не заархивировал, изменение инертно.
func (s *Store) ListTopics(ctx context.Context, includeArchived bool) ([]entity.FileTopicWithCount, int, int, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, 0, 0, err
	}
	archived := "ft.archived_at IS NULL"
	if includeArchived {
		archived = "1 = 1"
	}
	// Ordering by usage, not alphabetically: dead topics sink on their own, and a
	// list of sixty becomes the eight that are actually in use.
	topicParams := map[string]any{}
	topics, err := storeutil.QueryListNamed[entity.FileTopicWithCount](ctx, s.DB, `
		SELECT ft.*, COUNT(lf.id) AS files_count
		FROM file_topic ft
		LEFT JOIN library_file_topic lft ON lft.topic_id = ft.id
		LEFT JOIN library_file lf ON lf.id = lft.file_id AND `+v.Where("lf", topicParams)+`
		WHERE `+archived+`
		GROUP BY ft.id
		ORDER BY files_count DESC, ft.name ASC`, topicParams)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to list file topics: %w", err)
	}
	untopicedParams := map[string]any{}
	untopiced, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM library_file lf
		WHERE NOT EXISTS (SELECT 1 FROM library_file_topic lft WHERE lft.file_id = lf.id)
		  AND `+v.Where("lf", untopicedParams), untopicedParams)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to count untopiced files: %w", err)
	}
	totalParams := map[string]any{}
	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM library_file lf WHERE `+v.Where("lf", totalParams), totalParams)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to count library files: %w", err)
	}
	return topics, untopiced, total, nil
}

// CreateTopic creates a topic, or returns the id of the existing one with that
// name (uniqueness is case-insensitive by collation, so "Brand" and "brand" are
// one topic).
func (s *Store) CreateTopic(ctx context.Context, name, description string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("topic name is empty")
	}
	id, err := upsertTopic(ctx, s.DB, name, description)
	if err != nil {
		return 0, fmt.Errorf("can't create file topic: %w", err)
	}
	return id, nil
}

// RenameTopic updates the name and the description together — one dialog edits
// both. Returns sql.ErrNoRows when no such topic exists.
func (s *Store) RenameTopic(ctx context.Context, id int, name, description string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("topic name is empty")
	}
	exists, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM file_topic WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("failed to check file topic existence: %w", err)
	}
	if exists == 0 {
		return sql.ErrNoRows
	}
	if err := storeutil.ExecNamed(ctx, s.DB,
		`UPDATE file_topic SET name = :name, description = :description WHERE id = :id`,
		map[string]any{"id": id, "name": name, "description": nullString(description)}); err != nil {
		return fmt.Errorf("can't rename file topic: %w", err)
	}
	return nil
}

// DeleteTopic removes a topic only while nothing carries it. Deleting a used one
// would silently unlabel its files and drop them into «Разобрать», which is the
// opposite of what the person meant.
//
// СЧЁТ ИДЁТ ПОД ПРЕДИКАТОМ ВИДИМОСТИ, тем же билдером Viewer.Where, что и рельс тем.
// Иначе тема, у которой ВСЁ содержимое невидимо смотрящему, отдаёт в рельс
// `files_count = 0`, а на удаление отвечает «topic still has files: 1» — то есть
// называет ЧИСЛО скрытых файлов ровно тем людям, от которых они скрыты. Счётчики тем
// сделаны персональными именно ради устранения этого сигнала, и отказ удаления не
// имеет права быть вторым его источником.
//
// ЧТО ОСТАЁТСЯ ЗА ПРЕДЕЛАМИ ПРАВКИ И ОСТАЁТСЯ СОЗНАТЕЛЬНО: тема, которую держат ТОЛЬКО
// невидимые файлы, чужому по-прежнему не удаляется — но упирается она теперь во внешний
// ключ RESTRICT, и хендлер переводит его в тот же FailedPrecondition БЕЗ числа
// (files_library.go, ветка IsErrForeignKeyViolation). Снимать связи невидимых файлов
// было бы хуже любого тупика: это молча разметило бы чужие файлы.
// ТРАНЗАКЦИЯ ПОЯВИЛАСЬ ВМЕСТЕ С 0321, И БЕЗ НЕЁ УДАЛЕНИЕ СТАЛО БЫ ТЕРЯТЬ ДАННЫЕ МОЛЧА. Для
// файлов страховкой от гонки «посчитали ноль — коллега привязал — удалили» служил внешний ключ
// RESTRICT: DELETE упирался в 1452, и хендлер отвечал FailedPrecondition. У связи со стилем
// ключ КАСКАДНЫЙ, поэтому той же гонке упереться не во что — привязка, заведённая между
// проверкой и удалением, погибла бы в тот же миг, как появилась, и никто бы этого не заметил.
// Пишущие транзакции стора идут в SERIALIZABLE, поэтому чтение внутри транзакции реально
// запирает диапазон, а не просто сужает окно.
//
// СТИЛИ НЕ ЗАПРЕЩАЮТ УДАЛЕНИЕ, НО СЧИТАЮТСЯ И ВОЗВРАЩАЮТСЯ. Отказ здесь был бы тупиком: число
// привязанных стилей на экране тем не показано вовсе, и человек упёрся бы в «нельзя» без
// единого способа увидеть, во что именно. А молчание — это ровно тот дефект, ради которого
// понижение проекта возвращает ClearedRoles/ClearedStyles: «убрал пустую съёмку с глаз» и «у
// восьми вещей пропал ответ, каким файлом их сделали» обязаны быть одним и тем же событием на
// экране, а не двумя, между которыми месяц.
func (s *Store) DeleteTopic(ctx context.Context, id int) (int, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return 0, err
	}
	var unlinkedStyles int
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		params := map[string]any{"id": id}
		used, err := storeutil.QueryCountNamed(ctx, rep.DB(), `
			SELECT COUNT(*) FROM library_file_topic lft
			JOIN library_file lf ON lf.id = lft.file_id
			WHERE lft.topic_id = :id AND `+v.Where("lf", params), params)
		if err != nil {
			return fmt.Errorf("failed to count files in topic: %w", err)
		}
		if used > 0 {
			return entity.NewErrFileTopicInUse(used)
		}
		// Считаем ДО удаления темы: каскад унесёт строки вместе с ней, и после DELETE считать
		// будет нечего. Число ТОЧНОЕ, без предиката видимости, — стиль не файл библиотеки, он
		// живёт под собственным RBAC секции techcards.
		styles, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM file_topic_tech_card WHERE topic_id = :id`,
			map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to count styles linked to a topic: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM file_topic WHERE id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("can't delete file topic: %w", err)
		}
		unlinkedStyles = styles
		return nil
	})
	if err != nil {
		return 0, err // ErrFileTopicInUse и отказ внешнего ключа доезжают нетронутыми
	}
	return unlinkedStyles, nil
}

// MergeTopics folds source into target and deletes source, returning how many
// files gained the target topic. It is the only way out of a duplicated label
// ("бирки" next to "бирка"): DeleteTopic refuses on a topic that is in use, which
// is precisely the topic somebody wants merged.
//
// Одной транзакцией, потому что промежуточное состояние здесь наблюдаемо и
// разрушительно: связи источника уже сняты, а на цель ещё не перевешены — файлы
// на это мгновение теряют тему совсем и уезжают в «Разобрать».
//
// ПЕРЕВЕШИВАЕТСЯ ВСЁ, А В ОТЧЁТ ИДЁТ ВИДИМОЕ. Слияние — операция над ЯРЛЫКОМ, и оставить
// невидимый файл висеть на удаляемой теме значило бы уронить её удаление о собственный
// внешний ключ. А вот возвращаемое число читает человек, и «переехало 7» на теме, в
// которой он видит два файла, — тот же самый сигнал «здесь есть что-то, чего тебе не
// показывают», от которого ушли персональные счётчики (и DeleteTopic выше). Поэтому
// moved считается ПОД предикатом, тем же билдером.
func (s *Store) MergeTopics(ctx context.Context, sourceID, targetID int) (int, error) {
	if sourceID == targetID {
		// Бэкстоп: хендлер отвечает на это InvalidArgument раньше. Молчаливый
		// no-op был бы хуже — слияние необратимо, и «готово» на бессмысленный
		// запрос убеждает человека, что он сделал то, чего не делал.
		return 0, fmt.Errorf("cannot merge a topic into itself")
	}
	v, err := s.viewer(ctx)
	if err != nil {
		return 0, err
	}
	var moved int
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Существование проверяем ВНУТРИ транзакции: пишущие транзакции стора идут
		// в SERIALIZABLE, поэтому проверка реально закрывает гонку с параллельным
		// удалением темы, а не просто сужает окно.
		type topicKindRow struct {
			Id   int                  `db:"id"`
			Kind entity.FileTopicKind `db:"kind"`
		}
		found, err := storeutil.QueryListNamed[topicKindRow](ctx, rep.DB(),
			`SELECT id, kind FROM file_topic WHERE id IN (:ids)`,
			map[string]any{"ids": []int{sourceID, targetID}})
		if err != nil {
			return fmt.Errorf("failed to check file topics existence: %w", err)
		}
		if len(found) != 2 {
			return sql.ErrNoRows
		}
		// СЛИЯНИЕ МЕЖДУ ТИПАМИ ЗАПРЕЩЕНО. Слить проект в обычный ярлык — не редкий случай, а
		// бессмыслица: роли, живущие на строках связи источника, переехали бы на строки темы,
		// которая проектом не является, и получилось бы состояние, которого стор больше нигде
		// не допускает (роль ставится только внутри проекта). Обратное направление ничем не
		// лучше: даты и разбивка цели остались бы, а половина файлов пришла бы без ролей и
		// навсегда осела в «без роли», не отличимая от честного приёмника.
		if found[0].Kind != found[1].Kind {
			return entity.ErrFileTopicKindMismatch
		}
		// Число снимается ДО вставки и повторяет её условие слово в слово: «связь источника
		// есть, связи цели ещё нет» — то самое, что INSERT IGNORE реально вставит, — плюс
		// предикат видимости. Файл, уже несущий обе темы, в отчёт не попадает, потому что он
		// и не переехал.
		movedParams := map[string]any{"source": sourceID, "target": targetID}
		visibleMoved, err := storeutil.QueryCountNamed(ctx, rep.DB(), `
			SELECT COUNT(*) FROM library_file_topic lft
			JOIN library_file lf ON lf.id = lft.file_id
			WHERE lft.topic_id = :source
			  AND NOT EXISTS (SELECT 1 FROM library_file_topic done
					WHERE done.file_id = lft.file_id AND done.topic_id = :target)
			  AND `+v.Where("lf", movedParams), movedParams)
		if err != nil {
			return fmt.Errorf("failed to count visible files moved between topics: %w", err)
		}
		// РОЛЬ ЕДЕТ ТРЕТЬЕЙ КОЛОНКОЙ, И БЕЗ НЕЁ СЛИЯНИЕ ПРОЕКТОВ ТЕРЯЕТ ВСЮ РАЗМЕТКУ. «Две
		// съёмки оказались одной» — штатный сценарий; проекция без role_id перевесила бы файлы
		// на целевой проект, обнулив то, чем они в нём являются, и восстановить это было бы
		// нечем: исходный проект в той же транзакции перестаёт существовать.
		//
		// Столкновение (файл уже лежал в цели) гасит INSERT IGNORE: побеждает роль, которая
		// СТОЯЛА В ЦЕЛЕВОМ проекте. Это единственный разумный выбор — целевой проект переживает
		// слияние, и его собственная разметка старше приезжей, — но человека о нём надо
		// предупредить в диалоге, рядом с уже стоящим «обратно это не разбирается».
		if _, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			INSERT IGNORE INTO library_file_topic (file_id, topic_id, role_id)
			SELECT lft.file_id, :target, lft.role_id FROM library_file_topic lft WHERE lft.topic_id = :source`,
			map[string]any{"source": sourceID, "target": targetID}); err != nil {
			return fmt.Errorf("failed to move file topic links: %w", err)
		}
		moved = visibleMoved
		// ПРИВЯЗКИ СТИЛЕЙ ПЕРЕЕЗЖАЮТ ТОЖЕ (0321), И БЕЗ ЭТОЙ СТРОКИ ОНИ ИСЧЕЗЛИ БЫ МОЛЧА.
		// Внешний ключ file_topic_tech_card.topic_id стоит с ON DELETE CASCADE, поэтому
		// DELETE темы-источника ниже унёс бы её связи со стилями БЕЗ единого отказа — и
		// «две съёмки оказались одной» стёрло бы ответ на «каким файлом сделана эта вещь»
		// у всех вещей исходного проекта. Столкновение (стиль уже привязан к цели) гасит
		// INSERT IGNORE: связь — голый факт без свойств, поэтому какая из двух строк уцелеет,
		// не наблюдаемо ничем, кроме created_at.
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			INSERT IGNORE INTO file_topic_tech_card (topic_id, tech_card_id)
			SELECT :target, ftc.tech_card_id FROM file_topic_tech_card ftc WHERE ftc.topic_id = :source`,
			map[string]any{"source": sourceID, "target": targetID}); err != nil {
			return fmt.Errorf("failed to move project style links: %w", err)
		}
		// Связи источника снимаем ДО удаления самой темы: внешний ключ на тему
		// стоит без каскада (RESTRICT), иначе DELETE упал бы о собственные связи.
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM library_file_topic WHERE topic_id = :source`,
			map[string]any{"source": sourceID}); err != nil {
			return fmt.Errorf("failed to drop source topic links: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM file_topic WHERE id = :source`, map[string]any{"source": sourceID}); err != nil {
			return fmt.Errorf("failed to delete source topic: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err // sql.ErrNoRows passes through untouched
	}
	return moved, nil
}

// AssignTopics ADDS topics to a set of files and returns how many links were
// created. Additive on purpose — see the rpc comment: a bulk write has not seen
// the labels it would be replacing.
func (s *Store) AssignTopics(ctx context.Context, fileIDs, topicIDs []int, newTopics []string) (int, error) {
	if len(fileIDs) == 0 {
		return 0, nil
	}
	if len(fileIDs) > maxPageLimit {
		return 0, fmt.Errorf("%w: at most %d files can be labelled in one call, got %d",
			entity.ErrLibraryBatchTooLarge, maxPageLimit, len(fileIDs))
	}
	v, err := s.viewer(ctx)
	if err != nil {
		return 0, err
	}
	var assigned int
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// ТОЧКА 10 ПРЕДИКАТА, СЕМАНТИКА ПАЧКИ: ОДИН невидимый id отказывает ВСЕЙ пачке.
		//
		// Частичное применение отвечало бы на видимый и невидимый id по-разному — «проставилось
		// 4 из 5» и есть подтверждение, что пятый файл существует. Отказ на всю пачку не
		// различает их вовсе.
		//
		// Считается именно НЕВИДИМОЕ (условие под отрицанием), а не «сколько видимо»: файл,
		// УДАЛЁННЫЙ между загрузкой сетки и нажатием кнопки, обязан остаться тем, чем был до
		// предиката, — просто отсутствующей строкой, из-за которой пачка не падает (см. довод у
		// кросс-джойна ниже). Невидимый и удалённый — разные вещи ровно здесь и больше нигде.
		invisibleParams := map[string]any{"fileIds": fileIDs}
		invisible, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM library_file lf WHERE lf.id IN (:fileIds) AND NOT (`+
				v.Where("lf", invisibleParams)+`)`, invisibleParams)
		if err != nil {
			return fmt.Errorf("failed to check library files visibility: %w", err)
		}
		if invisible > 0 {
			return sql.ErrNoRows
		}
		ids := make(map[int]struct{}, len(topicIDs)+len(newTopics))
		for _, id := range topicIDs {
			if id > 0 {
				ids[id] = struct{}{}
			}
		}
		if len(ids) > 0 {
			// Проверка существования тем обязательна ИМЕННО из-за INSERT IGNORE:
			// IGNORE глушит и нарушение внешнего ключа тоже, так что несуществующая
			// тема без этой проверки превратилась бы в «ничего не проставилось» с
			// ответом «готово».
			list := make([]int, 0, len(ids))
			for id := range ids {
				list = append(list, id)
			}
			found, err := storeutil.QueryCountNamed(ctx, rep.DB(),
				`SELECT COUNT(*) FROM file_topic WHERE id IN (:ids)`, map[string]any{"ids": list})
			if err != nil {
				return fmt.Errorf("failed to check file topics existence: %w", err)
			}
			if found != len(list) {
				return sql.ErrNoRows
			}
		}
		for _, name := range newTopics {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			id, err := upsertTopic(ctx, rep.DB(), name, "")
			if err != nil {
				return fmt.Errorf("failed to resolve topic %q: %w", name, err)
			}
			ids[id] = struct{}{}
		}
		if len(ids) == 0 {
			return nil
		}
		list := make([]int, 0, len(ids))
		for id := range ids {
			list = append(list, id)
		}
		// Кросс-джойн двух наборов id вместо перечисления пар в VALUES: файл,
		// удалённый между загрузкой сетки и нажатием кнопки, просто не даёт строк,
		// и вся пачка не падает из-за одного исчезнувшего файла.
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			INSERT IGNORE INTO library_file_topic (file_id, topic_id)
			SELECT lf.id, ft.id
			FROM library_file lf
			CROSS JOIN file_topic ft
			WHERE lf.id IN (:fileIds) AND ft.id IN (:topicIds)`,
			map[string]any{"fileIds": fileIDs, "topicIds": list})
		if err != nil {
			return fmt.Errorf("failed to assign file topics: %w", err)
		}
		assigned = int(rows)
		return nil
	})
	if err != nil {
		return 0, err // sql.ErrNoRows passes through untouched
	}
	return assigned, nil
}

// SetFilePreview points the file at a new preview object and returns the key of
// the one it replaced, so the caller can drop the now-unreachable bytes. An empty
// key clears the preview.
//
// Читаем старый ключ и пишем новый одной транзакцией: две одновременные
// перезаливки иначе прочитали бы один и тот же старый ключ, и вторая удалила бы
// байты, на которые уже указывает первая.
//
// ТОЧКА 13 ПРЕДИКАТА, И ОНА ЗАКРЫВАЕТСЯ ЗДЕСЬ ЦЕЛИКОМ. HTTP-довесок POST /api/files/{id}/preview
// в карте RPC не значится и о правах ничего не знает — он лишь переводит sql.ErrNoRows отсюда в
// 404, поэтому предикат внутри этого чтения и есть вся проверка. Побочный факт (Ф7): байты
// превью уезжают в бакет ДО этого вызова и подчищаются после отказа — невидимый файл стоит
// впустую загруженной картинки, но наружу не выдаёт ничего.
func (s *Store) SetFilePreview(ctx context.Context, id int, previewKey string) (string, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return "", err
	}
	var previous string
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		params := map[string]any{"id": id}
		f, err := storeutil.QueryNamedOne[entity.LibraryFile](ctx, rep.DB(),
			`SELECT lf.* FROM library_file lf WHERE lf.id = :id AND `+v.Where("lf", params), params)
		if err != nil {
			return err // sql.ErrNoRows passes through untouched
		}
		if f.PreviewObjectKey.Valid && f.PreviewObjectKey.String != previewKey {
			previous = f.PreviewObjectKey.String
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`UPDATE library_file SET preview_object_key = :key WHERE id = :id`,
			map[string]any{"id": id, "key": nullString(previewKey)}); err != nil {
			return fmt.Errorf("failed to update library file preview: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return previous, nil
}

// resolveTopicIDs turns the (ids, names typed on the fly) pair into the concrete
// set of topic ids, creating the new names as it goes.
func resolveTopicIDs(ctx context.Context, db dependency.DB, topicIDs []int, newTopics []string) (map[int]struct{}, error) {
	ids := make(map[int]struct{}, len(topicIDs)+len(newTopics))
	for _, id := range topicIDs {
		if id > 0 {
			ids[id] = struct{}{}
		}
	}
	for _, name := range newTopics {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		id, err := upsertTopic(ctx, db, name, "")
		if err != nil {
			return nil, fmt.Errorf("failed to resolve topic %q: %w", name, err)
		}
		ids[id] = struct{}{}
	}
	return ids, nil
}

// insertTopicLinks writes the given (file, topic) rows.
//
// Обычный INSERT, а НЕ `INSERT IGNORE`, и это существенно: IGNORE глушит и нарушение внешнего
// ключа тоже, поэтому несуществующая тема превратилась бы в «ничего не проставилось» с ответом
// «готово». Вызывающие рассчитывают на то, что ошибка внешнего ключа доедет до хендлера и станет
// InvalidArgument.
func insertTopicLinks(ctx context.Context, db dependency.DB, fileID int, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, map[string]any{"file_id": fileID, "topic_id": id})
	}
	if err := storeutil.BulkInsert(ctx, db, "library_file_topic", rows); err != nil {
		return fmt.Errorf("failed to link file topics: %w", err)
	}
	return nil
}

// linkTopics attaches an explicit id set plus any names typed on the fly. Used on
// the CREATE paths (upload, note), where there is nothing to diff against.
func linkTopics(ctx context.Context, db dependency.DB, fileID int, topicIDs []int, newTopics []string) error {
	ids, err := resolveTopicIDs(ctx, db, topicIDs, newTopics)
	if err != nil {
		return err
	}
	list := make([]int, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	return insertTopicLinks(ctx, db, fileID, list)
}

// syncTopics brings the file's topic set to exactly the requested one BY
// DIFFERENCE: only the departed links are removed, only the new ones inserted,
// and the surviving rows are never touched.
//
// «Не трогать уцелевшие» — это и есть весь смысл функции. На строке связи с 0320 живёт роль файла
// в проекте; снеси мы весь набор и заведи заново, роль исчезала бы при каждом сохранении карточки
// — молча, без единого сообщения, при обычном переименовании файла. Читается это как «роли иногда
// пропадают сами», и найти такое по симптому почти невозможно.
//
// Читаем текущий набор ВНУТРИ транзакции (пишущие транзакции стора идут в SERIALIZABLE), поэтому
// разница считается по состоянию, которое никто не переписывает под руками.
func syncTopics(ctx context.Context, db dependency.DB, fileID int, topicIDs []int, newTopics []string) error {
	want, err := resolveTopicIDs(ctx, db, topicIDs, newTopics)
	if err != nil {
		return err
	}
	current, err := storeutil.QueryScalarListNamed[int](ctx, db,
		`SELECT topic_id FROM library_file_topic WHERE file_id = :id`, map[string]any{"id": fileID})
	if err != nil {
		return fmt.Errorf("failed to read current file topics: %w", err)
	}
	have := make(map[int]struct{}, len(current))
	remove := make([]int, 0, len(current))
	for _, id := range current {
		have[id] = struct{}{}
		if _, keep := want[id]; !keep {
			remove = append(remove, id)
		}
	}
	add := make([]int, 0, len(want))
	for id := range want {
		if _, already := have[id]; !already {
			add = append(add, id)
		}
	}
	if len(remove) > 0 {
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM library_file_topic WHERE file_id = :id AND topic_id IN (:removed)`,
			map[string]any{"id": fileID, "removed": remove}); err != nil {
			return fmt.Errorf("failed to drop departed file topics: %w", err)
		}
	}
	return insertTopicLinks(ctx, db, fileID, add)
}

// upsertTopic returns the id of the topic with this name, creating it when it is
// new. INSERT ... ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id) makes this
// race-free without inspecting driver error codes: two uploads naming the same
// new topic in the same second both get the same id instead of one of them
// dying on 1062.
func upsertTopic(ctx context.Context, db dependency.DB, name, description string) (int, error) {
	return storeutil.ExecNamedLastId(ctx, db, `
		INSERT INTO file_topic (name, description) VALUES (:name, :description)
		ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
		map[string]any{"name": name, "description": nullString(description)})
}

// attachTopics resolves the label set of the given files in one query — AND their
// (project → role) pairs out of the same rows, without a second round-trip.
//
// Обе половины приезжают из ОДНОГО запроса намеренно: роль живёт на той же строке связи, что и
// тема, и отдельный запрос за ролями означал бы, что однажды один из двух путей чтения приедет с
// темами и без ролей — на одном экране у файла будет проект, на соседнем нет.
//
// В список ролей попадают только строки с непустым role_id. Проверять при этом, что тема —
// проект, не нужно: непустой role_id может стоять только внутри проекта (SetFileRoles отказывает
// на обычной теме, понижение проекта обнуляет роли, слияние разнотипных тем запрещено).
func (s *Store) attachTopics(ctx context.Context, files []*entity.LibraryFile) error {
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
		entity.FileTopic
		// COALESCE, а не sql.Null*: ноль и пустая строка здесь означают ровно «роли на этой
		// строке нет», и отличать их от NULL незачем — читатель проверяет одно и то же условие.
		RoleId   int    `db:"role_id"`
		RoleName string `db:"role_name"`
	}
	rows, err := storeutil.QueryListNamed[row](ctx, s.DB, `
		SELECT lft.file_id, ft.id, ft.name, ft.description, ft.created_at,
			ft.kind, ft.starts_at, ft.ends_at, ft.archived_at,
			COALESCE(lft.role_id, 0) AS role_id, COALESCE(fr.name, '') AS role_name
		FROM library_file_topic lft
		JOIN file_topic ft ON ft.id = lft.topic_id
		LEFT JOIN file_role fr ON fr.id = lft.role_id
		WHERE lft.file_id IN (:ids)
		ORDER BY ft.name`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("failed to load file topics: %w", err)
	}
	for _, r := range rows {
		f, ok := byId[r.FileId]
		if !ok {
			continue
		}
		f.Topics = append(f.Topics, r.FileTopic)
		if r.RoleId > 0 {
			f.Roles = append(f.Roles, entity.LibraryFileRoleRef{
				ProjectTopicId:   r.FileTopic.Id,
				ProjectTopicName: r.FileTopic.Name,
				RoleId:           r.RoleId,
				RoleName:         r.RoleName,
			})
		}
	}
	return nil
}

// attachOwners resolves the owners of the given files in TWO queries total,
// whatever the page size: one for the (file → person) links, one for the
// specialties of everybody who turned up. The shape is deliberately the same as
// attachTopics — a per-file lookup here would be one round-trip per tile, and the
// grid draws two hundred tiles.
func (s *Store) attachOwners(ctx context.Context, files []*entity.LibraryFile) error {
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
		entity.AdminRef
	}
	rows, err := storeutil.QueryListNamed[row](ctx, s.DB, `
		SELECT lfo.file_id, a.id, a.username, a.is_super
		FROM library_file_owner lfo
		JOIN admins a ON a.id = lfo.admin_id
		WHERE lfo.file_id IN (:ids)
		ORDER BY a.username`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("failed to load library file owners: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	adminIDs := make([]int, 0, len(rows))
	seen := make(map[int]bool, len(rows))
	for _, r := range rows {
		if !seen[r.Id] {
			seen[r.Id] = true
			adminIDs = append(adminIDs, r.Id)
		}
	}
	// Один и тот же человек ведёт несколько файлов страницы — его специальности
	// запрашиваются ОДИН раз на всю страницу, а не по разу на каждое владение.
	specialties, err := storeutil.LoadAdminSpecialties(ctx, s.DB, adminIDs)
	if err != nil {
		return err
	}
	for _, r := range rows {
		f, ok := byId[r.FileId]
		if !ok {
			continue
		}
		owner := r.AdminRef
		owner.Specialties = specialties[r.Id]
		f.Owners = append(f.Owners, owner)
	}
	return nil
}

// SetFileOwners REPLACES the file's owner set. Returns sql.ErrNoRows when the
// file does not exist, so a card open on a deleted file says so instead of
// reporting a successful write into nothing.
//
// Замена, а не дельта: владельцев единицы, и пикер показал вызывающему ВЕСЬ
// текущий набор перед правкой — в отличие от массового проставления тем, которое
// набора не видело и потому только дописывает.
// ТОЧКА 10 ПРЕДИКАТА (запись): назначить владельцев невидимому файлу нельзя. Иначе любой с
// files:write вписал бы себя во владельцы чужого ограниченного файла и тем же движением ОТКРЫЛ
// БЫ СЕБЕ ДОСТУП — четвёртое плечо предиката ровно про владельцев.
func (s *Store) SetFileOwners(ctx context.Context, fileID int, adminIDs []int, addedBy string) error {
	v, err := s.viewer(ctx)
	if err != nil {
		return err
	}
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Существование И видимость проверяем ВНУТРИ транзакции: пишущие транзакции стора идут в
		// SERIALIZABLE, поэтому проверка реально закрывает гонку с удалением файла, а
		// не просто сужает окно.
		if err := EnsureVisible(ctx, rep.DB(), v, fileID); err != nil {
			return err // sql.ErrNoRows нетронутым
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM library_file_owner WHERE file_id = :id`, map[string]any{"id": fileID}); err != nil {
			return fmt.Errorf("failed to clear library file owners: %w", err)
		}
		if len(adminIDs) == 0 {
			return nil
		}
		// Несуществующий аккаунт обязан УПАСТЬ внешним ключом, а не пропасть тихо:
		// «владелец назначен» на пустое место — худший из возможных ответов, потому
		// что спрашивать по такому файлу будет некого, а карточка скажет, что есть кого.
		rows := make([]map[string]any, 0, len(adminIDs))
		for _, id := range adminIDs {
			rows = append(rows, map[string]any{"file_id": fileID, "admin_id": id, "added_by": addedBy})
		}
		if err := storeutil.BulkInsert(ctx, rep.DB(), "library_file_owner", rows); err != nil {
			return fmt.Errorf("failed to link library file owners: %w", err)
		}
		return nil
	})
	if err != nil {
		return err // sql.ErrNoRows passes through untouched
	}
	return nil
}

// attachRelated resolves everything a file carries beyond its own row: topic
// labels, owners and the size of its discussion. Читающие пути зовут ЕГО, а не три функции по
// отдельности — иначе новый путь чтения однажды приедет с темами и без владельцев, и на одном
// экране у файла будет ответственный, а на соседнем нет.
//
// Счётчик реплик стоит ЗДЕСЬ (а не в конвертере и не в хендлере) по тому же доводу: он обязан
// приезжать на файл всюду, где приезжают темы, — иначе плитка витрины доступа выглядела бы иначе,
// чем та же плитка в сетке. Пока этой строки не было, comments_count ехал на провод нулём при
// полностью рабочем конвертере (Ф5 не могла её вписать: файл держался в карантине под предикат).
func (s *Store) attachRelated(ctx context.Context, files []*entity.LibraryFile) error {
	if err := s.attachTopics(ctx, files); err != nil {
		return err
	}
	if err := s.attachOwners(ctx, files); err != nil {
		return err
	}
	return s.AttachCommentsCount(ctx, files)
}

func (s *Store) attachRelatedSlice(ctx context.Context, files []entity.LibraryFile) error {
	ptrs := make([]*entity.LibraryFile, len(files))
	for i := range files {
		ptrs[i] = &files[i]
	}
	return s.attachRelated(ctx, ptrs)
}

// escapeLike neutralises the LIKE wildcards in user input, so searching for
// "50%" looks for that literal string rather than matching everything.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func nullString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
