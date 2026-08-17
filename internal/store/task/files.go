package task

import (
	"context"
	"errors"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// КАРКАС Ф4 — СВЯЗЬ ФАЙЛ ↔ ЗАДАЧА СО СТОРОНЫ ФАЙЛА (task_file, 0312). ЗДЕСЬ ТОЛЬКО ПОДПИСИ.
//
// Тела пишет T-4.2: join task_file → task под строку карточки, attach через INSERT IGNORE с
// display_order в хвост, detach как no-op на отсутствующей связи. UpdateTask с полным набором
// file_ids НЕ ТРОГАТЬ — форма задачи живёт на нём, а гонка «замещающий набор сносит привязку с
// карточки файла» принята планом и названа в комментарии к интерфейсу.
//
// Заглушка ОТКАЗЫВАЕТ, а не возвращает пустоту: «успешно ничего не сделал» выглядит на бете как
// потерянная привязка, а не как незаконченная работа.
var errFileLinksNotImplemented = errors.New("task: file links store is not implemented yet (T-4.2)")

func (s *Store) ListTasksByFileId(ctx context.Context, fileID int) ([]entity.LibraryFileTask, error) {
	return nil, errFileLinksNotImplemented
}

func (s *Store) AttachFileToTask(ctx context.Context, fileID, taskID int) error {
	return errFileLinksNotImplemented
}

func (s *Store) DetachFileFromTask(ctx context.Context, fileID, taskID int) error {
	return errFileLinksNotImplemented
}
