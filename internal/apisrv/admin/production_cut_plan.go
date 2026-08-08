package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// GetProductionRunCutPlan projects the run's lines onto the style's cut pieces: деталь × колорвей ×
// размер → сколько панелей выкроить и из какого артикула. Read-only; writes nothing.
//
// Сестра GetProductionRunMaterialPlan, и загрузка списана с неё намеренно — тот же прогон, тот же
// способ отвечать NotFound/Internal, тот же гейт (rd(SectionProduction)). Денег в ответе нет ни в
// каком виде, поэтому RBAC-стрипа костинга здесь тоже нет, и его отсутствие — решение, а не
// упущение: этот же ответ уезжает в публичный манифест наряда, где стрипать некому.
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

	card, release, caveats, err := s.cutPlanSpec(ctx, run)
	if err != nil {
		return nil, err
	}
	resp := dto.ComputeProductionRunCutPlan(run, card, release)
	// Оговорки о ВЫБОРЕ спецификации идут первыми: они объясняют, по какой карточке посчитано всё
	// остальное, и читать их после списка «размер вне градации» бессмысленно.
	resp.Caveats = append(caveats, resp.Caveats...)
	return resp, nil
}

// cutPlanSpec выбирает карточку, по которой печатается наряд, и говорит, чем пришлось пожертвовать.
//
// РЕЛИЗ ПОБЕЖДАЕТ ЖИВУЮ КАРТОЧКУ, когда прогон к нему привязан, и это не вкус: фабрика кроит по
// замороженной спецификации, а живая карточка меняется каждый день. Напечатать наряд по живой
// карточке для прогона с релизом — значит выдать в цех крой, которого никто не утверждал.
//
// НО НЕЧИТАЕМЫЙ СНАПШОТ — НЕ 500. Блоб заморожен вместе со схемой, которая с тех пор уехала;
// правило hero-v2 (и GetTechCardRelease рядом) говорит деградировать, а не падать. Наряд по живой
// карточке с громкой оговоркой лучше, чем пустой экран у закройщика; release_id в ответе при этом
// остаётся НУЛЁМ — поле называет карточку, по которой ПОСЧИТАНО, а не ту, на которую ссылается
// прогон, и соврать в нём означало бы подписать чужой ревизией.
func (s *Server) cutPlanSpec(ctx context.Context, run *entity.ProductionRun) (*entity.TechCard, *entity.TechCardReleaseMeta, []string, error) {
	var caveats []string
	if run.ReleaseId.Valid && run.ReleaseId.Int64 > 0 {
		relID := int(run.ReleaseId.Int64)
		rel, err := s.repo.TechCards().GetTechCardRelease(ctx, relID)
		switch {
		// `err == nil && rel == nil` лежит в одной ветке с ErrNoRows намеренно: это то же «релиза
		// нет», и без него оно провалилось бы в default, где первым же действием разыменовывается.
		case errors.Is(err, sql.ErrNoRows), err == nil && rel == nil:
			caveats = append(caveats, fmt.Sprintf(
				"релиз #%d, на который ссылается прогон, не найден — наряд посчитан по ЖИВОЙ карточке", relID))
		case err != nil:
			slog.Default().ErrorContext(ctx, "can't load release for cut plan", slog.String("err", err.Error()))
			return nil, nil, nil, status.Error(codes.Internal, "can't load release")
		default:
			// РЕЛИЗ ОБЯЗАН БЫТЬ РЕЛИЗОМ ЭТОЙ КАРТОЧКИ. В схеме это две независимые ссылки
			// (production_run.release_id и production_run.tech_card_id), составного ключа между ними
			// нет, и никто по дороге их не сверяет — снапшот-костинг прогона проверяет только, что
			// релиз существует. Не проверить здесь значит уметь напечатать цеху наряд по ЧУЖОЙ
			// карточке: детали, ткани и артикулы другого стиля под номером этой партии.
			if rel.TechCardId != run.TechCardId {
				slog.Default().WarnContext(ctx, "cut plan: run points at a release of another tech card",
					slog.Int("run_id", run.Id), slog.Int("release_id", relID),
					slog.Int("release_tech_card_id", rel.TechCardId), slog.Int("run_tech_card_id", run.TechCardId))
				caveats = append(caveats, fmt.Sprintf(
					"релиз #%d принадлежит другой карточке — наряд посчитан по ЖИВОЙ карточке этого прогона", relID))
				break
			}
			var snap pb_common.TechCard
			if uerr := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(rel.Snapshot), &snap); uerr != nil {
				// Текст ошибки цитирует поле замороженного блоба (а тот несёт костинг и цены BOM) —
				// в оговорку, которая уедет на бумагу и в публичный манифест, он не попадает.
				slog.Default().WarnContext(ctx, "cut plan: release snapshot won't parse",
					slog.Int("release_id", relID), slog.String("err", uerr.Error()))
				caveats = append(caveats, fmt.Sprintf(
					"снапшот релиза Rev.%d не читается текущей схемой — наряд посчитан по ЖИВОЙ карточке, а она могла измениться после релиза",
					rel.ReleaseNumber))
				break
			}
			card := dto.CutSpecCardFromReleaseSnapshot(&snap)
			if card == nil {
				caveats = append(caveats, fmt.Sprintf(
					"снапшот релиза Rev.%d пуст — наряд посчитан по ЖИВОЙ карточке, а она могла измениться после релиза",
					rel.ReleaseNumber))
				break
			}
			s.resolveCutSpecArticles(ctx, card)
			meta := rel.TechCardReleaseMeta
			return card, &meta, caveats, nil
		}
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, run.TechCardId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't load tech card for cut plan", slog.String("err", err.Error()))
		return nil, nil, nil, status.Error(codes.Internal, "can't load tech card")
	}
	return card, nil, caveats, nil
}

// resolveCutSpecArticles возвращает карточке из снапшота каталожные имена артикулов.
//
// Живое чтение приносит их само (TechCard.LinkedMaterials), а замороженный блок их не несёт — и без
// них наряд напечатал бы в колонке артикула ИМЯ РОЛИ слота: «основная ткань» вместо названия ткани.
// У колорвея с пином это уже не бедность, а ложь — пин и умолчание слота получили бы под разными
// material_id одну и ту же подпись, то есть исчезла бы ровно та разница, ради которой закройщик и
// смотрит в наряд.
//
// Множество то же, что у материального плана: умолчания слотов ПЛЮС пины рецептов. Промах чтения
// деградирует к снимку строки BOM и не валит запрос — наряд без каталожного имени всё ещё наряд.
func (s *Server) resolveCutSpecArticles(ctx context.Context, card *entity.TechCard) {
	seen := make(map[int]bool, len(card.BomItems))
	ids := make([]int, 0, len(card.BomItems))
	add := func(id int) {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for i := range card.BomItems {
		if card.BomItems[i].MaterialId.Valid {
			add(int(card.BomItems[i].MaterialId.Int64))
		}
	}
	for i := range card.Colorways {
		for j := range card.Colorways[i].Usages {
			if u := &card.Colorways[i].Usages[j]; u.MaterialId.Valid {
				add(int(u.MaterialId.Int64))
			}
		}
	}
	if len(ids) == 0 {
		return
	}
	linked := make(map[int]entity.MaterialWithPrice, len(ids))
	for _, mid := range ids {
		m, err := s.repo.TechCards().GetMaterial(ctx, mid)
		if err != nil || m == nil {
			continue
		}
		linked[mid] = *m
	}
	card.LinkedMaterials = linked
}
