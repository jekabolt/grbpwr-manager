package task

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/fileslibrary"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Ф4 — СВЯЗЬ ФАЙЛ ↔ ЗАДАЧА СО СТОРОНЫ ФАЙЛА (task_file, 0312).
//
// Обе кнопки карточки файла описывают ЖЕЛАЕМОЕ состояние («пусть этот файл висит на этой задаче» /
// «пусть не висит»), поэтому повтор здесь не ошибка, а совпадение с уже достигнутым состоянием.
// Отсюда идемпотентность обеих мутаций.
//
// UpdateTask с полным набором file_ids НЕ ТРОГАЕТСЯ: форма задачи живёт на нём, а гонка
// «замещающий набор сносит привязку, сделанную с карточки файла» названа планом и продублирована в
// комментарии к dependency.Tasks.
//
// ПРЕДИКАТ ВИДИМОСТИ (Ф7, T-7.3, точка 8) ПРИХОДИТ СЮДА ИЗ ПАКЕТА БИБЛИОТЕКИ, а не пишется здесь
// заново: `fileslibrary.Viewer` — единственный билдер этого условия во всём репозитории, и импорт
// ради него дешевле, чем вторая реализация (пункт 1 «опасных мест» плана). Обратной зависимости
// нет: fileslibrary про задачи ничего не знает.
//
// DetachFileFromTask предиката НЕ ПОЛУЧАЕТ, и это решение. Отцепление ничего не разглашает (ответ
// один и тот же для прицепленного и не прицепленного), а запрет отцеплять невидимое оставил бы
// файл, который нельзя ни увидеть, ни снять с задачи, — и удаление задачи стало бы единственным
// выходом.

// ListTasksByFileId returns the task rows the file card draws.
//
// АРХИВНЫЕ ЗАДАЧИ ВХОДЯТ В ОТВЕТ, И ЭТО НЕ НЕДОСМОТР. Отказ удаления файла (DeleteFile) называет
// держателями ВСЕ строки task_file, не разбирая архив; спрячь мы архивные здесь — карточка сказала
// бы «задач нет», а удаление отказало бы с именем задачи, которой человек на экране не видит, и
// подсказка «отцепите его в разделе задачи выше» вела бы в пустой список. Порядок это признаёт:
// живые задачи идут первыми, внутри группы — свежие сверху.
//
// НЕВИДИМЫЙ ФАЙЛ ОТДАЁТ ПУСТОЙ СПИСОК, А НЕ ОШИБКУ. Пустота для несуществующего файла здесь была
// правдой с самого начала (см. хендлер ListLibraryFileTasks), и невидимый обязан быть от него
// неотличим — а хендлер переводит в Internal всё, что не пусто, поэтому ошибка здесь означала бы
// 500 вместо «ничего нет». Заголовки чужих задач — такая же утечка, как имя файла: «где этот файл
// используется» рассказывает, ЧЕМ занят закрывший его человек.
func (s *Store) ListTasksByFileId(ctx context.Context, fileID int) ([]entity.LibraryFileTask, error) {
	v, err := fileslibrary.ResolveViewer(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"fileId": fileID}
	// Колонки t.assignee здесь БОЛЬШЕ НЕТ: исполнителей у задачи много (0337), и они дозаполняются
	// тем же батч-хелпером, что и у доски, — по собранным id, а не JOIN'ом, иначе строка задачи
	// размножилась бы по числу исполнителей.
	rows, err := storeutil.QueryListNamed[entity.LibraryFileTask](ctx, s.DB, `
		SELECT t.id, t.title, t.status, t.due_date, t.board
		FROM task_file tf
		JOIN task t ON t.id = tf.task_id
		WHERE tf.file_id = :fileId AND `+v.ExistsFile("tf.file_id", params)+`
		ORDER BY (t.archived_at IS NULL) DESC, t.id DESC`, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks by file id: %w", err)
	}
	if len(rows) == 0 {
		return rows, nil
	}
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.TaskId)
	}
	assignees, err := s.assigneesByTaskIds(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Assignees = assignees[rows[i].TaskId]
	}
	return rows, nil
}

// AttachFileToTask links one file to one task idempotently, appending it to the END of that task's
// attachment order.
//
// ПОЧЕМУ НЕ INSERT IGNORE, ХОТЯ ПЛАН НАЗЫВАЕТ ИМЕННО ЕГО. IGNORE в MySQL глушит не только дубль
// ключа, но и нарушение внешнего ключа (1452): привязка к несуществующей задаче или к удалённому
// файлу прошла бы «успешно», ничего не записав, — то самое «успешно ничего не сделал», которое на
// бете читается как потерянная привязка. ON DUPLICATE KEY UPDATE даёт ту же идемпотентность по
// uniq_task_file, но оставляет FK работать: несуществующий id доезжает до хендлера ошибкой.
//
// Повтор НЕ ПЕРЕНУМЕРОВЫВАЕТ строку: порядок вложений задачи принадлежит задаче, и второе нажатие
// на карточке файла не имеет права переставлять её содержимое. Явная проверка существования связи
// стоит до вставки ровно за этим, а ON DUPLICATE остаётся страховкой на гонку.
//
// ПРЕДИКАТ ВИДИМОСТИ (точка 8): невидимый файл НЕ ПРИКРЕПИТЬ — иначе его имя вылезло бы на чужой
// карточке задачи в обход всех выдач. Отказ — sql.ErrNoRows, и хендлер делает из него тот же
// NotFound, что из исчезнувшей задачи: «того, к чему цепляем, больше нет» — единственный ответ,
// не различающий несуществующий файл и закрытый.
func (s *Store) AttachFileToTask(ctx context.Context, fileID, taskID int) error {
	v, err := fileslibrary.ResolveViewer(ctx, s.DB)
	if err != nil {
		return err
	}
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Внутри SERIALIZABLE-транзакции, поэтому проверка реально закрывает гонку с удалением
		// файла и со сменой его уровня, а не просто сужает окно.
		if err := fileslibrary.EnsureVisible(ctx, rep.DB(), v, fileID); err != nil {
			return err // sql.ErrNoRows нетронутым
		}
		params := map[string]any{"taskId": taskID, "fileId": fileID}
		linked, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COUNT(*) FROM task_file WHERE task_id = :taskId AND file_id = :fileId`, params)
		if err != nil {
			return fmt.Errorf("failed to check task file link: %w", err)
		}
		if linked > 0 {
			return nil
		}
		// Хвост считается внутри той же SERIALIZABLE-транзакции, поэтому параллельная привязка к той
		// же задаче не получит тот же display_order.
		pos, err := storeutil.QueryCountNamed(ctx, rep.DB(),
			`SELECT COALESCE(MAX(display_order)+1, 0) FROM task_file WHERE task_id = :taskId`,
			map[string]any{"taskId": taskID})
		if err != nil {
			return fmt.Errorf("failed to compute task file display order: %w", err)
		}
		params["displayOrder"] = pos
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			INSERT INTO task_file (task_id, file_id, display_order)
			VALUES (:taskId, :fileId, :displayOrder)
			ON DUPLICATE KEY UPDATE display_order = display_order`, params); err != nil {
			return fmt.Errorf("failed to attach file to task: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("can't attach file to task: %w", err)
	}
	return nil
}

// DetachFileFromTask removes the link. Отцепить не прицепленное — no-op, а не ошибка: двое,
// убирающие одно и то же вложение, оба обязаны услышать «его больше нет», а не один из них — «его
// никогда и не было».
//
// Дыра в display_order после удаления НЕ ЗАКРЫВАЕТСЯ намеренно: колонка задаёт порядок, а не
// нумерацию, сортировка по ней от дыры не меняется, а перенумерация переписала бы строки, которых
// эта кнопка не касалась (и подралась бы с сохранением формы задачи, которое нумерует набор заново).
func (s *Store) DetachFileFromTask(ctx context.Context, fileID, taskID int) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM task_file WHERE task_id = :taskId AND file_id = :fileId`,
			map[string]any{"taskId": taskID, "fileId": fileID}); err != nil {
			return fmt.Errorf("failed to detach file from task: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("can't detach file from task: %w", err)
	}
	return nil
}
