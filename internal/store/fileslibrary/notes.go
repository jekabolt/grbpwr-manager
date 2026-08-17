package fileslibrary

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Ф8 — MARKDOWN-ЗАМЕТКИ (0318).
//
// Два инварианта, на которых стоит весь файл:
//
//  1. ПАКЕТ НЕ ХОДИТ В БАКЕТ (шапка fileslibrary.go). Новый объект заливает вызывающий ДО вызова и
//     приносит сюда ключ/отпечаток/размер; старый ключ уносит наружу результат и удаляется ПОСЛЕ
//     коммита. Порядок «строка раньше байтов» — тот же, что в DeleteFile.
//  2. CAS ЧИТАЕТ СТРОКУ ВНУТРИ ТРАНЗАКЦИИ. Сравнение base_sha256 со значением, прочитанным до
//     транзакции, не закрывает гонку вовсе — перечитывать обязательно в той же SERIALIZABLE.

// noteRowColumns is the note's row as LibraryNote scans it — БЕЗ ТЕКСТА (текст в бакете) и без
// звёздочки: перечисление здесь ровно то, что описывает тип, поэтому новая колонка на library_file
// не начинает молча приезжать в заметку.
const noteRowColumns = `lf.id, lf.file_name, lf.content_type, lf.object_key, lf.sha256, lf.size_bytes,
	lf.content_updated_by, lf.content_updated_at, lf.uploaded_by`

// ТОЧКА 9 ПЕРЕЧНЯ ВИДИМОСТИ (Ф7, план §Ф7 «точки вырезания») ЗАКРЫТА.
//
// GetNote и SaveNoteContent выбирают строку ПОД ТЕМ ЖЕ билдером (Viewer.Where, visibility.go),
// что и остальные выдачи, — ровно в этих двух точках и нигде больше. Помеченная константа
// noteVisibilityWhere, которую Ф8 оставила вместо предиката, снята: держать рядом с билдером
// вторую строчку «где искать заметку» значило бы завести тот самый второй способ написать
// предикат, ради отсутствия которого фаза и делалась.

// CreateNote inserts the note's row with its topics — та же транзакция и та же грамматика тем, что
// у AddFile, плюс три колонки заметки (0318).
//
// Имя с `.md`, потолок текста и сама выдержка — забота вызывающего: это правила ввода и производные
// от текста, которого стор не видит.
func (s *Store) CreateNote(ctx context.Context, n *entity.LibraryNoteInsert, topicIDs []int, newTopics []string) (int, error) {
	if n == nil {
		return 0, fmt.Errorf("note insert is nil")
	}
	if strings.TrimSpace(n.ObjectKey) == "" {
		// Стор в бакет не ходит: строка без ключа — это заметка, текст которой негде взять.
		return 0, fmt.Errorf("note object key is required (upload the object before inserting the row)")
	}
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		// uploaded_by_id ВЫВОДИТСЯ ИЗ ТОГО ЖЕ ИМЕНИ ОДНИМ ОПЕРАТОРОМ — дословно как в AddFile:
		// заметка это обычный файл, и две половины авторства у неё не должны уметь разойтись.
		//
		// content_updated_at стамповано ЧАСАМИ БАЗЫ (NOW()), а не временем вызывающего, и берёт из
		// него только флаг Valid («текст при создании был»). Иначе «правил {когда}» у создания и у
		// сохранения приезжали бы с разных часов, и сортировка по свежести врала бы на секунды.
		stamp := "NULL"
		if n.ContentUpdatedAt.Valid {
			stamp = "NOW()"
		}
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO library_file
				(object_key, preview_object_key, file_name, content_type, size_bytes, sha256, uploaded_by, uploaded_by_id,
				 content_excerpt, content_updated_by, content_updated_at)
			VALUES (:objectKey, :previewObjectKey, :fileName, :contentType, :sizeBytes, :sha256, :uploadedBy,
				(SELECT a.id FROM admins a WHERE a.username = :uploadedBy),
				:contentExcerpt, :contentUpdatedBy, `+stamp+`)`,
			map[string]any{
				"objectKey":        n.ObjectKey,
				"previewObjectKey": n.PreviewObjectKey,
				"fileName":         n.FileName,
				"contentType":      n.ContentType,
				"sizeBytes":        n.SizeBytes,
				"sha256":           n.Sha256,
				"uploadedBy":       n.UploadedBy,
				"contentExcerpt":   n.ContentExcerpt,
				"contentUpdatedBy": n.ContentUpdatedBy,
			})
		if err != nil {
			return fmt.Errorf("failed to insert library note: %w", err)
		}
		return linkTopics(ctx, rep.DB(), id, topicIDs, newTopics)
	})
	if err != nil {
		return 0, fmt.Errorf("can't create library note: %w", err)
	}
	return id, nil
}

// GetNote returns the note's ROW — без текста. Содержимое вызывающий читает по ObjectKey через
// FileStore.GetLibraryObject; разрез идёт по слою, а не по удобству.
//
// sql.ErrNoRows, когда файла нет ИЛИ он невидим — эти два случая снаружи неразличимы намеренно.
func (s *Store) GetNote(ctx context.Context, fileID int) (*entity.LibraryNote, error) {
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"id": fileID}
	n, err := storeutil.QueryNamedOne[entity.LibraryNote](ctx, s.DB,
		`SELECT `+noteRowColumns+` FROM library_file lf WHERE lf.id = :id AND `+v.Where("lf", params),
		params)
	if err != nil {
		return nil, err // sql.ErrNoRows passes through untouched
	}
	return &n, nil
}

// SaveNoteContent is the compare-and-set write.
//
// ПОЧЕМУ СТОЛКНОВЕНИЕ ВОЗВРАЩАЕТСЯ ДАННЫМИ, А НЕ ОШИБКОЙ: клиент обязан построить по нему баннер и
// три исхода, и статус ошибки стоил бы второго запроса на каждый. Молчаливая перезапись при этом
// невозможна ПО ПОСТРОЕНИЮ: без совпавшего отпечатка и без явного Force ни один UPDATE отсюда не
// выходит.
//
// ПОЧЕМУ FOR UPDATE, А НЕ ПРОСТО SERIALIZABLE. Уровень изоляции сам по себе гонку тоже закрывает —
// но парой разделяемых блокировок, то есть взаимоблокировкой обоих сохранений и повтором транзакции
// вторым. Явная монопольная блокировка строки превращает это в ожидание: второй читает строку уже
// ПОСЛЕ коммита первого, видит чужой отпечаток и получает конфликт — детерминированно, а не через
// откат по дедлоку.
func (s *Store) SaveNoteContent(ctx context.Context, in entity.LibraryNoteSave) (*entity.LibraryNoteSaveResult, error) {
	if in.FileId <= 0 {
		return nil, fmt.Errorf("note file id is required")
	}
	if strings.TrimSpace(in.ObjectKey) == "" || strings.TrimSpace(in.Sha256) == "" {
		// Пустой ключ обнулил бы указатель на текст, оставив строку живой: заметка, которая
		// открывается и ничего не показывает. Это хуже любого отказа.
		return nil, fmt.Errorf("note object key and sha256 are required (upload the object before saving the row)")
	}
	v, err := s.viewer(ctx)
	if err != nil {
		return nil, err
	}
	var res entity.LibraryNoteSaveResult
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Tx повторяет колбэк на транзиентных отказах — результат обязан собираться заново, иначе
		// повтор дописал бы к нему поля прошлой попытки.
		res = entity.LibraryNoteSaveResult{}

		params := map[string]any{"id": in.FileId}
		cur, err := storeutil.QueryNamedOne[entity.LibraryNote](ctx, rep.DB(),
			`SELECT `+noteRowColumns+` FROM library_file lf
			WHERE lf.id = :id AND `+v.Where("lf", params)+` FOR UPDATE`, params)
		if err != nil {
			return err // sql.ErrNoRows passes through untouched
		}

		if !in.Force && cur.Sha256 != in.BaseSha256 {
			// НИЧЕГО НЕ ЗАПИСАНО. Наружу уезжает то, что лежит, включая ключ чужого объекта —
			// текст по нему читает вызывающий (стор в бакет не ходит).
			res.Conflict = true
			res.CurrentSha256 = cur.Sha256
			res.CurrentObjectKey = cur.ObjectKey
			res.LastEditedBy = cur.ContentUpdatedBy
			res.LastEditedAt = cur.ContentUpdatedAt
			return nil
		}

		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE library_file SET
				object_key = :objectKey,
				sha256 = :sha256,
				size_bytes = :sizeBytes,
				content_excerpt = :contentExcerpt,
				content_updated_by = :editedBy,
				content_updated_at = NOW()
			WHERE id = :id`,
			map[string]any{
				"id":             in.FileId,
				"objectKey":      in.ObjectKey,
				"sha256":         in.Sha256,
				"sizeBytes":      in.SizeBytes,
				"contentExcerpt": in.ContentExcerpt,
				"editedBy":       in.EditedBy,
			}); err != nil {
			return fmt.Errorf("failed to update library note content: %w", err)
		}
		// RowsAffected не проверяется, и причина НЕ в том, что повторное сохранение ничего не
		// меняет: object_key у каждой заливки новый, а content_updated_at = NOW(), так что нулю
		// здесь взяться неоткуда. Причина в другом — проверять нечего: строка прочитана этой же
		// транзакцией под FOR UPDATE и держится монопольно до коммита, поэтому «строки нет»
		// невозможно по построению, а не маловероятно. Ошибку записи вернул бы ExecNamed.

		// Штампы перечитываются, а не вычисляются в Go: наружу обязано уехать ровно то, что легло в
		// строку, иначе клиент нарисует в шапке время, которого в базе нет.
		type noteStamps struct {
			ContentUpdatedAt sql.NullTime `db:"content_updated_at"`
			UpdatedAt        time.Time    `db:"updated_at"`
		}
		st, err := storeutil.QueryNamedOne[noteStamps](ctx, rep.DB(),
			`SELECT lf.content_updated_at, lf.updated_at FROM library_file lf WHERE lf.id = :id`,
			map[string]any{"id": in.FileId})
		if err != nil {
			return fmt.Errorf("failed to read back library note stamps: %w", err)
		}

		res.CurrentSha256 = in.Sha256
		res.LastEditedBy = in.EditedBy
		res.LastEditedAt = st.ContentUpdatedAt
		res.UpdatedAt = st.UpdatedAt
		// Старый ключ уносится наружу для best-effort уборки ПОСЛЕ коммита. Пустым он остаётся, если
		// вызывающий почему-то принёс тот же ключ: удалить его значило бы снести байты, на которые
		// строка уже указывает.
		if cur.ObjectKey != "" && cur.ObjectKey != in.ObjectKey {
			res.PreviousObjectKey = cur.ObjectKey
		}
		return nil
	})
	if err != nil {
		return nil, err // sql.ErrNoRows passes through untouched
	}
	return &res, nil
}
