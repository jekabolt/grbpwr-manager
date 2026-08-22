package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// КАТАЛОГ РАБОТ И ЖЕСТ «ЗАПОМНИТЬ КАК ДЕФОЛТ» (R3).
//
// ОДИН ЗАПРОС НА ВЕСЬ ПИКЕР, И ЭТО НЕ ЭКОНОМИЯ КРУГЛЫХ ПОЕЗДОК. Пункты, синонимы, машинки,
// дефолты и подсказки нормы времени описывают ОДНО состояние: набор работ, которые сегодня можно
// выбрать, и то, чем заполнится форма при выборе. Приезжай они порознь — пикер существовал бы в
// промежуточных состояниях («пункты уже есть, дефолтов ещё нет»), в которых человек успевает
// нажать, и подстановка не случилась бы без единого сообщения об этом.
//
// ⚠️ ЧТЕНИЕ КАТАЛОГА ИДЁТ В БАЗУ, А НЕ В СНИМОК ПРОЦЕССА (entity.OperationWorkCatalogSnapshot).
// Снимок существует для ПРАВИЛ ЗАПИСИ — разбор payload'а обязан отвечать «такой работы нет» без
// похода в базу, — и он хранит только то, что правилам нужно. Ярлык, синонимы и стадия ему не
// нужны, и класть их туда значило бы держать в памяти процесса второй, полный, каталог ради
// одного экрана. Отдаём тем же чтением, которым снимок и наполняется на старте.

// GetOperationWorkCatalog returns the whole work catalog in one call: works with their machines and
// RU/EN synonyms, the stored global defaults, the «last time it was N» time-norm ghosts, and the
// closed registry of fields a default may be remembered on.
func (s *Server) GetOperationWorkCatalog(ctx context.Context, _ *pb_admin.GetOperationWorkCatalogRequest) (*pb_admin.GetOperationWorkCatalogResponse, error) {
	cards := s.repo.TechCards()

	works, err := cards.GetOperationWorkCatalog(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't read operation work catalog", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read the work catalog")
	}
	defaults, err := cards.GetOperationWorkDefaults(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't read operation work defaults", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read the work defaults")
	}
	samples, err := cards.ListOperationWorkSmvSamples(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't read operation work smv samples", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read the time-norm hints")
	}

	return &pb_admin.GetOperationWorkCatalogResponse{
		Works:    dto.ConvertOperationWorkCatalogToPb(works),
		Defaults: dto.ConvertOperationWorkDefaultsToPb(defaults),
		SmvHints: dto.ConvertOperationWorkSmvHintsToPb(entity.LatestOperationWorkSmv(samples)),
		// Реестр отдаётся СЕРВЕРОМ, чтобы жест рисовался по тому же списку, который его принимает.
		// Второй, клиентский, разошёлся бы молча, и кнопка «запомнить» стояла бы на контроле, где
		// она всегда отвечает отказом.
		DefaultFields: append([]string(nil), entity.OperationWorkDefaultFields...),
	}, nil
}

// RememberOperationWorkDefault writes (or clears) ONE global default of a work property.
//
// ПОРЯДОК ПРОВЕРОК — ЭТО И ЕСТЬ ПРАВИЛО. Сначала работа (она обязана существовать в каталоге),
// затем ИМЯ ПОЛЯ через реестр entity — там машинная настройка отвергается ПО ИМЕНИ, потому что у
// неё уже есть лестница наследования 0306, — и только потом значение. Иначе человек, набравший
// температуру пресса, услышал бы сначала «значение вне диапазона», а «этому полю дефолт не
// положен» — вторым заходом.
//
// ⚠️ СНЯТИЕ ТОЖЕ ПРОХОДИТ РЕЕСТР. `clear` не пропускает проверку имени: иначе «забудь дефолт
// stitches_per_cm» отвечало бы успехом и рождало бы у человека уверенность, что такой дефолт где-то
// был. Значение при clear не проверяется вовсе — забывают строку, а не число.
func (s *Server) RememberOperationWorkDefault(ctx context.Context, req *pb_admin.RememberOperationWorkDefaultRequest) (*pb_admin.RememberOperationWorkDefaultResponse, error) {
	token := strings.TrimSpace(req.GetWorkToken())
	field := strings.TrimSpace(req.GetField())

	if token == "" {
		return nil, apierr.Invalid(entity.NewFieldViolation("work_token", "required", "",
			"name the work the default belongs to"))
	}
	// Каталог спрашивается у СНИМКА, а не у базы: правило «такой работы нет» уже живёт там, и
	// второй его источник разошёлся бы с первым. Незагруженный снимок — не «работы нет», а «этот
	// сервер каталога не знает», и это отдельный диагноз (тот же, что у разбора шага).
	catalog := entity.OperationWorkCatalogSnapshot()
	if catalog == nil {
		return nil, apierr.FailedPrecondition(entity.NewFieldViolation("work_token", "catalog_unavailable", token,
			"this server has not loaded the work catalog (migration 0329), so it cannot tell one work from another — the catalog is read once at startup"))
	}
	// СНЯТАЯ (retired) РАБОТА ПРИНИМАЕТСЯ НАМЕРЕННО: её дефолт ещё читают шаги, уже несущие этот
	// токен, и запретить правку значило бы заморозить подстановку на строках, которые человек
	// продолжает открывать. В пикер такая работа всё равно не предлагается — это решает клиент по
	// флагу retired.
	if _, ok := catalog.Lookup(token); !ok {
		return nil, apierr.Invalid(entity.NewFieldViolation("work_token", "unknown_work", token,
			"no such work in the work catalog (migration 0329) — pick one from the list"))
	}

	// ПРАВИЛО РЕЕСТРА. Одно на оба жеста, до значения.
	if ve := entity.ValidateOperationWorkDefaultField(field); ve != nil {
		return nil, apierr.Invalid(ve)
	}

	// Хранилище спрашивается ПОСЛЕ всех правил, а не до: отказ по имени поля не должен зависеть от
	// того, поднялся ли репозиторий, и тест «машинная настройка не доехала до записи» иначе не
	// смог бы этого доказать — он собирает мок вовсе без ожидания вызова.
	if req.GetClear() {
		if err := s.repo.TechCards().DeleteOperationWorkDefault(ctx, token, field); err != nil {
			slog.Default().ErrorContext(ctx, "can't forget operation work default",
				slog.String("err", err.Error()), slog.String("work", token), slog.String("field", field))
			return nil, status.Error(codes.Internal, fmt.Sprintf("can't forget the default for %s", field))
		}
		return &pb_admin.RememberOperationWorkDefaultResponse{}, nil
	}

	value := strings.TrimSpace(req.GetValue())
	if ve := entity.ValidateOperationWorkDefaultValue(field, value); ve != nil {
		return nil, apierr.Invalid(ve)
	}
	if err := s.repo.TechCards().SetOperationWorkDefault(ctx, token, field, value); err != nil {
		slog.Default().ErrorContext(ctx, "can't remember operation work default",
			slog.String("err", err.Error()), slog.String("work", token), slog.String("field", field))
		return nil, status.Error(codes.Internal, fmt.Sprintf("can't remember the default for %s", field))
	}
	return &pb_admin.RememberOperationWorkDefaultResponse{}, nil
}
