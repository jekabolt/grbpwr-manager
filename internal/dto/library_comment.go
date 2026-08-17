package dto

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxLibraryCommentRunes bounds one remark in a file's discussion.
//
// СЧИТАЕТСЯ В РУНАХ, А ХРАНИТСЯ В БАЙТАХ, И ИМЕННО ПОЭТОМУ ЧИСЛО ТАКОЕ. Колонка body — TEXT, то
// есть 65535 БАЙТ; кириллица в utf8mb4 занимает два байта на знак, эмодзи — четыре. Предел,
// заданный в байтах, разрешил бы русской реплике вдвое меньше текста, чем английской (тот же довод,
// что у maxNoteFormatRunes в files_ai.go), а предел в рунах без запаса упёрся бы в 1406 Data too
// long — отказом БД, а не понятной фразой. 10000 рун при худших четырёх байтах на руну — 40 КБ,
// то есть в TEXT влезает любая допущенная сюда реплика.
//
// Это НЕ правило о том, как людям писать: 10000 знаков — это несколько страниц, и такая реплика
// в обсуждении файла означает, что человек промахнулся полем ввода. Ограничение защищает столбец,
// а не стиль.
const maxLibraryCommentRunes = 10000

// ValidateLibraryCommentBody trims and bounds the text of one remark.
//
// Обрезка по краям — единственное, что делается с текстом. @упоминания НЕ РАЗБИРАЮТСЯ и не
// размечаются: сервер хранит то, что набрали. Подсветка и поповер живут на клиенте, где они
// экранируют текст ДО разметки; серверная разметка означала бы, что каждый читатель обязан
// экранировать её заново, и первый забывший открыл бы XSS в ленте.
func ValidateLibraryCommentBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("comment body is required")
	}
	if n := utf8.RuneCountInString(body); n > maxLibraryCommentRunes {
		return "", fmt.Errorf("comment body must be at most %d characters (got %d)", maxLibraryCommentRunes, n)
	}
	return body, nil
}

// ConvertEntityLibraryFileCommentToPb converts one stored remark.
func ConvertEntityLibraryFileCommentToPb(c *entity.LibraryFileComment) *pb_admin.LibraryFileComment {
	if c == nil {
		return nil
	}
	return &pb_admin.LibraryFileComment{
		Id:     int32(c.Id),
		FileId: int32(c.FileId),
		Author: c.Author,
		// 0 значит «аккаунта больше нет», а НЕ «неизвестно кто» — ровно как uploaded_by_id у
		// файла: имя автора едет строкой выше и переживает удаление аккаунта. Клиент печатает имя
		// всегда, а аватар и специальность резолвит только при ненулевом id.
		AuthorId:  int32(c.AuthorId.Int64),
		Body:      c.Body,
		CreatedAt: timestamppb.New(c.CreatedAt),
		// nullTimeToPb (dto/membership.go) отдаёт nil, а не нулевой Timestamp: у реплики, которую
		// никто не правил, «изменено в первом году» было бы враньём, а метка «изменено» рисуется
		// именно по непустому значению. Незаполненное время приезжает на провод явным null
		// (EmitUnpopulated), и клиент отличает отсутствие только по нему.
		EditedAt: nullTimeToPb(c.EditedAt),
	}
}

// ConvertEntityLibraryFileCommentsToPb converts a whole feed, preserving order.
// Пустая лента едет пустым срезом, а не nil: «обсуждения нет» — это состояние экрана, а не
// отсутствие ответа.
func ConvertEntityLibraryFileCommentsToPb(comments []entity.LibraryFileComment) []*pb_admin.LibraryFileComment {
	out := make([]*pb_admin.LibraryFileComment, 0, len(comments))
	for i := range comments {
		out = append(out, ConvertEntityLibraryFileCommentToPb(&comments[i]))
	}
	return out
}
