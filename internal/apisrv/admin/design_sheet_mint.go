package admin

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// MintDesignSheetVersion — АТОМАРНЫЙ МИНТ: обычная запись документа и рождение версии в ОДНОЙ
// транзакции.
//
// ХЕНДЛЕР НАСЛЕДУЕТ ВЕСЬ КОНВЕЙЕР ХЕНДЛЕРА СЕЙВА, И ЭТО ГЛАВНОЕ, ЧТО ОН ДЕЛАЕТ (П-Д). Он зовёт тот
// же prepareTechCardWrite, что и UpdateTechCard, — не копию списка шагов, а саму функцию, — потому
// что скопированный список расходится, а расходится он молча: первая выноска черновика (та самая,
// что рождает v1) замёрзла бы в версии с номером 0, а свежая подпись родилась бы протухшей и не
// лечилась переутверждением.
//
// И ОН ВКЛАДЫВАЕТ ПЛИТЫ ВЕРСТАКА В technical_media ДОКУМЕНТА (П-А) — ДО конвейера, потому что
// плиты обязаны попасть и в проекцию дайджеста DESIGN (она хеширует media), и в множество, по
// которому механизм «деталь кроя ↔ выноска» решает, оторвана ли деталь.
func (s *Server) MintDesignSheetVersion(ctx context.Context, req *pb_admin.MintDesignSheetVersionRequest) (*pb_admin.MintDesignSheetVersionResponse, error) {
	cardID := int(req.GetTechCardId())
	if cardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	if strings.TrimSpace(req.GetClientRequestId()) == "" {
		return nil, status.Error(codes.InvalidArgument,
			"client_request_id is required — without it a lost response mints a phantom version")
	}
	if req.GetTechCard() == nil {
		return nil, status.Error(codes.InvalidArgument,
			"the mint carries the whole card document: the version freezes callouts, and callouts are part of it")
	}
	if !entity.IsDesignMintedVia(req.GetMintedVia()) {
		return nil, status.Errorf(codes.InvalidArgument,
			"minted_via %q is not callout|print|release|share", req.GetMintedVia())
	}
	expected, err := designExpectedPlatesFromPb(req.GetExpectedPlates())
	if err != nil {
		return nil, err
	}

	// ВЕРСТАК ЧИТАЕТСЯ ЗДЕСЬ ТОЛЬКО ЗАТЕМ, ЧТОБЫ СОБРАТЬ ДОКУМЕНТ. Авторитетное чтение верстака —
	// внутри транзакции минта, там же стоит CAS по expected_plates и пояс «плиты обязаны быть в
	// документе». Расхождение между этими двумя чтениями поэтому не молчит: либо CAS скажет
	// bench_moved, либо пояс — plates_not_in_document.
	band, err := s.repo.Design().GetBand(ctx, cardID, 1)
	if err != nil {
		return nil, designError(ctx, "failed to read the design band before minting", err, nil)
	}

	// КЛОН, А НЕ ЗАПРОС. Мутировать входящее сообщение значит писать в чужую память: перехватчики,
	// логи и повтор gRPC видят тот же объект, и «сервер дописал две картинки» проявилось бы в
	// месте, где его никто не ищет.
	doc, _ := proto.Clone(req.GetTechCard()).(*pb_common.TechCardInsert)
	injectBenchPlatesAsTechnicalMedia(doc, band.Bench)

	tc, err := s.prepareTechCardWrite(ctx, cardID, doc)
	if err != nil {
		return nil, err
	}

	full, err := s.repo.Design().MintSheetVersion(ctx, entity.DesignSheetMint{
		TechCardId:          cardID,
		ClientRequestId:     strings.TrimSpace(req.GetClientRequestId()),
		TechCard:            tc,
		ExpectedLockVersion: int(req.GetExpectedLockVersion()),
		ExpectedPlates:      expected,
		MixedConsent:        req.GetMixedConsent(),
		UploadedFitConfirm:  req.GetUploadedFitConfirmed(),
		MintedVia:           req.GetMintedVia(),
		Actor:               designActor(ctx),
	})
	if err != nil {
		return nil, s.designMintError(ctx, err)
	}
	// ПОСЛЕ-КОММИТНАЯ ПОЛОВИНА ЗАПИСИ — ТОЛЬКО ЕСЛИ ЗАПИСЬ БЫЛА. Идемпотентный повтор ничего не
	// писал: он вернул уже созданную версию. Позвать здесь finalize значило бы удалить объекты
	// выкроек второй раз и пересеять стоимости под номером версии блокировки, которого уже нет.
	if !full.Idempotent {
		s.finalizeTechCardWrite(ctx, cardID, int(req.GetExpectedLockVersion()), full.OrphanedPatternURLs)
	}
	return &pb_admin.MintDesignSheetVersionResponse{
		Version: designSheetVersionToPb(ctx, full.Version),
	}, nil
}

// injectBenchPlatesAsTechnicalMedia — П-А, САМА ВКЛАДКА.
//
// ЧТО СЛОМАЕТСЯ БЕЗ НЕЁ. buildCalloutSync строит множество технических эскизов ровно из tc.Media с
// category='technical' (store/techcard/materials.go), и apply ставит Detached=true КАЖДОЙ детали
// кроя, чья выноска стоит на медиа вне этого множества. Новая карточка живёт плитами верстака,
// которых в tech_card_media нет ни одной, — значит после минта ВСЕ детали кроя стали бы
// оторванными, тех-пак печатал бы пустой эскиз, а подпись DESIGN не видела бы чертежа.
//
// ДЕЛАЕТ ЭТО СЕРВЕР, А НЕ ДИАЛОГ. План говорил «минт-диалог собирает документ, в котором плиты
// лежат как technical_media», но правило, исполняемое клиентом, это не правило: любой второй
// клиент (импорт, скрипт, старый бандл) получил бы молча оторванные детали. Сервер знает верстак
// точно, а пояс в транзакции минта отказывает, если плиты в документе всё-таки не оказалось.
//
// УЖЕ ПЕРЕЧИСЛЕННОЕ НЕ ТРОГАЕТСЯ. Клиент вправе прислать плиту сам (и со временем будет);
// дубликат создал бы вторую строку tech_card_media на то же медиа — UNIQUE там нет, и молчаливое
// удвоение эскиза увидели бы только на бумаге.
func injectBenchPlatesAsTechnicalMedia(doc *pb_common.TechCardInsert, bench []entity.DesignBenchSlot) {
	if doc == nil {
		return
	}
	present := make(map[int32]bool, len(doc.GetTechnicalMedia()))
	for _, m := range doc.GetTechnicalMedia() {
		present[m.GetMediaId()] = true
	}
	add := func(slot entity.DesignBenchSlot) {
		if slot.Picture == nil || slot.Picture.MediaId == 0 {
			return
		}
		id := int32(slot.Picture.MediaId)
		if present[id] {
			return
		}
		present[id] = true
		doc.TechnicalMedia = append(doc.TechnicalMedia, &pb_common.TechCardMediaItem{
			MediaId: id,
			Kind:    dto.PbTechCardMediaKind(entity.DesignPlateMediaKind(slot.ViewKey)),
			Caption: slot.DetailName.String,
		})
	}
	// Порядок тот же, что у состава версии: четыре стороны канонически, затем детали. Он виден
	// человеку — это порядок эскизов на карточке.
	for _, view := range entity.DesignSilhouetteViews {
		for _, slot := range bench {
			if slot.ViewKey == view {
				add(slot)
			}
		}
	}
	for _, slot := range bench {
		if !entity.IsDesignSilhouetteView(slot.ViewKey) {
			add(slot)
		}
	}
}

// designExpectedPlatesFromPb переводит оптимистичную блокировку по верстаку.
func designExpectedPlatesFromPb(in []*pb_admin.DesignExpectedPlate) ([]entity.DesignExpectedPlate, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]entity.DesignExpectedPlate, 0, len(in))
	for _, e := range in {
		ref, err := designSlotRefFromPb(e.GetSlot())
		if err != nil {
			return nil, err
		}
		out = append(out, entity.DesignExpectedPlate{Slot: ref, SlotRev: int(e.GetSlotRev())})
	}
	return out, nil
}

// designMintError — ОДНА таблица на два происхождения отказа.
//
// Внутри транзакции минта живут ДВЕ таксономии: отказы полосы (bench_moved, unrepinned_callouts…)
// и отказы документной записи (конфликт версии блокировки, замороженный релиз, запертое
// назначение) — ровно те же, что видит обычный сейв, потому что пишет их тот же код.
//
// Конфликт версии блокировки — ЕДИНСТВЕННЫЙ, что переводится не как у сейва: код тот же (Aborted),
// но контракт минта обещает МАШИННУЮ причину `lock_version_mismatch`, по которой клиент откатывает
// диалог. Прозы «modified concurrently» ему для этого мало.
func (s *Server) designMintError(ctx context.Context, err error) error {
	if errors.Is(err, entity.ErrTechCardConflict) {
		st := status.New(codes.Aborted, "the card moved under the mint; reload and retry")
		withDetails, derr := st.WithDetails(&errdetails.ErrorInfo{
			Reason: "lock_version_mismatch", Domain: designErrorDomain,
			Metadata: map[string]string{"reason": "lock_version_mismatch"},
		})
		if derr != nil {
			return st.Err()
		}
		return withDetails.Err()
	}
	// Всё остальное из документной половины — дословно теми же словами, что у сейва: два разных
	// ответа на одну и ту же причину заставили бы клиента держать две ветки восстановления.
	if s.isTechCardWriteRefusal(err) {
		return s.techCardWriteError(ctx, err)
	}
	var refusal *entity.DesignMintRefusal
	md := map[string]string(nil)
	if errors.As(err, &refusal) {
		md = refusal.Metadata
	}
	return designError(ctx, "failed to mint the design sheet version", err, md)
}

// isTechCardWriteRefusal — отказы ДОКУМЕНТНОЙ записи, у которых уже есть перевод у сейва.
//
// СПИСОК ОБЯЗАН БЫТЬ ПОЛНЫМ, иначе минт отвечает Internal там, где сейв отвечает словами: 1062 на
// номере стиля, 1452 на несуществующем размере, «карточки нет» — всё это приезжает из ТОГО ЖЕ кода
// и уже имеет внятный перевод. Отдать их как «внутренняя ошибка» значило бы, что один и тот же
// payload объясняет себя на сейве и молчит на минте.
func (s *Server) isTechCardWriteRefusal(err error) bool {
	var ve *entity.ValidationError
	return errors.As(err, &ve) ||
		errors.Is(err, sql.ErrNoRows) ||
		errors.Is(err, entity.ErrTechCardReleased) ||
		errors.Is(err, entity.ErrTechCardPurposeLocked) ||
		s.repo.IsErrUniqueViolation(err) ||
		s.repo.IsErrForeignKeyViolation(err)
}
