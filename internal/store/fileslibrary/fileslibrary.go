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
func (s *Store) UpdateFile(ctx context.Context, id int, fileName string, topicIDs []int, newTopics []string) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE library_file SET file_name = :fileName WHERE id = :id`,
			map[string]any{"id": id, "fileName": fileName})
		if err != nil {
			return fmt.Errorf("failed to update library file: %w", err)
		}
		if rows == 0 {
			// Rows-affected is 0 both for "no such row" and for "same name again";
			// disambiguate rather than reporting a spurious not-found.
			exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
				`SELECT COUNT(*) FROM library_file WHERE id = :id`, map[string]any{"id": id})
			if err != nil {
				return fmt.Errorf("failed to check library file existence: %w", err)
			}
			if exists == 0 {
				return sql.ErrNoRows
			}
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM library_file_topic WHERE file_id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear library file topics: %w", err)
		}
		return linkTopics(ctx, rep.DB(), id, topicIDs, newTopics)
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
func (s *Store) DeleteFile(ctx context.Context, id int) ([]string, error) {
	var keys []string
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
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
func (s *Store) GetFileById(ctx context.Context, id int) (*entity.LibraryFile, error) {
	f, err := storeutil.QueryNamedOne[entity.LibraryFile](ctx, s.DB,
		`SELECT * FROM library_file WHERE id = :id`, map[string]any{"id": id})
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
func (s *Store) ListFilesByIds(ctx context.Context, ids []int) ([]entity.LibraryFile, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	files, err := storeutil.QueryListNamed[entity.LibraryFile](ctx, s.DB,
		`SELECT * FROM library_file WHERE id IN (:ids)`, map[string]any{"ids": ids})
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
func (s *Store) FindFilesBySha256(ctx context.Context, sha256 string) ([]entity.LibraryFile, error) {
	if sha256 == "" {
		return nil, nil
	}
	files, err := storeutil.QueryListNamed[entity.LibraryFile](ctx, s.DB,
		`SELECT * FROM library_file WHERE sha256 = :sha ORDER BY id`,
		map[string]any{"sha": sha256})
	if err != nil {
		return nil, fmt.Errorf("failed to find library files by sha256: %w", err)
	}
	return files, nil
}

// ListFiles returns a page of the library plus the total matching count.
func (s *Store) ListFiles(ctx context.Context, f entity.LibraryFileListFilter) ([]entity.LibraryFile, int, error) {
	if len(f.TopicIds) > entity.MaxLibraryTopicFilters {
		return nil, 0, fmt.Errorf("at most %d topics can be combined in one filter, got %d",
			entity.MaxLibraryTopicFilters, len(f.TopicIds))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultPageLimit
	}
	limit = min(limit, maxPageLimit)
	offset := max(f.Offset, 0)

	where := []string{"1 = 1"}
	params := map[string]any{}
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
		where = append(where, `(lf.file_name LIKE :search ESCAPE '\\' OR lf.uploaded_by LIKE :search ESCAPE '\\' OR EXISTS (
			SELECT 1 FROM library_file_topic lft
			JOIN file_topic ft ON ft.id = lft.topic_id
			WHERE lft.file_id = lf.id AND ft.name LIKE :search ESCAPE '\\'))`)
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
func (s *Store) ListTopics(ctx context.Context) ([]entity.FileTopicWithCount, int, int, error) {
	// Ordering by usage, not alphabetically: dead topics sink on their own, and a
	// list of sixty becomes the eight that are actually in use.
	topics, err := storeutil.QueryListNamed[entity.FileTopicWithCount](ctx, s.DB, `
		SELECT ft.*, COUNT(lft.file_id) AS files_count
		FROM file_topic ft
		LEFT JOIN library_file_topic lft ON lft.topic_id = ft.id
		GROUP BY ft.id
		ORDER BY files_count DESC, ft.name ASC`, map[string]any{})
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to list file topics: %w", err)
	}
	untopiced, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM library_file lf
		WHERE NOT EXISTS (SELECT 1 FROM library_file_topic lft WHERE lft.file_id = lf.id)`,
		map[string]any{})
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to count untopiced files: %w", err)
	}
	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM library_file`, map[string]any{})
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
func (s *Store) DeleteTopic(ctx context.Context, id int) error {
	used, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM library_file_topic WHERE topic_id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("failed to count files in topic: %w", err)
	}
	if used > 0 {
		return entity.NewErrFileTopicInUse(used)
	}
	if err := storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM file_topic WHERE id = :id`, map[string]any{"id": id}); err != nil {
		return fmt.Errorf("can't delete file topic: %w", err)
	}
	return nil
}

// MergeTopics folds source into target and deletes source, returning how many
// files gained the target topic. It is the only way out of a duplicated label
// ("бирки" next to "бирка"): DeleteTopic refuses on a topic that is in use, which
// is precisely the topic somebody wants merged.
//
// Одной транзакцией, потому что промежуточное состояние здесь наблюдаемо и
// разрушительно: связи источника уже сняты, а на цель ещё не перевешены — файлы
// на это мгновение теряют тему совсем и уезжают в «Разобрать».
func (s *Store) MergeTopics(ctx context.Context, sourceID, targetID int) (int, error) {
	if sourceID == targetID {
		// Бэкстоп: хендлер отвечает на это InvalidArgument раньше. Молчаливый
		// no-op был бы хуже — слияние необратимо, и «готово» на бессмысленный
		// запрос убеждает человека, что он сделал то, чего не делал.
		return 0, fmt.Errorf("cannot merge a topic into itself")
	}
	var moved int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Существование проверяем ВНУТРИ транзакции: пишущие транзакции стора идут
		// в SERIALIZABLE, поэтому проверка реально закрывает гонку с параллельным
		// удалением темы, а не просто сужает окно.
		found, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM file_topic WHERE id IN (:ids)`,
			map[string]any{"ids": []int{sourceID, targetID}})
		if err != nil {
			return fmt.Errorf("failed to check file topics existence: %w", err)
		}
		if found != 2 {
			return sql.ErrNoRows
		}
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			INSERT IGNORE INTO library_file_topic (file_id, topic_id)
			SELECT lft.file_id, :target FROM library_file_topic lft WHERE lft.topic_id = :source`,
			map[string]any{"source": sourceID, "target": targetID})
		if err != nil {
			return fmt.Errorf("failed to move file topic links: %w", err)
		}
		// INSERT IGNORE считает только реально вставленные строки, поэтому файл,
		// уже несущий обе темы, в отчёт не попадает — а он и не переехал.
		moved = int(rows)
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
		return 0, fmt.Errorf("at most %d files can be labelled in one call, got %d", maxPageLimit, len(fileIDs))
	}
	var assigned int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
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
func (s *Store) SetFilePreview(ctx context.Context, id int, previewKey string) (string, error) {
	var previous string
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		f, err := storeutil.QueryNamedOne[entity.LibraryFile](ctx, rep.DB(),
			`SELECT * FROM library_file WHERE id = :id`, map[string]any{"id": id})
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

// linkTopics attaches an explicit id set plus any names typed on the fly.
func linkTopics(ctx context.Context, db dependency.DB, fileID int, topicIDs []int, newTopics []string) error {
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
			return fmt.Errorf("failed to resolve topic %q: %w", name, err)
		}
		ids[id] = struct{}{}
	}
	if len(ids) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(ids))
	for id := range ids {
		rows = append(rows, map[string]any{"file_id": fileID, "topic_id": id})
	}
	if err := storeutil.BulkInsert(ctx, db, "library_file_topic", rows); err != nil {
		return fmt.Errorf("failed to link file topics: %w", err)
	}
	return nil
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

// attachTopics resolves the label set of the given files in one query.
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
	}
	rows, err := storeutil.QueryListNamed[row](ctx, s.DB, `
		SELECT lft.file_id, ft.id, ft.name, ft.description, ft.created_at
		FROM library_file_topic lft
		JOIN file_topic ft ON ft.id = lft.topic_id
		WHERE lft.file_id IN (:ids)
		ORDER BY ft.name`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("failed to load file topics: %w", err)
	}
	for _, r := range rows {
		if f, ok := byId[r.FileId]; ok {
			f.Topics = append(f.Topics, r.FileTopic)
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
func (s *Store) SetFileOwners(ctx context.Context, fileID int, adminIDs []int, addedBy string) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Существование проверяем ВНУТРИ транзакции: пишущие транзакции стора идут в
		// SERIALIZABLE, поэтому проверка реально закрывает гонку с удалением файла, а
		// не просто сужает окно.
		exists, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM library_file WHERE id = :id`, map[string]any{"id": fileID})
		if err != nil {
			return fmt.Errorf("failed to check library file existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
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
// labels and owners. Читающие пути зовут ЕГО, а не две функции по отдельности —
// иначе новый путь чтения однажды приедет с темами и без владельцев, и на одном
// экране у файла будет ответственный, а на соседнем нет.
func (s *Store) attachRelated(ctx context.Context, files []*entity.LibraryFile) error {
	if err := s.attachTopics(ctx, files); err != nil {
		return err
	}
	return s.attachOwners(ctx, files)
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
