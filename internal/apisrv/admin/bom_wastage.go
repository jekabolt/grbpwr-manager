package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// measuredLayScanLimit bounds how many freshest measured настилы the wastage suggestion (and the
// claim verification below) considers — правки ревью T7в2, MAJOR 5. 50, and the number is argued,
// not felt: the worst case per candidate is one card load (deduped by model — настилы артикула
// кучкуются по немногим карточкам) plus one раскладки load per run, so the ceiling is ~a hundred
// point reads, well under the gateway timeout; while a median over the 50 freshest cuts already
// dominates any older tail — межлекальные выпады дрейфуют с рецептом, and last year's lays would
// only dilute today's answer. The window is NAMED in the response (considered/total), never
// implied.
const measuredLayScanLimit = 50

// GetBomWastageSuggestion предлагает ПРОЦЕНТ РАСКРОЯ (bom_item.wastage_percent) по факту настилов
// артикула (T7 волна 2): медиана «факт ÷ netto − 1» по настилам, где есть и замер, и
// netto-знаменатель.
//
// ЭТО СОСЕД, А НЕ КОПИЯ GetMaterialCuttingCoefficientSuggestion. Оба сканируют одни и те же
// настилы, но делят факт на РАЗНЫЕ знаменатели: тот — на план-геометрию (выпады внутри → медиана
// меряет усадку/пороки, 2–6%, для МНОЖИТЕЛЯ артикула), этот — на netto «по выкройкам» (выпадов нет
// → медиана меряет выпады + концы + усадку, 15–30%, для ПРОЦЕНТА строки BOM). Шапка
// dto/bom_wastage_calibration.go — обязательное чтение до любой правки.
//
// ПРЕДЛОЖЕНИЕ, А НЕ ПРИМЕНЕНИЕ: обработчик ничего не пишет. TOO_FEW_FACTS — штатный ответ, не
// ошибка: на пустой базе настилов он ОСНОВНОЙ, и клиент говорит «фактов ещё нет».
func (s *Server) GetBomWastageSuggestion(ctx context.Context,
	req *pb_admin.GetBomWastageSuggestionRequest,
) (*pb_admin.GetBomWastageSuggestionResponse, error) {

	materialID := int(req.GetMaterialId())
	if materialID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "material_id is required")
	}

	// Артикул читается ради ЧЕСТНОСТИ ответа, как у соседа: «у такого артикула фактов нет» и
	// «такого артикула нет» — разные ответы, и второй нельзя выдавать под видом первого.
	if _, err := s.repo.TechCards().GetMaterial(ctx, materialID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "material not found")
		}
		slog.Default().ErrorContext(ctx, "can't get material for bom wastage suggestion",
			slog.Int("material_id", materialID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get material")
	}

	in, err := s.bomWastageCalibrationInput(ctx, materialID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't assemble bom wastage suggestion input",
			slog.Int("material_id", materialID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load measured lays")
	}

	return dto.BuildBomWastageSuggestion(in), nil
}

// bomWastageCalibrationInput loads everything ONE material's wastage suggestion reads — shared by
// the RPC above and by the write-side claim verification (verifyBomWastageClaims), so both stand on
// the SAME median.
//
// Loading discipline (правки ревью T7в2, MAJOR 5): the candidate selection is capped to the
// measuredLayScanLimit freshest замеры (the store also reports how many exist in total — the
// response names the window). Run раскладки are loaded ONLY for runs that still need a LIVE netto
// recompute — a настил whose stamp survives the self-check (entity.TrustedNettoStamp) never touches
// them. CARDS are loaded for every настил regardless, and that is deliberate, not a missed
// optimisation: the card is what resolves the настил to its article (dto.LayArticleMaterialId —
// колорвей может пинить ЧУЖУЮ ткань поверх слота), and skipping it would let another article's lays
// stand under this article's median. They are deduped by model, and настилы артикула кучкуются по
// немногим карточкам.
func (s *Server) bomWastageCalibrationInput(ctx context.Context, materialID int) (dto.BomWastageCalibrationInput, error) {
	lays, total, err := s.repo.ProductionRuns().ListMeasuredLayCandidates(ctx, materialID, measuredLayScanLimit)
	if err != nil {
		return dto.BomWastageCalibrationInput{}, fmt.Errorf("failed to list measured lays: %w", err)
	}

	// Карточки — настоящим загрузчиком и по одной на карточку (резолвер артикула откатывается на
	// позиционный индекс слота, осмысленный только в порядке настоящего загрузчика). НЕ best-effort:
	// ненагруженная карточка молча вынула бы её настилы из медианы — «фактов мало» при полном
	// журнале замеров.
	cards := make(map[int]*entity.TechCard)
	// Раскладки — ПО ПРОГОНАМ (ListRunMarkers: summaries с составом, без блобов): секции настила
	// всегда называют раскладки СВОЕГО прогона (0282). Состав нужен живому пересчёту netto у
	// настилов без (доверенного) штампа; читает его тот же владелец, что и всюду —
	// CompositionOrLegacy.
	markers := make(map[int]entity.TechCardMarkerSummary)
	seenRuns := make(map[int]bool)
	for i := range lays {
		if id := lays[i].TechCardId; id > 0 && cards[id] == nil {
			card, err := s.repo.TechCards().GetTechCardById(ctx, id)
			if err != nil {
				return dto.BomWastageCalibrationInput{}, fmt.Errorf("failed to load tech card %d of a measured lay: %w", id, err)
			}
			cards[id] = card
		}
		if stamp, _ := lays[i].TrustedNettoStamp(); stamp.Valid {
			continue // штамп доверен — живой пересчёт (и раскладки) этому настилу не нужны
		}
		if runID := lays[i].RunId; runID > 0 && !seenRuns[runID] {
			seenRuns[runID] = true
			runMarkers, err := s.repo.TechCards().ListRunMarkers(ctx, runID)
			if err != nil {
				return dto.BomWastageCalibrationInput{}, fmt.Errorf("failed to load раскладки of run %d: %w", runID, err)
			}
			for j := range runMarkers {
				markers[runMarkers[j].Id] = runMarkers[j]
			}
		}
	}

	return dto.BomWastageCalibrationInput{
		MaterialId:            materialID,
		Lays:                  lays,
		Cards:                 cards,
		Markers:               markers,
		ConsideredLayCount:    len(lays),
		TotalMeasuredLayCount: total,
	}, nil
}

// verifyBomWastageClaims — гейт свежих заявок провенанса 'lays' на сохранении карточки (правки
// ревью T7в2, MAJOR 3). ПРАВИЛО — О СУТИ, НЕ О ПРИСУТСТВИИ ПОЛЕЙ: источник «медиана по N
// раскроям» — это УТВЕРЖДЕНИЕ, что присланное число и есть текущая медиана сервера, и сервер
// проверяет утверждение, а не эхо. Три исхода:
//
//   - чистое эхо (stored-бейдж жив, процент и счётчик не менялись) — пропускается без проверки, и
//     store сохранит его verbatim, не двигая штамп даты;
//   - свежая заявка, СОВПАВШАЯ с текущим предложением (процент И счётчик) — помечается
//     WastageClaimVerified, и только эта метка открывает store'у дверь к бейджу;
//   - всё остальное — метка не ставится, и ResolveBomWastageProvenance положит строку как
//     'manual': изменил число — источник стал manual, что бы клиент ни прислал. Сейв НЕ падает:
//     упавший сейв на гонке «медиана уехала между GET и apply» наказывал бы честного оператора.
//
// Ошибка ЗАГРУЗКИ — отказ сейва, не тихий manual: транзиентный сбой не имеет права снимать бейдж
// (урок MAJOR 1). storedCard == nil — создание: у новой карточки эха не бывает, всё — заявки.
func (s *Server) verifyBomWastageClaims(ctx context.Context, storedCard *entity.TechCard,
	items []entity.TechCardBomItem) error {

	var storedByKey map[string]*entity.TechCardBomItem
	if storedCard != nil {
		storedByKey = make(map[string]*entity.TechCardBomItem, len(storedCard.BomItems))
		for i := range storedCard.BomItems {
			if k := strings.TrimSpace(storedCard.BomItems[i].LineKey); k != "" {
				storedByKey[k] = &storedCard.BomItems[i]
			}
		}
	}

	suggestions := make(map[int]dto.BomWastageSuggestion)
	for i := range items {
		b := &items[i]
		if b.WastageProvenanceOmitted || b.WastageSource != entity.BomWastageSourceLays {
			continue
		}
		if prior := storedByKey[strings.TrimSpace(b.LineKey)]; prior != nil {
			eff := prior.EffectiveWastageProvenance()
			if eff.Source == entity.BomWastageSourceLays &&
				prior.WastagePercent.Valid && b.WastagePercent.Valid &&
				prior.WastagePercent.Decimal.Equal(b.WastagePercent.Decimal) &&
				eff.LayCount.Valid && b.WastageLayCount.Valid &&
				eff.LayCount.Int64 == b.WastageLayCount.Int64 {
				continue // чистое эхо — store сохранит тройку verbatim
			}
		}
		// Свежая заявка. Проверять её не по чему без артикула: строка без каталожного материала
		// не имеет настилов — заявка не подтверждается и ляжет как manual.
		if !b.MaterialId.Valid || b.MaterialId.Int64 <= 0 {
			continue
		}
		matID := int(b.MaterialId.Int64)
		sug, ok := suggestions[matID]
		if !ok {
			in, err := s.bomWastageCalibrationInput(ctx, matID)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't verify bom wastage claim",
					slog.Int("material_id", matID), slog.String("err", err.Error()))
				return status.Error(codes.Internal, "can't verify the wastage provenance claim; try again")
			}
			obs, _ := dto.BomWastageObservationsOf(in)
			sug = dto.BomWastageSuggestionOf(obs)
			suggestions[matID] = sug
		}
		if sug.Status == dto.WastageSuggestionReady &&
			sug.SuggestedPercent.Valid && b.WastagePercent.Valid &&
			sug.SuggestedPercent.Decimal.Equal(b.WastagePercent.Decimal) &&
			b.WastageLayCount.Valid && int(b.WastageLayCount.Int64) == sug.LayCount {
			b.WastageClaimVerified = true
		}
	}
	return nil
}
