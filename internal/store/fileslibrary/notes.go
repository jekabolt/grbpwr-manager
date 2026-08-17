package fileslibrary

import (
	"context"
	"errors"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// КАРКАС Ф8 — MARKDOWN-ЗАМЕТКИ (0318). ЗДЕСЬ ТОЛЬКО ПОДПИСИ.
//
// Тела пишет T-8.4. Два инварианта, которые нельзя потерять при заполнении:
//
//  1. ПАКЕТ НЕ ХОДИТ В БАКЕТ (шапка fileslibrary.go). Новый объект заливает вызывающий ДО вызова и
//     приносит сюда ключ/отпечаток/размер; старый ключ уносит наружу результат и удаляется ПОСЛЕ
//     коммита. Порядок «строка раньше байтов» — тот же, что в DeleteFile.
//  2. CAS ЧИТАЕТ СТРОКУ ВНУТРИ ТРАНЗАКЦИИ. Сравнение base_sha256 со значением, прочитанным до
//     транзакции, не закрывает гонку вовсе — перечитывать обязательно в той же SERIALIZABLE.
//
// Заглушка отказывает намеренно — см. довод в comments.go.
var errNotesNotImplemented = errors.New("files library: notes store is not implemented yet (T-8.4)")

func (s *Store) CreateNote(ctx context.Context, n *entity.LibraryNoteInsert, topicIDs []int, newTopics []string) (int, error) {
	return 0, errNotesNotImplemented
}

func (s *Store) GetNote(ctx context.Context, fileID int) (*entity.LibraryNote, error) {
	return nil, errNotesNotImplemented
}

func (s *Store) SaveNoteContent(ctx context.Context, in entity.LibraryNoteSave) (*entity.LibraryNoteSaveResult, error) {
	return nil, errNotesNotImplemented
}
