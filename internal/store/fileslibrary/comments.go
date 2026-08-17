package fileslibrary

import (
	"context"
	"errors"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// КАРКАС Ф5 — ОБСУЖДЕНИЕ ФАЙЛА (0316). ЗДЕСЬ ТОЛЬКО ПОДПИСИ.
//
// Файл заведён каркасной задачей: интерфейс dependency.Files объявлен разом на четыре фазы, и без
// тел этих методов пакет перестал бы удовлетворять интерфейсу, то есть `go build ./...` упал бы у
// ВСЕХ. Тела пишет T-5.3 — прямо здесь, вместо заглушек, вместе с группированным счётчиком реплик
// на страницу списка (по образцу attachTopics) и контейнерным тестом каскада.
//
// Заглушка ОТКАЗЫВАЕТ, а не возвращает пустоту: метод, который «успешно» ничего не сделал, доезжает
// до беты и там выглядит как потерянные данные, а не как незаконченная работа.
var errCommentsNotImplemented = errors.New("files library: comments store is not implemented yet (T-5.3)")

func (s *Store) ListComments(ctx context.Context, fileID int) ([]entity.LibraryFileComment, error) {
	return nil, errCommentsNotImplemented
}

func (s *Store) GetCommentById(ctx context.Context, id int) (*entity.LibraryFileComment, error) {
	return nil, errCommentsNotImplemented
}

func (s *Store) AddComment(ctx context.Context, fileID int, author, body string) (*entity.LibraryFileComment, error) {
	return nil, errCommentsNotImplemented
}

func (s *Store) UpdateComment(ctx context.Context, id int, body string) (*entity.LibraryFileComment, error) {
	return nil, errCommentsNotImplemented
}

func (s *Store) DeleteComment(ctx context.Context, id int) error {
	return errCommentsNotImplemented
}
