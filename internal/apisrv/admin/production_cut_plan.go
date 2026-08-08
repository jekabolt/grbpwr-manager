package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cutspec"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetProductionRunCutPlan projects the run's lines onto the style's cut pieces: деталь × колорвей ×
// размер → сколько панелей выкроить и из какого артикула. Read-only; writes nothing.
//
// Сестра GetProductionRunMaterialPlan, и загрузка списана с неё намеренно — тот же прогон, тот же
// способ отвечать NotFound/Internal, тот же гейт (rd(SectionProduction)). Денег в ответе нет ни в
// каком виде, поэтому RBAC-стрипа костинга здесь тоже нет, и его отсутствие — решение, а не
// упущение: этот же ответ уезжает в публичный манифест наряда, где стрипать некому.
//
// ВЫБОР СПЕЦИФИКАЦИИ ЖИВЁТ В internal/cutspec, А НЕ ЗДЕСЬ, и переехал он туда не ради красоты:
// публичный наряд (internal/runpackaccess) — второй читатель того же правила, и пока правило лежало
// внутри этого хендлера, он считал по живой карточке, ставя в шапку номер релиза. Одно правило —
// одно определение.
func (s *Server) GetProductionRunCutPlan(ctx context.Context, req *pb_admin.GetProductionRunCutPlanRequest) (*pb_admin.GetProductionRunCutPlanResponse, error) {
	if req.GetRunId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	run, err := s.repo.ProductionRuns().GetProductionRun(ctx, int(req.GetRunId()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		slog.Default().ErrorContext(ctx, "can't load production run for cut plan", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load production run")
	}

	spec, err := cutspec.Resolve(ctx, s.repo.TechCards(), run)
	if err != nil {
		// ErrNoRows здесь означает ровно одно — карточки прогона нет; сбой чтения релиза приезжает
		// отдельной ошибкой и НЕ деградирует на живую карточку (см. cutspec.Resolve).
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't resolve cut plan spec", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't resolve cut plan spec")
	}
	resp := dto.ComputeProductionRunCutPlan(run, spec.Card, spec.Release)
	// Оговорки о ВЫБОРЕ спецификации идут первыми: они объясняют, по какой карточке посчитано всё
	// остальное, и читать их после списка «размер вне градации» бессмысленно.
	resp.Caveats = append(spec.Caveats, resp.Caveats...)
	return resp, nil
}
