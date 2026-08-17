package fileslibrary

import (
	"context"
	"errors"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// КАРКАС Ф7 — ДОСТУП К ФАЙЛУ (0317). ЗДЕСЬ ТОЛЬКО ПОДПИСИ.
//
// Тела пишет T-7.4: чтение/запись уровня и списка людей одной SERIALIZABLE-транзакцией, ротация
// поколения ссылки, журнал НА КАЖДОЕ изменение, витрина с «кто открыл» одним группированным
// запросом. Предикат видимости — задача T-7.3, и он должен войти сюда ТЕМ ЖЕ билдером, что в
// остальные выдачи: второй способ написать предикат и есть та дыра, ради которой фаза затевалась.
//
// Заглушка отказывает намеренно — см. довод в comments.go.
var errAccessNotImplemented = errors.New("files library: access store is not implemented yet (T-7.4)")

func (s *Store) GetFileAccess(ctx context.Context, fileID int) (*entity.LibraryFileAccess, error) {
	return nil, errAccessNotImplemented
}

func (s *Store) SetFileAccess(ctx context.Context, fileID int, u entity.LibraryFileAccessUpdate) (*entity.LibraryFileAccess, error) {
	return nil, errAccessNotImplemented
}

func (s *Store) RotateFileLink(ctx context.Context, fileID int, actor string) (*entity.LibraryFilePublicAccess, error) {
	return nil, errAccessNotImplemented
}

func (s *Store) ListFileAccessEvents(ctx context.Context, fileID, limit int) ([]entity.LibraryFileAccessEvent, error) {
	return nil, errAccessNotImplemented
}

func (s *Store) ListSharedFiles(ctx context.Context, f entity.SharedLibraryFileFilter) ([]entity.SharedLibraryFile, int, error) {
	return nil, 0, errAccessNotImplemented
}

func (s *Store) GetFileByPublicLink(ctx context.Context, fileID int) (*entity.LibraryFileLinkTarget, error) {
	return nil, errAccessNotImplemented
}

func (s *Store) RecordPublicAccess(ctx context.Context, counts map[int]int64, last map[int]time.Time) error {
	return errAccessNotImplemented
}
