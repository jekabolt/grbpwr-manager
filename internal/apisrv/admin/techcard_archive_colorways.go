package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф6.2 — «СОЗДАТЬ КОЛОРВЕИ ИЗ АРХИВА»: ВТОРОЙ, ЯВНЫЙ ШАГ ИМПОРТА.
//
// Импорт колорвеев НЕ СОЗДАЁТ — колорвей есть продукт, а решение завести продукт нельзя делегировать
// файлу. Поэтому colorways.json приезжает справкой, коммит пишет по строке
// `colorways_not_applied` на каждый цвет, и только человек, прочитавший отчёт, нажимает кнопку. Этот
// файл — то, что происходит по нажатию.
//
// ЧЕТЫРЕ ВЕЩИ, О КОТОРЫХ ЗДЕСЬ ДУМАЛИ, И КАЖДАЯ СТОИЛА БЫ ДЕФЕКТА:
//
//   - ИДЕМПОТЕНТНОСТЬ ЖИВЁТ В ЦВЕТЕ, А НЕ В НАЖАТИИ. Уже стоящий на карточке color_code —
//     это строка «exists» и пропуск, и такой же ответ даёт ПОЙМАННАЯ коллизия UNIQUE(style_id,
//     color_code): между нашей проверкой и вставкой помещается чужой клик, и превращать его в 500
//     значило бы наказывать человека за то, что он нажал дважды.
//   - ВЕРСИЯ КАРТОЧКИ ДВИЖЕТСЯ ПОД НОГАМИ. Каждая запись рецепта бампает tech_card.lock_version
//     (colorway_recipe.go), поэтому оптимистичный токен читается ЗАНОВО перед каждым колорвеем, а
//     не берётся один раз на весь цикл. Иначе второй цвет партии всегда падал бы в конфликт.
//   - ССЫЛКИ АРХИВА ФИЛЬТРУЮТСЯ ЗДЕСЬ, А НЕ В СТОРЕ. Рецепт адресует строки BOM и детали по
//     стабильным line_key, которые импорт вёз вербатимом — но строка, назвавшая ключ, которого на
//     карточке нет, роняет ВЕСЬ рецепт полевым нарушением. Одна такая строка не должна стоить
//     колорвея, поэтому она отсеивается и попадает в отчёт.
//   - ДЕНЬГИ НЕ ЕДУТ НИ ПО ОДНОЙ ДОРОГЕ. Колорвей заводится драфтом без cost_price, без цен и без
//     SKU, а сам payload проходит пояс: любое денежное ИМЯ внутри colorways.json — признак архива
//     чужой (до-версионной) сборки, и такой архив не применяется вовсе.
//
// ЧЕГО ЗДЕСЬ НЕТ И БЫТЬ НЕ ДОЛЖНО: публикации колорвея, минта SKU, назначения цен и правки самой
// тех-карты. Всё, что удостоверяет архив, — это драфт, его рецепт и раскладка деталей по тканям.
// ─────────────────────────────────────────────────────────────────────────────

// ApplyTechCardImportColorways создаёт драфт-колорвеи импортированной карточки из сохранённого тела
// colorways.json и возвращает ОБНОВЛЁННЫЙ отчёт импорта. Смысл жеста и его классификация — у RPC в
// admin.proto.
func (s *Server) ApplyTechCardImportColorways(ctx context.Context,
	req *pb_admin.ApplyTechCardImportColorwaysRequest) (*pb_admin.ApplyTechCardImportColorwaysResponse, error) {
	techCardID := int(req.GetTechCardId())
	if techCardID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}

	rec, err := s.repo.TechCards().GetTechCardImportReport(ctx, techCardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errTechCardImportReportAbsent
		}
		slog.Default().ErrorContext(ctx, "apply import colourways: can't read the import row",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read the import report")
	}
	if rec.Status != entity.TechCardImportStatusCommitted {
		return nil, status.Errorf(codes.FailedPrecondition,
			"this card's import is %q, not committed — there is nothing to build colourways onto", rec.Status)
	}
	if len(rec.Report) == 0 {
		// Same sentence GetTechCardImportReport gives, and for the same reason: the answer this
		// call owes is a REPORT, and a row that carries none cannot produce one. Refused BEFORE a
		// single colourway is created — the alternative is products on the card and no record of
		// where they came from.
		slog.Default().WarnContext(ctx, "apply import colourways: the import row carries no report",
			slog.Int("tech_card_id", techCardID), slog.String("import_id", rec.ImportID))
		return nil, errTechCardImportReportAbsent
	}

	payloads, err := tcacPayload(rec.ColorwaysPayload)
	if err != nil {
		return nil, err
	}
	// Parsed BEFORE anything is written, exactly like the commit path parses the report outside its
	// transaction: a report that does not read as one has to be discovered while nothing has
	// happened yet.
	stored, err := techcardarchive.ParseReport(rec.Report)
	if err != nil {
		slog.Default().ErrorContext(ctx, "apply import colourways: the stored report does not read as one",
			slog.Int("tech_card_id", techCardID), slog.String("import_id", rec.ImportID),
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "the stored import report cannot be read")
	}

	run, err := s.tcacPrepare(ctx, techCardID, rec.ObjectKey)
	if err != nil {
		return nil, err
	}
	for i := range payloads {
		if err := run.applyOne(ctx, payloads[i]); err != nil {
			return nil, err
		}
	}

	updated, err := stored.ApplyColorways(run.holes, run.tally)
	if err != nil {
		slog.Default().ErrorContext(ctx, "apply import colourways: can't rebuild the report",
			slog.Int("tech_card_id", techCardID), slog.String("import_id", rec.ImportID),
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "the colourways were created and the report could not be rewritten")
	}
	if err := s.repo.TechCards().StampTechCardImportReport(ctx, rec.ImportID, updated); err != nil {
		slog.Default().ErrorContext(ctx, "apply import colourways: can't stamp the report",
			slog.Int("tech_card_id", techCardID), slog.String("import_id", rec.ImportID),
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "the colourways were created and the report could not be stored")
	}

	// Re-parsed rather than kept as a message: ApplyColorways answers in the bytes that were
	// STORED, and returning anything else would let the screen and the column disagree about what
	// just happened.
	fresh, err := techcardarchive.ParseReport(updated)
	if err != nil { // unreachable: these are the bytes MarshalReport just produced
		return nil, status.Error(codes.Internal, "the rewritten import report cannot be read back")
	}

	slog.Default().InfoContext(ctx, "colourways created from a tech card archive",
		slog.Int("tech_card_id", techCardID), slog.String("import_id", rec.ImportID),
		slog.Int("created", len(run.created)), slog.Int("degraded", run.tally.Degraded),
		slog.Int("skipped", run.tally.Skipped))

	return &pb_admin.ApplyTechCardImportColorwaysResponse{
		CreatedColorwayIds: run.created,
		Report:             fresh.Message(),
	}, nil
}

// ────────────────────────────── the payload ──────────────────────────────

// tcacPayload turns the stored colorways.json into the list this action builds from, refusing
// everything that must never be built from.
//
// THE MONEY BELT RUNS ON THE RAW BYTES AND NOT ON THE PARSED STRUCT, which is the only place it can
// run: ColorwayPayload has no member for a price, so encoding/json drops one without a word and a
// belt looking at the struct would be looking at a payload already cleaned by its own blindness.
// The names come from techcardarchive.MoneyFieldNamesArchive — the ONE list the export redacts by —
// so a money field added to the contract is caught here without anybody remembering to.
func tcacPayload(raw []byte) ([]techcardarchive.ColorwayPayload, error) {
	if len(raw) == 0 {
		return nil, status.Error(codes.FailedPrecondition,
			"the archive this card came from carried no colourways, so there is nothing to create")
	}
	if field, found := tcacMoneyInPayload(raw); found {
		return nil, status.Errorf(codes.FailedPrecondition,
			"this card's archive carries costing data in its colourways (%s), which an archive may never "+
				"bring; nothing was created. The file was written by a build that predates the money "+
				"policy — re-export the source card and import that archive", field)
	}

	var payloads []techcardarchive.ColorwayPayload
	if err := json.Unmarshal(raw, &payloads); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition,
			"this card's stored colourway payload does not read as %s: %v", techcardarchive.FileColorways, err)
	}
	if len(payloads) == 0 {
		return nil, status.Error(codes.FailedPrecondition,
			"the archive this card came from carried no colourways, so there is nothing to create")
	}
	return payloads, nil
}

// tcacMoneyInPayload walks arbitrary JSON looking for a money-bearing KEY, at any depth, and names
// the first one it finds. Depth is bounded by the parser that produced the value, which is
// encoding/json on a payload the upload route already accepted.
func tcacMoneyInPayload(raw []byte) (string, bool) {
	var any any
	if err := json.Unmarshal(raw, &any); err != nil {
		// Not this function's refusal to make: the parse below says it better, naming the error.
		return "", false
	}
	return tcacMoneyInValue(any, "")
}

func tcacMoneyInValue(v any, path string) (string, bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			at := k
			if path != "" {
				at = path + "." + k
			}
			if techcardarchive.MoneyFieldNamesArchive[k] {
				return at, true
			}
			if found, ok := tcacMoneyInValue(sub, at); ok {
				return found, true
			}
		}
	case []any:
		for i, sub := range t {
			if found, ok := tcacMoneyInValue(sub, fmt.Sprintf("%s[%d]", path, i)); ok {
				return found, true
			}
		}
	}
	return "", false
}

// ────────────────────────────── the run ──────────────────────────────

// tcacRun is one press of the button: everything read once up front, plus what the press produced.
type tcacRun struct {
	s          *Server
	techCardID int

	// colorwayIDByCode is the card's LIVE colour occupancy, upper-cased. It grows as colours are
	// created, so a payload naming the same colour twice creates it once.
	colorwayIDByCode map[string]int
	bomKeys          map[string]bool
	pieceKeys        map[string]bool
	// sizeIDByName is this base's size dictionary; cardSizes is the imported card's own range. Both
	// are needed and neither substitutes for the other — see the two reason codes they produce.
	sizeIDByName map[string]int
	cardSizes    map[int]bool

	// catalog is the live material catalogue, read once. passportByRef is materials/index.json IF
	// the uploaded archive is still in the bucket; passportsAvailable says whether it is, and is
	// what tells «this catalogue has no such article» apart from «nothing described the article».
	catalog            []entity.Material
	passportByRef      map[int64]techcardarchive.MaterialPassport
	passportsAvailable bool

	holes   []techcardarchive.ImportHole
	tally   techcardarchive.EntityTally
	created []int32
}

// tcacPrepare reads everything the whole press needs, once. Every read here is against the TARGET
// base and none of it depends on the payload: a per-colourway catalogue read would be the N+1 the
// import resolver already refuses to be.
func (s *Server) tcacPrepare(ctx context.Context, techCardID int, objectKey string) (*tcacRun, error) {
	card, err := s.repo.TechCards().GetTechCardByIdConsistent(ctx, techCardID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "apply import colourways: can't read the card",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read the tech card")
	}
	if card == nil {
		return nil, status.Error(codes.NotFound, "tech card not found")
	}

	run := &tcacRun{
		s: s, techCardID: techCardID,
		colorwayIDByCode: make(map[string]int, len(card.Colorways)),
		bomKeys:          make(map[string]bool, len(card.BomItems)),
		pieceKeys:        make(map[string]bool, len(card.Pieces)),
		cardSizes:        make(map[int]bool, len(card.SizeIds)),
		tally:            techcardarchive.EntityTally{},
	}
	for i := range card.Colorways {
		run.colorwayIDByCode[tcacColourKey(card.Colorways[i].ColorCode)] = card.Colorways[i].Id
	}
	for i := range card.BomItems {
		if k := card.BomItems[i].LineKey; k != "" {
			run.bomKeys[k] = true
		}
	}
	for i := range card.Pieces {
		if k := card.Pieces[i].LineKey; k != "" {
			run.pieceKeys[k] = true
		}
	}
	for _, id := range card.SizeIds {
		run.cardSizes[id] = true
	}

	di, err := s.repo.Cache().GetDictionaryInfo(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't read the dictionary")
	}
	run.sizeIDByName = make(map[string]int, len(di.Sizes))
	for _, sz := range di.Sizes {
		run.sizeIDByName[tcimpKey(sz.Name)] = sz.Id
	}

	mats, err := s.repo.TechCards().ListMaterials(ctx, "", true)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't read the material catalogue")
	}
	run.catalog = make([]entity.Material, 0, len(mats))
	for i := range mats {
		// entity.Material and not MaterialWithPrice: the matcher must not be able to reach a price.
		run.catalog = append(run.catalog, mats[i].Material)
	}
	run.passportByRef, run.passportsAvailable = s.tcacPassports(ctx, objectKey)
	return run, nil
}

// tcacPassports fetches materials/index.json out of the uploaded archive — BEST EFFORT, and the
// «best effort» is the design rather than laziness.
//
// A recipe pin travels as the SOURCE's material_id and is identified by its passport (§5.4). The
// passports live in the archive; the colourway payload that outlives the archive does not carry
// them, and the bucket object is deleted once the retention window closes. So a pin resolves
// EXACTLY while the uploaded file is still there — which is the case the button is actually pressed
// in — and degrades honestly (colorway_pin_lost) once it is not.
//
// Nothing here can fail the press: an unreachable object, an unreadable ZIP or a missing index all
// mean the same thing to the caller — nothing to match a pin against.
func (s *Server) tcacPassports(ctx context.Context, objectKey string) (map[int64]techcardarchive.MaterialPassport, bool) {
	if objectKey == "" {
		return nil, false
	}
	ra, size, err := s.bucket.GetImportObjectReaderAt(ctx, objectKey)
	if err != nil {
		slog.Default().InfoContext(ctx, "apply import colourways: the uploaded archive is gone, pins will not resolve",
			slog.String("object_key", objectKey), slog.String("err", err.Error()))
		return nil, false
	}
	defer ra.Close()

	arch, err := techcardarchive.OpenArchive(ra, size)
	if err != nil {
		slog.Default().WarnContext(ctx, "apply import colourways: the uploaded archive no longer opens",
			slog.String("object_key", objectKey), slog.String("err", err.Error()))
		return nil, false
	}
	if !arch.Has(techcardarchive.FileMaterialsIndex) {
		// A legal archive: a card whose lines name no catalogue article carries no passports. There
		// is then nothing to resolve and nothing to report — every pin in such a payload is 0.
		return map[int64]techcardarchive.MaterialPassport{}, true
	}
	raw, err := arch.ReadFile(techcardarchive.FileMaterialsIndex)
	if err != nil {
		slog.Default().WarnContext(ctx, "apply import colourways: can't read the archive's material index",
			slog.String("object_key", objectKey), slog.String("err", err.Error()))
		return nil, false
	}
	var passports []techcardarchive.MaterialPassport
	if err := json.Unmarshal(raw, &passports); err != nil {
		slog.Default().WarnContext(ctx, "apply import colourways: the archive's material index does not parse",
			slog.String("object_key", objectKey), slog.String("err", err.Error()))
		return nil, false
	}
	byRef := make(map[int64]techcardarchive.MaterialPassport, len(passports))
	for _, p := range passports {
		byRef[p.Ref] = p
	}
	return byRef, true
}

// applyOne builds ONE colourway. A returned error aborts the whole press; everything else that goes
// wrong becomes a report line and the next colour is attempted.
//
// The split between the two is «is this about the card or about this row»: a released card, an
// auxiliary style and a card that vanished refuse every remaining colour identically, and answering
// with a report full of the same line N times would bury the one sentence the operator needs.
func (r *tcacRun) applyOne(ctx context.Context, p techcardarchive.ColorwayPayload) error {
	code := tcacColourKey(p.ColorCode)
	ref := fmt.Sprintf("color_code=%s", p.ColorCode)
	if code == "" {
		r.skip(ref, techcardarchive.ReasonArchiveRowInvalid,
			"the archive's colourway row names no colour, so there is nothing to create")
		return nil
	}
	if id, taken := r.colorwayIDByCode[code]; taken {
		r.exists(ref, id, len(p.Recipe))
		return nil
	}

	// Драфт и ничего кроме. Ни cost_price, ни prices, ни медиа, ни тегов, ни лаб-дипа: всё это
	// либо деньги, либо ссылки на строки ЧУЖОЙ базы. Единственное, что архив удостоверяет о самом
	// колорвее, — его код цвета; base_sku из payload сознательно НЕ едет, SKU минтится на
	// публикации и только ею.
	colorwayID, err := r.s.createColorway(ctx, colorwayCreateInput{
		StyleID:       r.techCardID,
		Merchandising: &pb_common.ColorwayMerchandisingInsert{ColorCode: code},
	})
	switch {
	case err == nil:
	case errors.Is(err, entity.ErrColorwayColorExists) || tcacIsDuplicateColour(err):
		// ПОЙМАННАЯ КОЛЛИЗИЯ, а не 500. Проверка занятости цвета и вставка стоят в разных
		// транзакциях (стор проверяет внутри своей, мы — по прочитанной карточке), и в зазор
		// помещается второй клик по той же кнопке. Ответ обязан быть тем же, что и у заведомо
		// занятого цвета, иначе идемпотентность держалась бы на удаче гонки.
		r.exists(ref, 0, len(p.Recipe))
		return nil
	case errors.Is(err, entity.ErrTechCardReleased), errors.Is(err, entity.ErrColorwayNotSellable),
		errors.Is(err, sql.ErrNoRows):
		return createColorwayStatus(ctx, err)
	default:
		r.skip(ref, techcardarchive.ReasonColorwayNotCreated, tcacRefusal(err))
		return nil
	}

	r.colorwayIDByCode[code] = colorwayID
	r.created = append(r.created, int32(colorwayID))

	degraded := r.recipe(ctx, ref, colorwayID, p.Recipe)
	lost, err := r.pieceMaterials(ctx, ref, colorwayID, p.PieceMaterials)
	if err != nil {
		return err
	}
	if degraded || lost {
		r.tally.Degraded++
		return nil
	}
	r.tally.Imported++
	return nil
}

// recipe writes one colourway's material recipe and reports what it had to leave out. It returns
// whether anything was lost.
func (r *tcacRun) recipe(ctx context.Context, ref string, colorwayID int, lines []techcardarchive.RecipeLine) bool {
	usages, degraded := r.buildUsages(ref, lines)
	if len(usages) == 0 {
		return degraded
	}
	if err := r.s.tcacWriteRecipe(ctx, r.techCardID, colorwayID, usages); err != nil {
		// The colourway stands and its recipe does not. Reported rather than fatal: the press is
		// over several colours and the one that failed is nameable, while aborting here would leave
		// the report unwritten and the created drafts unexplained.
		r.hole(techcardarchive.EntityColorway, ref, techcardarchive.StatusDegraded,
			techcardarchive.ReasonArchiveRowInvalid,
			fmt.Sprintf("the colourway was created and its recipe was refused (%v); re-enter the recipe "+
				"on the colourway, or delete the draft and press the button again", err))
		return true
	}
	return degraded
}

// buildUsages turns archive recipe rows into the store's usages, dropping every row that cannot
// stand on THIS card and reporting each one. It returns the usages and whether anything was lost.
//
// FILTERED HERE ON PURPOSE. UpdateColorwayRecipe answers an unknown bom_line_key / piece_line_key
// with a field violation that aborts the WHOLE recipe — correct for a panel save, wrong for a
// restore, where one stale row would cost the other twenty.
func (r *tcacRun) buildUsages(ref string, lines []techcardarchive.RecipeLine) ([]entity.TechCardColorwayUsage, bool) {
	usages := make([]entity.TechCardColorwayUsage, 0, len(lines))
	degraded := false
	for i := range lines {
		l := &lines[i]
		rowRef := tcacRowRef(ref, l)
		if l.BomLineKey != "" && !r.bomKeys[l.BomLineKey] {
			r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusSkipped,
				techcardarchive.ReasonArchiveRowInvalid,
				fmt.Sprintf("the recipe row names BOM line %q and this card has none; the row was dropped "+
					"and the rest of the recipe landed", l.BomLineKey))
			degraded = true
			continue
		}
		if l.PieceLineKey != "" && !r.pieceKeys[l.PieceLineKey] {
			r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusSkipped,
				techcardarchive.ReasonArchiveRowInvalid,
				fmt.Sprintf("the recipe row names cut-piece %q and this card has none; the row was dropped "+
					"and the rest of the recipe landed", l.PieceLineKey))
			degraded = true
			continue
		}

		consumption, ok := r.decimalOf(rowRef, "consumption", l.Consumption)
		if !ok {
			degraded = true
			continue
		}
		quantity, ok := r.decimalOf(rowRef, "quantity", l.Quantity)
		if !ok {
			degraded = true
			continue
		}
		selvedge, ok := r.decimalOf(rowRef, "waste_selvedge_pct", l.WasteSelvedgePct)
		if !ok {
			degraded = true
			continue
		}
		cut, ok := r.decimalOf(rowRef, "waste_cut_pct", l.WasteCutPct)
		if !ok {
			degraded = true
			continue
		}

		source := strings.ToLower(strings.TrimSpace(l.ConsumptionSource))
		if source != "" && !entity.ValidConsumptionSources[source] {
			r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusDegraded,
				techcardarchive.ReasonArchiveRowInvalid,
				fmt.Sprintf("the recipe row states consumption_source %q, which is not a provenance this "+
					"server knows; the norm landed as entered by hand", l.ConsumptionSource))
			source = ""
			degraded = true
		}
		if source == entity.ConsumptionSourceMarker {
			// norm_marker_id НЕ ЕДЕТ (§5.3): штамп указывает на раскладку чужой базы. Норма
			// остаётся, провенанс остаётся (он решает, гроссится ли процент), а вот аудит «из какой
			// раскладки» пересшить нечем — и это ровно то, что говорит norm_marker_lost.
			r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusDegraded,
				techcardarchive.ReasonNormMarkerLost,
				"the norm came from a раскладка of the source instance; the figure landed, the stamp "+
					"pointing at the layout it was measured on did not")
			degraded = true
		}

		u := entity.TechCardColorwayUsage{
			BomLineKey:        l.BomLineKey,
			PieceLineKey:      l.PieceLineKey,
			Placement:         tcimpNullString(l.Placement),
			Color:             tcimpNullString(l.Color),
			Pantone:           tcimpNullString(l.Pantone),
			Consumption:       consumption,
			Quantity:          quantity,
			WasteSelvedgePct:  selvedge,
			WasteCutPct:       cut,
			ConsumptionSource: sql.NullString{String: source, Valid: true},
			// Обе присутствия ЯВНЫЕ и обе true: колорвей только что заведён, сохранять нечего, а
			// false означал бы «клиент промолчал, сохрани прежнее» — в применении архива это
			// приглашение унаследовать чужой пин или чужой штамп.
			MaterialIdSet:   true,
			NormMarkerIdSet: true,
		}
		if lost := r.pin(rowRef, l, &u); lost {
			degraded = true
		}
		if lost := r.sizeConsumptions(rowRef, l, &u); lost {
			degraded = true
		}
		usages = append(usages, u)
	}
	return usages, degraded
}

// pin resolves a recipe row's material pin through the archive's passports and this base's
// catalogue — the SAME ladder the import resolver runs for a BOM line's default article, because
// «is this the same article» must have one answer (techcardarchive.MatchMaterial).
//
// Returns whether the pin was lost. A lost pin is never fatal: the row keeps its norm and its
// placement and simply inherits the BOM slot's own article.
func (r *tcacRun) pin(rowRef string, l *techcardarchive.RecipeLine, u *entity.TechCardColorwayUsage) bool {
	if l.MaterialRef <= 0 {
		return false // the row never pinned anything: it takes the slot default by design
	}
	if !r.passportsAvailable {
		r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusDegraded,
			techcardarchive.ReasonColorwayPinLost,
			"the uploaded archive is no longer in storage, so the passport that said WHICH article this "+
				"row pins is gone; the row landed with the BOM line's own article")
		return true
	}
	p, ok := r.passportByRef[l.MaterialRef]
	if !ok {
		r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusDegraded,
			techcardarchive.ReasonColorwayPinLost,
			fmt.Sprintf("the archive carries no passport for the pinned material_id=%d, so there was nothing "+
				"to match; the row landed with the BOM line's own article", l.MaterialRef))
		return true
	}
	id, verdict := techcardarchive.MatchMaterial(p, r.catalog)
	if verdict == techcardarchive.MaterialMatched && id > 0 {
		u.MaterialId = sql.NullInt64{Int64: id, Valid: true}
		return false
	}
	r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusDegraded,
		techcardarchive.ReasonForMaterialVerdict(verdict),
		fmt.Sprintf("the pinned article %q (%s / %s) did not resolve to one live article here; the row "+
			"landed with its norm and placement and with no article pinned", p.Code, p.Supplier, p.SupplierRef))
	return true
}

// sizeConsumptions maps the per-size norms, which travel BY SIZE NAME, onto this base's sizes.
//
// TWO checks and two different report codes, because they send the operator to two different
// places: a name this base's dictionary does not carry is size_unknown (add the size), and a size
// the dictionary HAS but the imported card does not make is size_not_in_card_range (widen the
// card). The second one also has to happen here rather than in the store, which answers it with a
// field violation that would abort the whole recipe.
func (r *tcacRun) sizeConsumptions(rowRef string, l *techcardarchive.RecipeLine, u *entity.TechCardColorwayUsage) bool {
	if len(l.SizeConsumptions) == 0 {
		return false
	}
	lost := false
	// Sorted, so a report of a payload read twice reads the same twice: Go map order is not one.
	for _, name := range tcacSortedKeys(l.SizeConsumptions) {
		raw := l.SizeConsumptions[name]
		sizeID, known := r.sizeIDByName[tcimpKey(name)]
		if !known {
			r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusSkipped,
				techcardarchive.ReasonSizeUnknown,
				fmt.Sprintf("the per-size norm for size %q was dropped: this base's size dictionary has no "+
					"such size", name))
			lost = true
			continue
		}
		if !r.cardSizes[sizeID] {
			r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusSkipped,
				techcardarchive.ReasonSizeNotInCardRange,
				fmt.Sprintf("the per-size norm for size %q was dropped: the imported card's own size range "+
					"does not include it", name))
			lost = true
			continue
		}
		value, err := decimal.NewFromString(strings.TrimSpace(raw))
		if err != nil {
			r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusSkipped,
				techcardarchive.ReasonArchiveRowInvalid,
				fmt.Sprintf("the per-size norm for size %q is %q, which is not a number; it was dropped", name, raw))
			lost = true
			continue
		}
		u.SizeConsumptions = append(u.SizeConsumptions, entity.TechCardBomSizeConsumption{
			SizeId: sizeID, Consumption: value,
		})
	}
	return lost
}

// pieceMaterials writes the colourway's piece→cloth mapping, dropping the rows this card cannot
// hold. Returns whether anything was lost; an error is fatal to the press.
func (r *tcacRun) pieceMaterials(ctx context.Context, ref string, colorwayID int,
	lines []techcardarchive.PieceMaterialLine) (bool, error) {
	if len(lines) == 0 {
		return false, nil
	}
	rows := make([]entity.TechCardArchivePieceMaterial, 0, len(lines))
	seen := make(map[string]bool, len(lines))
	lost := false
	for i := range lines {
		l := &lines[i]
		rowRef := fmt.Sprintf("%s piece_line_key=%s", ref, l.PieceLineKey)
		switch {
		case l.PieceLineKey == "" || !r.pieceKeys[l.PieceLineKey]:
			r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusSkipped,
				techcardarchive.ReasonArchiveRowInvalid,
				"the piece→cloth row names a cut-piece this card does not have; it was dropped")
			lost = true
			continue
		case seen[l.PieceLineKey]:
			// The table holds ONE row per (piece, colourway) by UNIQUE. A payload with two is an
			// archive contradicting itself, and letting the second one hit the constraint would
			// cost the whole mapping instead of the one row.
			r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusSkipped,
				techcardarchive.ReasonArchiveRowInvalid,
				"the archive names this cut-piece twice for one colour, and a piece is cut from one cloth "+
					"per colour; the repeat was dropped")
			lost = true
			continue
		case l.BomLineKey != "" && !r.bomKeys[l.BomLineKey],
			l.FusingBomLineKey != "" && !r.bomKeys[l.FusingBomLineKey]:
			r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusSkipped,
				techcardarchive.ReasonArchiveRowInvalid,
				"the piece→cloth row names a BOM line this card does not have; it was dropped")
			lost = true
			continue
		}
		seen[l.PieceLineKey] = true
		rows = append(rows, entity.TechCardArchivePieceMaterial{
			PieceLineKey:     l.PieceLineKey,
			BomLineKey:       l.BomLineKey,
			FusingBomLineKey: l.FusingBomLineKey,
			Note:             l.Note,
		})
	}
	if len(rows) == 0 {
		return lost, nil
	}
	if err := r.s.repo.TechCards().ApplyImportedColorwayPieceMaterials(ctx, r.techCardID, colorwayID, rows); err != nil {
		slog.Default().ErrorContext(ctx, "apply import colourways: can't write the piece→cloth mapping",
			slog.Int("tech_card_id", r.techCardID), slog.Int("colorway_id", colorwayID),
			slog.String("err", err.Error()))
		return lost, status.Error(codes.Internal, "can't write the colourway's piece mapping")
	}
	return lost, nil
}

// ────────────────────────────── the optimistic token ──────────────────────────────

// tcacRecipeAttempts is how many times ONE colourway's recipe is written before the press gives up.
// Three, not one: EVERY recipe write bumps the card's shared lock_version, so a press over several
// colours races with itself unless the token is re-read — and the retry is what makes a race with
// SOMEBODY ELSE'S save cost a re-read rather than a colourway with an empty recipe.
const tcacRecipeAttempts = 3

// tcacWriteRecipe writes one colourway's recipe under a FRESHLY READ optimistic token.
//
// The token is read immediately before every attempt and never carried over from the previous
// colour: colorway_recipe.go bumps tech_card.lock_version at the end of each write, so a version
// captured once at the top of the press is stale from the second colour onward — and the failure
// would look like a conflict with another user rather than with ourselves.
func (s *Server) tcacWriteRecipe(ctx context.Context, techCardID, colorwayID int,
	usages []entity.TechCardColorwayUsage) error {
	var err error
	for attempt := 0; attempt < tcacRecipeAttempts; attempt++ {
		var version int
		version, err = s.repo.TechCards().GetTechCardLockVersion(ctx, techCardID)
		if err != nil {
			return err
		}
		if _, err = s.repo.TechCards().UpdateColorwayRecipe(ctx, colorwayID, version, usages); err == nil {
			return nil
		}
		if !errors.Is(err, entity.ErrTechCardConflict) {
			return err
		}
	}
	return err
}

// ────────────────────────────── report plumbing ──────────────────────────────

func (r *tcacRun) hole(entityName, ref, status string, reason techcardarchive.Reason, detail string) {
	r.holes = append(r.holes, techcardarchive.ImportHole{
		Entity: entityName, Ref: ref, Status: status, Reason: reason, Detail: detail,
	})
}

// skip records a colour that did not land at all.
func (r *tcacRun) skip(ref string, reason techcardarchive.Reason, detail string) {
	r.hole(techcardarchive.EntityColorway, ref, techcardarchive.StatusSkipped, reason, detail)
	r.tally.Skipped++
}

// exists records a colour the card already carried. colorwayID is 0 when the collision was caught
// on the write rather than read off the card, which changes nothing the operator can act on.
func (r *tcacRun) exists(ref string, colorwayID, recipeRows int) {
	detail := fmt.Sprintf("this card already has a colourway of this colour, so it was not created and "+
		"its recipe was left alone; the archive's %d recipe rows were not applied over it", recipeRows)
	if colorwayID > 0 {
		detail = fmt.Sprintf("%s (colourway %d)", detail, colorwayID)
	}
	r.hole(techcardarchive.EntityColorway, ref, techcardarchive.StatusDegraded,
		techcardarchive.ReasonColorwayExists, detail)
	r.tally.Degraded++
}

// decimalOf parses one optional decimal off a recipe row. An empty string is «not stated» and is
// SQL NULL; anything that is not a number costs the row, because a norm nobody can read is worse on
// a card than a norm that is visibly missing.
func (r *tcacRun) decimalOf(rowRef, field, raw string) (decimal.NullDecimal, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return decimal.NullDecimal{}, true
	}
	value, err := decimal.NewFromString(trimmed)
	if err != nil {
		r.hole(techcardarchive.EntityColorway, rowRef, techcardarchive.StatusSkipped,
			techcardarchive.ReasonArchiveRowInvalid,
			fmt.Sprintf("the recipe row states %s = %q, which is not a number; the row was dropped", field, raw))
		return decimal.NullDecimal{}, false
	}
	return decimal.NullDecimal{Decimal: value, Valid: true}, true
}

// ────────────────────────────── small shared shapes ──────────────────────────────

// tcacColourKey normalises a colour code the way the colour dictionary does: trimmed and upper
// case. It is what makes «blk» in a payload and «BLK» on the card one colour rather than two.
func tcacColourKey(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// tcacRowRef names one recipe row inside its colour, so a report line points at something an
// operator can find. Both keys, because a slot legitimately appears twice: once for the garment
// (the norm) and once per cut-piece (the material assignment).
func tcacRowRef(ref string, l *techcardarchive.RecipeLine) string {
	out := ref
	if l.BomLineKey != "" {
		out += " bom_line_key=" + l.BomLineKey
	}
	if l.PieceLineKey != "" {
		out += " piece_line_key=" + l.PieceLineKey
	}
	return out
}

// tcacSortedKeys returns a map's keys in a stable order.
func tcacSortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// tcacIsDuplicateColour reports the raw UNIQUE(style_id, color_code) violation — the one the store's
// own pre-check did not get to first. The store answers a duplicate it SAW with
// entity.ErrColorwayColorExists; this catches the one that appeared between that check and the
// INSERT, which is the same news and must not become a 500.
func tcacIsDuplicateColour(err error) bool {
	name, ok := tcciDuplicateKeyName(err)
	if !ok {
		return false
	}
	// The index is uniq_product_style_color; matched loosely because the name is schema history and
	// this predicate must not go quiet if it is ever rebuilt under a longer name.
	return strings.Contains(strings.ToLower(name), "color")
}

// tcacRefusal is the store's own sentence about why a colourway could not be created, bounded so
// one refusal cannot fill the report. It is quoted rather than rewritten: the reasons are open-ended
// (a colour outside the dictionary, a country FK, a media id) and a paraphrase would be a second,
// vaguer dictionary.
func tcacRefusal(err error) string {
	return techcardarchive.ClipDetail(fmt.Sprintf("the colourway could not be created here: %v", err), 512)
}
