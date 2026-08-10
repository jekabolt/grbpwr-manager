package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetMaterialCuttingCoefficientSuggestion предлагает коэффициент раскроя артикула по ФАКТУ его
// настилов (Ф5б.3, §4.2): медиана дрейфов план/факт по настилам, где есть и план, и факт.
//
// ПРЕДЛОЖЕНИЕ, А НЕ ПРИМЕНЕНИЕ. Обработчик НИЧЕГО НЕ ПИШЕТ и писать не должен: автоприменение
// подменило бы решение владельца артикула числом, которого он не выбирал, — та же дисциплина, что у
// нормы в Ф3 и у пер-размерного расхода в Ф2.4. Здесь только чтения.
//
// ОТДЕЛЬНЫЙ RPC, а не поле на GetMaterial: этот ответ сканирует настилы по прогонам и поднимает их
// карточки, а каталог материалов читается всюду. Платить за скан на каждом чтении каталога незачем.
func (s *Server) GetMaterialCuttingCoefficientSuggestion(ctx context.Context,
	req *pb_admin.GetMaterialCuttingCoefficientSuggestionRequest,
) (*pb_admin.GetMaterialCuttingCoefficientSuggestionResponse, error) {

	materialID := int(req.GetMaterialId())
	if materialID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "material_id is required")
	}

	// Артикул читается ради ЕДИНИЦЫ, в которой он считается: замер не в этой единице делить на план
	// нельзя, и настил, нарушающий это, обязан быть назван, а не посчитан. Отсутствие артикула — это
	// NotFound, а не пустое предложение: «у такого артикула фактов нет» и «такого артикула нет» —
	// разные ответы, и второй нельзя выдавать под видом первого.
	m, err := s.repo.TechCards().GetMaterial(ctx, materialID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "material not found")
		}
		slog.Default().ErrorContext(ctx, "can't get material for coefficient suggestion",
			slog.Int("material_id", materialID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get material")
	}

	// Окно то же, что у соседа (measuredLayScanLimit, MAJOR 5): безлимитный скан истории артикула
	// делал по последовательной загрузке карточки на настил и выходил по таймауту на популярном
	// артикуле. Полный счётчик здесь не отдаётся — контракт этого ответа не менялся.
	lays, _, err := s.repo.ProductionRuns().ListMeasuredLayCandidates(ctx, materialID, measuredLayScanLimit)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list measured lays for coefficient suggestion",
			slog.Int("material_id", materialID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list measured lays")
	}

	// КАРТОЧКИ ГРУЗЯТСЯ НАСТОЯЩИМ ЗАГРУЗЧИКОМ И ПО ОДНОЙ НА КАРТОЧКУ, а не на настил: у одного
	// артикула настилы кучкуются по немногим карточкам, и повторное чтение той же карточки было бы
	// N+1 по числу, которым распоряжается цех.
	//
	// ПОЧЕМУ ИМЕННО GetTechCardById, А НЕ ЛЁГКАЯ ВЫБОРКА СЛОТОВ. Резолвер артикула умеет откатываться
	// на ПОЗИЦИОННЫЙ индекс слота (planBomLine: legacy-юзеджи адресуют слот номером, а не FK).
	// Позиция осмысленна только в том порядке, в котором слоты грузит настоящий загрузчик; своя
	// «лёгкая» выборка с другим ORDER BY разрешила бы такой юзедж В ЧУЖОЙ СЛОТ и выдала бы чужой
	// артикул. Это ровно та ошибка, что уже давала пустой материальный план на бете.
	cards := make(map[int]*entity.TechCard)
	for i := range lays {
		id := lays[i].TechCardId
		if _, ok := cards[id]; ok || id <= 0 {
			continue
		}
		card, err := s.repo.TechCards().GetTechCardById(ctx, id)
		if err != nil {
			// НЕ best-effort. Ненагруженная карточка означает, что её настилы не разрешатся ни в какой
			// артикул и молча выпадут из медианы — предложение стало бы меньше по причине, которой нет
			// в данных, и выглядело бы это как «фактов пока мало» при полном журнале замеров.
			slog.Default().ErrorContext(ctx, "can't load tech card for coefficient suggestion",
				slog.Int("material_id", materialID), slog.Int("tech_card_id", id), slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "can't load tech card of a measured lay")
		}
		cards[id] = card
	}

	return dto.BuildMaterialCoefficientSuggestion(dto.MaterialCoefficientCalibrationInput{
		MaterialId: materialID,
		ArticleUom: m.Unit.String,
		Lays:       lays,
		Cards:      cards,
	}), nil
}
