package dto

import (
	"fmt"
	"math"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ДОСТУП К ФАЙЛУ НА ПРОВОДЕ (Ф7): уровень, люди, публичная ссылка, журнал, витрина.
//
// Один файл конвертеров на всю фазу, а не поле-в-поле по хендлерам: «истёк» и «отозвана»
// ВЫЧИСЛЯЮТСЯ, и вычислять их обязано одно место. Два экрана, каждый со своим сравнением с
// часами сервера, разошлись бы на первой же ссылке, у которой срок прошёл минуту назад.

// maxLibraryFileLinkTTLHours bounds the link's life at five years. Не политика, а защита от
// цикла: чипы макета — 24ч/7д/30д/бессрочно, и «бессрочно» выражается нулём, а не числом,
// поэтому огромное положительное значение может приехать только из ошибки клиента.
const maxLibraryFileLinkTTLHours = 24 * 365 * 5

// ParseLibraryFileAccessLevel turns the wire string into the domain level, REFUSING anything
// else. Неизвестный уровень не толкуется: «непонятный = team» тихо расширил бы доступ,
// «= people» — тихо потерял бы файл. Ровно тот же довод, по которому колонка объявлена ENUM.
func ParseLibraryFileAccessLevel(s string) (entity.LibraryFileAccessLevel, error) {
	lvl := entity.LibraryFileAccessLevel(s)
	if !entity.ValidLibraryFileAccessLevels[lvl] {
		return "", fmt.Errorf("level must be one of team, people, link (got %q)", s)
	}
	return lvl, nil
}

// ValidateLibraryFileLinkTTL bounds the ttl in HOURS (0 = бессрочно).
func ValidateLibraryFileLinkTTL(hours int32) (int, error) {
	if hours < 0 {
		return 0, fmt.Errorf("link_ttl must not be negative (0 means no expiry)")
	}
	if hours > maxLibraryFileLinkTTLHours {
		return 0, fmt.Errorf("link_ttl must be at most %d hours", maxLibraryFileLinkTTLHours)
	}
	return int(hours), nil
}

// ConvertEntityLibraryFilePublicLinkToPb renders the link row for the wire.
//
// url ПРИХОДИТ СНАРУЖИ и приходит пустым, когда файл сейчас не на уровне `link`: строка
// доступа переживает уровень (возврат в `team` её не удаляет), и отдать по такой строке
// копируемый url значило бы показать человеку ссылку, которая гарантированно отвечает 404.
// Минтить его здесь тоже нельзя — pepper живёт в сервисе, а dto не место для секрета.
func ConvertEntityLibraryFilePublicLinkToPb(row *entity.LibraryFilePublicAccess, url string) *pb_admin.LibraryFilePublicLink {
	if row == nil {
		return nil
	}
	link := &pb_admin.LibraryFilePublicLink{
		Url:         url,
		Revoked:     row.RevokedAt.Valid,
		AccessCount: clampInt32(row.AccessCount),
	}
	if row.ExpiresAt.Valid {
		link.ExpiresAt = timestamppb.New(row.ExpiresAt.Time)
		// `expired` ВЫЧИСЛЯЕТСЯ и не хранится: прошедший срок НЕ меняет уровень (ничто не
		// расшаривает и не расшаривает обратно файл за спиной владельца) — маршрут отвечает
		// 404, а панель рисует бейдж «истёк».
		link.Expired = time.Now().UTC().After(row.ExpiresAt.Time)
	}
	if row.LastAccessAt.Valid {
		link.LastAccessAt = timestamppb.New(row.LastAccessAt.Time)
	}
	return link
}

// ConvertEntityLibraryFileAccessToPb renders the whole access block.
func ConvertEntityLibraryFileAccessToPb(a *entity.LibraryFileAccess, url string) *pb_admin.LibraryFileAccess {
	if a == nil {
		return nil
	}
	return &pb_admin.LibraryFileAccess{
		Level:  string(a.Level),
		People: ConvertEntityAdminRefsToPb(a.People),
		Link:   ConvertEntityLibraryFilePublicLinkToPb(a.Link, url),
	}
}

// ConvertEntityLibraryFileAccessEventsToPb renders the journal. actor_id наружу не едет: в
// журнале его читает человек, а лишний id в публичном ответе — это лишний способ перечислить
// аккаунты.
func ConvertEntityLibraryFileAccessEventsToPb(events []entity.LibraryFileAccessEvent) []*pb_admin.LibraryFileAccessEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]*pb_admin.LibraryFileAccessEvent, 0, len(events))
	for _, e := range events {
		out = append(out, &pb_admin.LibraryFileAccessEvent{
			Id:        int32(e.Id),
			Actor:     e.Actor,
			What:      e.What,
			CreatedAt: timestamppb.New(e.CreatedAt),
		})
	}
	return out
}

// ConvertEntitySharedLibraryFileToPb assembles one витрина row. The FILE message arrives ready
// (the handler mints its presigned urls) — витрина рисует ту же плитку, что и сетка, и собирать
// её вторым способом здесь значило бы завести второй набор правил про inline-безопасность.
func ConvertEntitySharedLibraryFileToPb(row entity.SharedLibraryFile, file *pb_admin.LibraryFile, url string) *pb_admin.SharedLibraryFile {
	out := &pb_admin.SharedLibraryFile{
		File:     file,
		People:   ConvertEntityAdminRefsToPb(row.People),
		Link:     ConvertEntityLibraryFilePublicLinkToPb(row.Link, url),
		SharedBy: row.SharedBy,
	}
	if row.SharedAt.Valid {
		out.SharedAt = timestamppb.New(row.SharedAt.Time)
	}
	return out
}

// clampInt32 сжимает BIGINT-счётчик в int32 контракта. Счётчик крутит кто угодно со ссылкой,
// поэтому переполнение — вопрос времени, а не гипотеза; насыщение честнее отрицательного числа
// на экране.
func clampInt32(v int64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < 0 {
		return 0
	}
	return int32(v)
}
