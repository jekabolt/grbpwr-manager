package dto

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var fittingStatusPbToEntity = map[pb_common.FittingStatus]entity.FittingStatus{
	pb_common.FittingStatus_FITTING_STATUS_PLANNED:   entity.FittingPlanned,
	pb_common.FittingStatus_FITTING_STATUS_DONE:      entity.FittingDone,
	pb_common.FittingStatus_FITTING_STATUS_CANCELLED: entity.FittingCancelled,
}

var fittingStatusEntityToPb = map[entity.FittingStatus]pb_common.FittingStatus{
	entity.FittingPlanned:   pb_common.FittingStatus_FITTING_STATUS_PLANNED,
	entity.FittingDone:      pb_common.FittingStatus_FITTING_STATUS_DONE,
	entity.FittingCancelled: pb_common.FittingStatus_FITTING_STATUS_CANCELLED,
}

var fittingVerdictPbToEntity = map[pb_common.FittingVerdict]entity.FittingVerdict{
	pb_common.FittingVerdict_FITTING_VERDICT_PENDING:      entity.FittingPending,
	pb_common.FittingVerdict_FITTING_VERDICT_APPROVED:     entity.FittingApproved,
	pb_common.FittingVerdict_FITTING_VERDICT_NEEDS_REWORK: entity.FittingNeedsRework,
	pb_common.FittingVerdict_FITTING_VERDICT_REJECTED:     entity.FittingRejected,
}

var fittingVerdictEntityToPb = map[entity.FittingVerdict]pb_common.FittingVerdict{
	entity.FittingPending:     pb_common.FittingVerdict_FITTING_VERDICT_PENDING,
	entity.FittingApproved:    pb_common.FittingVerdict_FITTING_VERDICT_APPROVED,
	entity.FittingNeedsRework: pb_common.FittingVerdict_FITTING_VERDICT_NEEDS_REWORK,
	entity.FittingRejected:    pb_common.FittingVerdict_FITTING_VERDICT_REJECTED,
}

// ConvertPbFittingInsertToEntity converts a pb_common.FittingInsert to entity,
// validating the product, date, and sizes. Status/verdict default to
// planned/pending when unset.
func ConvertPbFittingInsertToEntity(pb *pb_common.FittingInsert) (*entity.FittingInsert, error) {
	if pb == nil {
		return nil, fmt.Errorf("fitting insert is nil")
	}
	if pb.ProductId < 0 || pb.TechCardId < 0 {
		return nil, fmt.Errorf("fitting product_id and tech_card_id must not be negative")
	}
	// A fitting must anchor to the style and/or the specific colour sample.
	if pb.ProductId <= 0 && pb.TechCardId <= 0 {
		return nil, fmt.Errorf("fitting requires product_id or tech_card_id")
	}
	if pb.FittingDate == nil {
		return nil, fmt.Errorf("fitting_date is required")
	}
	// recorded_by is deprecated (§2.7): the recorder is now the server-stamped created_by. Ignored on write.

	// Default only when explicitly unset; reject any other unmapped value
	// instead of silently coercing it to the default.
	status := entity.FittingPlanned
	if pb.Status != pb_common.FittingStatus_FITTING_STATUS_UNKNOWN {
		v, ok := fittingStatusPbToEntity[pb.Status]
		if !ok {
			return nil, fmt.Errorf("unknown fitting status: %v", pb.Status)
		}
		status = v
	}
	verdict := entity.FittingPending
	if pb.Verdict != pb_common.FittingVerdict_FITTING_VERDICT_UNKNOWN {
		v, ok := fittingVerdictPbToEntity[pb.Verdict]
		if !ok {
			return nil, fmt.Errorf("unknown fitting verdict: %v", pb.Verdict)
		}
		verdict = v
	}

	sizes := make([]entity.FittingSize, 0, len(pb.Sizes))
	seen := make(map[int]bool, len(pb.Sizes))
	for _, sz := range pb.Sizes {
		if sz.SizeId <= 0 {
			return nil, fmt.Errorf("fitting size size_id is required")
		}
		if seen[int(sz.SizeId)] {
			return nil, fmt.Errorf("duplicate fitting size_id: %d", sz.SizeId)
		}
		seen[int(sz.SizeId)] = true
		sizes = append(sizes, entity.FittingSize{
			SizeId:  int(sz.SizeId),
			FitNote: nullStringFromPb(sz.FitNote),
		})
	}

	mediaIds := make([]int, 0, len(pb.MediaIds))
	for _, mid := range pb.MediaIds {
		mediaIds = append(mediaIds, int(mid))
	}

	patterns := make([]entity.FittingPattern, 0, len(pb.Patterns))
	for _, p := range pb.Patterns {
		if p.SizeId < 0 {
			return nil, fmt.Errorf("fitting pattern size_id must not be negative")
		}
		url := strings.TrimSpace(p.Url)
		if url == "" {
			return nil, fmt.Errorf("fitting pattern url is required")
		}
		if len(url) > maxVarchar1024 {
			return nil, fmt.Errorf("fitting pattern url must be at most %d characters", maxVarchar1024)
		}
		if !isHTTPURL(url) {
			return nil, fmt.Errorf("fitting pattern url must be an http(s) URL")
		}
		// Managed-object check, same rationale as parseTechCardPatterns (Ф7).
		if _, ok := managedPatternObjectKey(url); !ok {
			return nil, fmt.Errorf("fitting pattern url must be an uploaded pattern object url")
		}
		if len(p.Filename) > maxVarchar255 {
			return nil, fmt.Errorf("fitting pattern filename must be at most %d characters", maxVarchar255)
		}
		if p.SizeBytes < 0 {
			return nil, fmt.Errorf("fitting pattern size_bytes must not be negative")
		}
		// name keeps its proto presence — Valid=false (absent) tells the store to carry the
		// stored name forward; an explicit empty string clears. See TechCardSizePattern.name.
		var name sql.NullString
		if p.Name != nil {
			trimmed := strings.TrimSpace(p.GetName())
			if len(trimmed) > maxVarchar255 {
				return nil, fmt.Errorf("fitting pattern name must be at most %d characters", maxVarchar255)
			}
			name = sql.NullString{String: trimmed, Valid: true}
		}
		patterns = append(patterns, entity.FittingPattern{
			SizeId:    nullInt32FromPb(p.SizeId),
			URL:       url,
			Filename:  nullStringFromPb(p.Filename),
			Name:      name,
			SizeBytes: nullInt64FromPb(p.SizeBytes),
		})
	}

	callouts := make([]entity.FittingCallout, 0, len(pb.Callouts))
	for ci, c := range pb.Callouts {
		path := fmt.Sprintf("callouts[%d]", ci)
		// Отказы этого блока НАЗЫВАЮТ ВЫНОСКУ. Плоская строка «fitting callout note is required» на
		// примерке с тридцатью выносками не говорит, какую из них чинить, и человек ищет её глазами
		// — при том что индекс у сервера был.
		if c.Number < 0 {
			return nil, entity.NewFieldViolation(path+".number", "invalid", fmt.Sprint(c.Number),
				"the callout number is what a remark refers to it by; it is never negative")
		}
		if c.MediaId < 0 {
			return nil, entity.NewFieldViolation(path+".media_id", "invalid", fmt.Sprint(c.MediaId),
				"a callout is either tied to a picture or not tied at all (0); there is no negative media")
		}
		// Маркер читается ТОЙ ЖЕ охраняемой проверкой, что и якоря фигуры (unitIntervalNull):
		// показатель степени в координате стоит одинаково дорого, в каком бы из полей он ни приехал.
		posX, err := unitIntervalNull(path+".pos_x", c.PosX)
		if err != nil {
			return nil, err
		}
		posY, err := unitIntervalNull(path+".pos_y", c.PosY)
		if err != nil {
			return nil, err
		}
		// ТОТ ЖЕ свод, что у карточной выноски (calloutGeometryFromPb): вид из закрытого списка,
		// число якорей по виду, координаты в кадре, цвет из закрытого списка, приведение
		// бессмысленных пунктира и штриховки. Второй валидатор той же фигуры разошёлся бы с первым
		// молча — и увидели бы это только на переносе замечания примерки в тех-карту.
		geom, err := calloutGeometryFromPb(path, fittingCalloutGeometryPb(c))
		if err != nil {
			return nil, err
		}
		// ЗАПИСКА ОБЯЗАТЕЛЬНА ТОЛЬКО У ПИНА, и проверяется ПОСЛЕ разбора фигуры, потому что до него
		// неизвестно, чего требовать.
		//
		// У пина текст и есть всё содержание: нумерованная точка без слов не сообщает ничего, её
		// нечего читать. У фигуры содержание — САМА ФИГУРА: обведённая зона заломов, дуга по окату,
		// мерка между двумя точками уже сказали, что не так, и требовать к ним подпись — это
		// требовать подпись к предложению. Клиенту при таком требовании остаётся выбрасывать
		// безымянные фигуры перед отправкой, то есть человек обводит зону, сохраняет и обнаруживает,
		// что её нет: молчаливая потеря нарисованного руками.
		//
		// ТРЕБУЕМ ТОЛЬКО У ЯВНО ОБЪЯВЛЕННОГО ПИНА. Вкладка со старым бандлом про вид молчит вовсе, а
		// её выноска на сервере запросто мерка или зона — её геометрия приедет переносом уже ПОСЛЕ
		// этой проверки (CarryOmittedFittingCalloutGeometry). Потребовать записку под молчание
		// значило бы судить о содержании по полю, которого клиент не касался, и отвергнуть всю
		// примерку за фигуру, которую сам же сервер сейчас и восстановит. Цена решения: старый
		// клиент может завести пин совсем без текста — бесполезный, но безвредный, и его собственный
		// интерфейс текста всё равно требует.
		note := strings.TrimSpace(c.Note)
		if note == "" && c.Kind != nil && geom.Kind == entity.AnnotationKindPin {
			return nil, entity.NewFieldViolation(path+".note", "required", "",
				"for a numbered point the note is the whole content: write what's wrong, or draw a shape")
		}
		if len(note) > maxTaskText {
			return nil, entity.NewFieldViolation(path+".note", "too_long", "",
				fmt.Sprintf("a callout note is no longer than %d characters; a full remark lives in the change list", maxTaskText))
		}
		callouts = append(callouts, entity.FittingCallout{
			Number:  int(c.Number),
			Note:    nullStringFromPb(note),
			MediaId: nullInt32FromPb(c.MediaId),
			PosX:    posX,
			PosY:    posY,
			Kind:    geom.Kind,
			Points:  geom.Points,
			Color:   geom.Color,
			Dashed:  geom.Dashed,
			Filled:  geom.Filled,
			// Группа атомарна: молчание про вид — молчание про всю геометрию, и хранимая
			// переносится по номеру выноски (CarryOmittedFittingCalloutGeometry).
			KindOmitted: c.Kind == nil,
		})
	}

	outcome := nullStringFromPb("")
	if o := strings.ToLower(strings.TrimSpace(pb.Outcome)); o != "" {
		if !entity.ValidFittingOutcomes[entity.FittingOutcome(o)] {
			return nil, fmt.Errorf("fitting outcome must be one of approved|new_round|dropped")
		}
		outcome = nullStringFromPb(o)
	}
	if pb.RoundNumber < 0 {
		return nil, fmt.Errorf("fitting round_number must not be negative")
	}

	changeRequests := make([]entity.FittingChangeRequest, 0, len(pb.ChangeRequests))
	for _, cr := range pb.ChangeRequests {
		e, err := fittingChangeRequestEntity(cr.Target, cr.Note, cr.CalloutNumber, cr.PieceId, cr.CarriedFromId, cr.PieceIds, cr.Zone, cr.Status)
		if err != nil {
			return nil, err
		}
		changeRequests = append(changeRequests, e)
	}

	// Normalize to a UTC calendar date so storage into the DATE column is
	// deterministic regardless of the incoming timestamp's time-of-day.
	// (Clients should send the fitting date at UTC midnight.)
	ft := pb.FittingDate.AsTime().UTC()
	fittingDate := time.Date(ft.Year(), ft.Month(), ft.Day(), 0, 0, 0, 0, time.UTC)

	return &entity.FittingInsert{
		TechCardId:     nullInt32FromPb(pb.TechCardId),
		ProductId:      nullInt32FromPb(pb.ProductId),
		ModelId:        nullInt32FromPb(pb.ModelId),
		FittingDate:    fittingDate,
		Comment:        nullStringFromPb(pb.Comment),
		Status:         status,
		Verdict:        verdict,
		RoundNumber:    nullInt32FromPb(pb.RoundNumber),
		Outcome:        outcome,
		SampleId:       nullInt32FromPb(pb.SampleId),
		Sizes:          sizes,
		MediaIds:       mediaIds,
		Patterns:       patterns,
		Callouts:       callouts,
		ChangeRequests: changeRequests,
	}, nil
}

// ConvertEntityFittingToPb converts an entity.Fitting to pb_common.Fitting,
// including resolved media.
func ConvertEntityFittingToPb(f *entity.Fitting) *pb_common.Fitting {
	if f == nil {
		return nil
	}

	sizes := make([]*pb_common.FittingSizeInsert, 0, len(f.Sizes))
	for _, sz := range f.Sizes {
		sizes = append(sizes, &pb_common.FittingSizeInsert{
			SizeId:  int32(sz.SizeId),
			FitNote: pbStringFromNull(sz.FitNote),
		})
	}

	media := make([]*pb_common.MediaFull, 0, len(f.Media))
	mediaIds := make([]int32, 0, len(f.Media))
	for i := range f.Media {
		media = append(media, ConvertEntityToCommonMedia(&f.Media[i]))
		mediaIds = append(mediaIds, int32(f.Media[i].Id))
	}

	return &pb_common.Fitting{
		Id: int32(f.Id),
		Fitting: &pb_common.FittingInsert{
			TechCardId:     pbInt32FromNull(f.TechCardId),
			ProductId:      pbInt32FromNull(f.ProductId),
			ModelId:        pbInt32FromNull(f.ModelId),
			FittingDate:    timestamppb.New(f.FittingDate),
			Comment:        pbStringFromNull(f.Comment),
			Status:         fittingStatusEntityToPb[f.Status],
			Verdict:        fittingVerdictEntityToPb[f.Verdict],
			RecordedBy:     f.CreatedBy, // deprecated field: mirror the server-stamped recorder for back-compat
			RoundNumber:    pbInt32FromNull(f.RoundNumber),
			Outcome:        f.Outcome.String,
			SampleId:       pbInt32FromNull(f.SampleId),
			Sizes:          sizes,
			MediaIds:       mediaIds,
			Patterns:       fittingPatternsToPb(f.Patterns),
			Callouts:       fittingCalloutsToPb(f.Callouts),
			ChangeRequests: fittingChangeRequestsToPb(f.ChangeRequests),
		},
		Media:       media,
		LockVersion: int32(f.LockVersion),
		CreatedBy:   f.CreatedBy,
		UpdatedBy:   f.UpdatedBy,
		CreatedAt:   timestamppb.New(f.CreatedAt),
		UpdatedAt:   timestamppb.New(f.UpdatedAt),
	}
}

// fittingChangeRequestsToPb emits a fitting's structured change requests for display.
func fittingChangeRequestsToPb(crs []entity.FittingChangeRequest) []*pb_common.FittingChangeRequest {
	out := make([]*pb_common.FittingChangeRequest, 0, len(crs))
	for _, c := range crs {
		out = append(out, ConvertEntityFittingChangeRequestToPb(c))
	}
	return out
}

// ConvertEntityFittingChangeRequestToPb converts one stored change-request item to pb (S26). resolved
// is a deprecated read-only mirror of status, and piece_id a deprecated read-only mirror of the first
// piece — both are emitted so a client on the previous contract still renders the row.
func ConvertEntityFittingChangeRequestToPb(c entity.FittingChangeRequest) *pb_common.FittingChangeRequest {
	pieceIDs := make([]int32, 0, len(c.PieceIds))
	for _, id := range c.PieceIds {
		pieceIDs = append(pieceIDs, int32(id))
	}
	var firstPiece int32
	if len(pieceIDs) > 0 {
		firstPiece = pieceIDs[0]
	}
	return &pb_common.FittingChangeRequest{
		Id:            int32(c.Id),
		FittingId:     int32(c.FittingId),
		Target:        c.Target,
		Note:          c.Note,
		CalloutNumber: pbInt32FromNull(c.CalloutNumber),
		Zone:          c.Zone.String,
		PieceIds:      pieceIDs,
		PieceId:       firstPiece,
		Status:        c.Status,
		CarriedFromId: pbInt32FromNull(c.CarriedFromId),
		CreatedBy:     c.CreatedBy,
		RoundNumber:   pbInt32FromNull(c.RoundNumber),
		Resolved:      c.Status == entity.FittingChangeStatusResolved,
	}
}

// maxFittingChangeRequestPieces bounds the piece set on one remark. A style's whole cut-list is well
// under this, so the cap only catches a client looping — it is not a modelling limit.
const maxFittingChangeRequestPieces = 100

// fittingChangeRequestPieceIds validates the piece set of a change request: positive ids only,
// de-duplicated, selection order preserved. pieceID is the DEPRECATED single-piece field — it is
// honoured only when the set is empty, so an older client that still sends piece_id keeps working
// and a current client that sends both does not get a phantom extra piece.
func fittingChangeRequestPieceIds(pieceIDs []int32, pieceID int32) ([]int, error) {
	src := pieceIDs
	if len(src) == 0 && pieceID > 0 {
		src = []int32{pieceID}
	}
	if len(src) > maxFittingChangeRequestPieces {
		return nil, fmt.Errorf("fitting change request must not reference more than %d pieces", maxFittingChangeRequestPieces)
	}
	out := make([]int, 0, len(src))
	seen := make(map[int32]bool, len(src))
	for _, id := range src {
		if id < 0 {
			return nil, fmt.Errorf("fitting change request piece_ids must not be negative")
		}
		// 0 is the proto zero value for "unset", not a piece — a client building a fixed-length row
		// can legitimately leave a slot empty. Skip rather than reject.
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, int(id))
	}
	return out, nil
}

// fittingChangeRequestEntity validates and converts the shared change-request fields (S26), used by
// both the embedded initial batch (FittingInsert) and the dedicated CRUD.
func fittingChangeRequestEntity(target, note string, calloutNumber, pieceID, carriedFromID int32, pieceIDs []int32, zone, status string) (entity.FittingChangeRequest, error) {
	t := strings.ToLower(strings.TrimSpace(target))
	if !entity.ValidFittingChangeTargets[t] {
		return entity.FittingChangeRequest{}, fmt.Errorf("fitting change request target must be one of pattern|construction|material|grading|other")
	}
	n := strings.TrimSpace(note)
	if n == "" {
		return entity.FittingChangeRequest{}, fmt.Errorf("fitting change request note is required")
	}
	if len(n) > maxTaskText {
		return entity.FittingChangeRequest{}, fmt.Errorf("fitting change request note must be at most %d characters", maxTaskText)
	}
	if calloutNumber < 0 || pieceID < 0 || carriedFromID < 0 {
		return entity.FittingChangeRequest{}, fmt.Errorf("fitting change request callout_number, piece_id and carried_from_id must not be negative")
	}
	pieces, err := fittingChangeRequestPieceIds(pieceIDs, pieceID)
	if err != nil {
		return entity.FittingChangeRequest{}, err
	}
	// Normalized, not just lowercased: a client sending the TECH_CARD_CONSTRUCTION_ZONE_* enum name
	// (which the admin did before this field grew its own dictionary) is mapped onto its token.
	z := entity.NormalizeFittingChangeZone(zone)
	if z != "" && !entity.ValidFittingChangeZones[z] {
		return entity.FittingChangeRequest{}, fmt.Errorf("fitting change request zone must be one of %s", strings.Join(entity.FittingChangeZoneTokens(), "|"))
	}
	st := strings.ToLower(strings.TrimSpace(status))
	if st == "" {
		st = entity.FittingChangeStatusOpen
	}
	if !entity.ValidFittingChangeStatuses[st] {
		return entity.FittingChangeRequest{}, fmt.Errorf("fitting change request status must be open or resolved")
	}
	return entity.FittingChangeRequest{
		Target:        t,
		Note:          n,
		CalloutNumber: nullInt32FromPb(calloutNumber),
		Zone:          nullStringFromPb(z),
		PieceIds:      pieces,
		Status:        st,
		CarriedFromId: nullInt32FromPb(carriedFromID),
	}, nil
}

// ConvertPbFittingChangeRequestInsertToEntity validates a dedicated change-request write payload (S26).
func ConvertPbFittingChangeRequestInsertToEntity(pb *pb_common.FittingChangeRequestInsert) (*entity.FittingChangeRequest, error) {
	if pb == nil {
		return nil, fmt.Errorf("change_request is required")
	}
	e, err := fittingChangeRequestEntity(pb.Target, pb.Note, pb.CalloutNumber, pb.PieceId, pb.CarriedFromId, pb.PieceIds, pb.Zone, pb.Status)
	if err != nil {
		return nil, err
	}
	e.FittingId = int(pb.FittingId)
	return &e, nil
}

// fittingCalloutsToPb emits a fitting's photo callouts for display.
func fittingCalloutsToPb(cs []entity.FittingCallout) []*pb_common.FittingCallout {
	out := make([]*pb_common.FittingCallout, 0, len(cs))
	for _, c := range cs {
		out = append(out, &pb_common.FittingCallout{
			Number:  int32(c.Number),
			Note:    pbStringFromNull(c.Note),
			MediaId: pbInt32FromNull(c.MediaId),
			PosX:    pbDecimalFromNull(c.PosX),
			PosY:    pbDecimalFromNull(c.PosY),
			// Вид на чтении ВСЕГДА присутствует, и пустой хранимый читается как PIN: так примерка,
			// записанная до 0319, читается как то, чем она была, а новый клиент возвращает круглым
			// рейсом присутствующее поле — то есть никогда не молчит про геометрию по ошибке.
			Kind:   calloutKindPbPtr(c.Kind),
			Points: calloutPointsToPb(c.Points),
			Color:  annotationColorToPb[c.Color],
			Dashed: c.Dashed,
			Filled: c.Filled,
		})
	}
	return out
}

// fittingPatternsToPb emits a fitting's PDF выкройка iterations for display.
func fittingPatternsToPb(ps []entity.FittingPattern) []*pb_common.FittingPattern {
	out := make([]*pb_common.FittingPattern, 0, len(ps))
	for _, p := range ps {
		out = append(out, &pb_common.FittingPattern{
			SizeId:    pbInt32FromNull(p.SizeId),
			Url:       p.URL,
			Filename:  pbStringFromNull(p.Filename),
			Name:      pbOptStringFromNull(p.Name),
			SizeBytes: p.SizeBytes.Int64,
		})
	}
	return out
}
