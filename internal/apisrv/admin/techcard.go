package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/accounting"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/stylenumber"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// techCardFKMsg is returned when a tech card references a missing category, base
// model, base sample size, size, product or media row.
const techCardFKMsg = "tech card references a non-existent category, model, size, product, media or fitting"

// styleNumberTaken is the field-tagged rejection for a global-UNIQUE(style_number) collision (Q1).
func styleNumberTaken() error {
	return apierr.Invalid(entity.NewFieldViolation("style_number", "already_exists", "",
		"this style number is already used by another style; choose a different one or accept a fresh generated proposal"))
}

// equipmentProfileKeyIndex is the name 0306 gives UNIQUE(tech_card_id, profile_key). It is written
// here because a 1062 is the ONLY way the API layer can learn which key was tripped: the driver
// hands back one error number for every unique index in the schema, and the index name inside its
// message is the only discriminator there is.
const equipmentProfileKeyIndex = "uq_equipment_profile_key"

// techCardUniqueViolation names the unique key a tech-card save actually tripped.
//
// A card write touches TWO of them, and until 0306 there was only one: for years every 1062 out of
// this path was UNIQUE(style_number), so the handlers translated the number itself into «this style
// number is taken». With an equipment park on the card that translation became a lie — a duplicate
// PROFILE key would tell the operator to change the article of a style whose article is fine, about
// a field the failing form does not even contain.
//
// The payload path is normally caught earlier and in a full sentence (parseTechCardEquipmentDefaults
// dedupes both lists against one key space), so what reaches here is the entity-level writers that
// never see the wire: the seeder, the clone path, tests. That is precisely why the message has to
// stand on its own — nothing upstream is going to explain it.
func techCardUniqueViolation(err error) error {
	if err != nil && strings.Contains(err.Error(), equipmentProfileKeyIndex) {
		return apierr.Invalid(entity.NewFieldViolation("construction.equipment_defaults", "duplicate_profile_key", "",
			"two equipment profiles on this card carry the same key — a key is the identity of a profile, and the steps pointing at it would have two answers; give one of them a fresh key"))
	}
	return styleNumberTaken()
}

// validateStyleNumberOverride enforces the strict manual-override contract (Q1): when the owner
// hand-sets the article (style_number_source = manual) the value must be present and pass the strict
// format validator, else a field-tagged InvalidArgument on style_number. A generated (server-
// proposed) value is trusted and not re-validated here; the global UNIQUE(style_number) index guards
// collisions for both paths.
func validateStyleNumberOverride(tc *entity.TechCardInsert) error {
	if tc.StyleNumberSource != entity.StyleNumberSourceManual {
		return nil
	}
	v := strings.TrimSpace(tc.StyleNumber.String)
	if !tc.StyleNumber.Valid || v == "" {
		return apierr.Invalid(entity.NewFieldViolation("style_number", "required_for_manual_override", "",
			"a manual override needs a style number; set style_number_source=generated to use the proposal"))
	}
	if reason := stylenumber.ValidateManual(v); reason != "" {
		return apierr.Invalid(entity.NewFieldViolation("style_number", reason, "", stylenumber.ManualHint()))
	}
	return nil
}

// techCardConvertErr maps a pb -> entity conversion failure onto a gRPC status. A field-tagged
// *entity.ValidationError becomes an InvalidArgument carrying a BadRequest FieldViolation, so the
// admin client's applyServerFieldErrors can pin the message to the exact input that caused it
// ("bom_items[3].name") instead of dropping a form-level banner the user has to hunt through.
// Anything else keeps the previous plain-string InvalidArgument.
func techCardConvertErr(err error) error {
	var ve *entity.ValidationError
	if errors.As(err, &ve) {
		return apierr.Invalid(ve)
	}
	return status.Errorf(codes.InvalidArgument, "%v", err)
}

// The leaf-category check (plan Q5) is deliberately gone. It rejected any category_id that had
// children, forcing every tech card to be filed under a level-3 type. That is wrong: only the TOP
// category is conceptually required, and sub-category/type are optional refinements — a style that
// is simply "tops" is legitimate, and so is one filed at a sub-category. The store now derives
// top/sub/type from category_id at whatever depth it was picked (syncStyleCategoryTriple), so a
// shallow pick produces a correct, if less specific, triple rather than an error.
//
// Nothing replaces it at write time: the FK on tech_card.category_id already rejects an unknown id,
// and a category whose tree has no top-level ancestor is caught by the derivation itself. A "a
// released style must have a top category" rule belongs on a stage transition (where style_number
// is already gated), not on every save — a card is routinely created before its category is chosen.

// CreateTechCard creates a new tech card with its nested sections.
func (s *Server) CreateTechCard(ctx context.Context, req *pb_admin.CreateTechCardRequest) (*pb_admin.CreateTechCardResponse, error) {
	canReadCosting, canWriteCosting := s.costingAccess(ctx)
	if !canWriteCosting && techCardInsertHasCostingData(req.TechCard) {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to set cost data (costing block or BOM prices)")
	}
	// §8, rule 1 — BEFORE the conversion, the only ordering that guarantees a stale bundle reads the
	// gate's sentence («update the admin panel») rather than a field violation about a control it does
	// not render. Rule 2 has nothing to say here: a card being created has no stored facts to erase.
	if err := machineCapabilityWireGate(req.TechCard); err != nil {
		return nil, err
	}
	// Щит узлов сборки (0307), тот же довод и тот же момент.
	if err := mediaCapabilityWireGate(req.TechCard); err != nil {
		return nil, err
	}
	if err := assemblyCapabilityWireGate(req.TechCard); err != nil {
		return nil, err
	}
	// Щит видов операций (0324), тот же довод и тот же момент. Стор-гейта на создании у него нет:
	// парного `*_cleared` он не несёт, а сказать про несуществующую карточку ему больше нечего.
	if err := operationKindsWireGate(req.TechCard); err != nil {
		return nil, err
	}
	// Щит оси «работа» (0330), тот же довод и тот же момент.
	if err := operationWorkWireGate(req.TechCard); err != nil {
		return nil, err
	}
	// Щит количеств на связях шага (0334), тот же довод и тот же момент. Стор-гейта на создании у
	// него нет по той же причине, что у щита видов: парного `*_cleared` он не несёт, а сказать про
	// несуществующую карточку ему больше нечего.
	if err := bomQtyWireGate(req.TechCard); err != nil {
		return nil, err
	}
	// Стор-гейт с nil вместо сохранённой карточки — не заглушка, а ровно то, чем создание
	// является: карточки ещё нет, стирать нечего. Единственное, что он тут скажет, — «снять
	// разметку» у создаваемой карточки бессмысленно, и это надо сказать, а не пропустить.
	if err := mediaCapabilityStoredGate(req.TechCard, nil); err != nil {
		return nil, err
	}
	if err := assemblyCapabilityStoredGate(req.TechCard, nil); err != nil {
		return nil, err
	}
	// Снятая работа на СОЗДАВАЕМОЙ карточке — всегда отказ: «уже несла этот токен» здесь заведомо
	// ложно, карточки ещё нет. nil тут не заглушка, а ровно то, чем создание является.
	if err := operationWorkRetiredGate(req.TechCard, nil); err != nil {
		return nil, err
	}
	tc, err := dto.ConvertPbTechCardInsertToEntity(req.TechCard)
	if err != nil {
		return nil, techCardConvertErr(err)
	}
	if err := validateStyleNumberOverride(tc); err != nil {
		return nil, err
	}
	// Заявки провенанса 'lays' на процент раскроя проверяются ПО СУТИ (MAJOR 3): у новой карточки
	// эха не бывает — либо число совпало с текущей медианой сервера и бейдж подтверждён, либо
	// строка ляжет как manual.
	if err := s.verifyBomWastageClaims(ctx, nil, tc.BomItems); err != nil {
		return nil, err
	}
	// Server-stamp the audit trail (norm §2.11); client-sent values are ignored.
	username := authsrv.GetAdminUsername(ctx)
	tc.CreatedBy, tc.UpdatedBy = username, username
	freshSignoffs := prepareCreateTechCardSignoffs(tc, username, time.Now().UTC())
	if costingSignoffChanged(nil, tc.Signoffs, freshSignoffs) && !canReadCosting {
		return nil, status.Error(codes.PermissionDenied, "costing:read is required to change the costing sign-off")
	}
	// §9 on the create path, in the same slot it holds on update: after the fresh set is known and
	// BEFORE the digests are stamped, because a refusal must reject the REQUEST rather than leave a
	// fingerprint behind it. A card can be created with CONSTRUCTION already approved, so the gate
	// belongs on both paths or on neither.
	if err := validateFusingSignGate(tc, freshSignoffs); err != nil {
		return nil, apierr.Invalid(err)
	}
	// A card can be created with sections already approved, and a linked BOM line reads back enriched
	// here exactly as it does on update — so the same correction applies. The card id is 0 on purpose:
	// it does not exist yet, so it can carry neither measured areas nor a recipe (Ф-П).
	if err := s.restampFreshSignoffDigests(ctx, 0, tc, freshSignoffs); err != nil {
		slog.Default().ErrorContext(ctx, "can't finalize fresh tech card sign-off digest",
			slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't finalize sign-off approval; try again")
	}

	id, err := s.repo.TechCards().AddTechCard(ctx, tc)
	if err != nil {
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, apierr.Invalid(ve)
		}
		if s.repo.IsErrUniqueViolation(err) {
			return nil, techCardUniqueViolation(err)
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, techCardFKMsg)
		}
		slog.Default().ErrorContext(ctx, "can't add tech card",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add tech card")
	}
	s.seedProductCostsFromTechCard(ctx, id, 0)
	s.snapshotReleaseIfReleased(ctx, id)
	return &pb_admin.CreateTechCardResponse{Id: int32(id)}, nil
}

// SuggestStyleNumber proposes the next free style number for a season (Q1). Advisory: the client may
// accept the proposal (style_number_source=GENERATED) or override it (MANUAL) on the tech-card write.
func (s *Server) SuggestStyleNumber(ctx context.Context, req *pb_admin.SuggestStyleNumberRequest) (*pb_admin.SuggestStyleNumberResponse, error) {
	code, year, err := dto.ConvertPbSkuSeasonToEntity(req.SkuSeason)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid sku_season: %v", err)
	}
	if code == "" {
		return nil, status.Error(codes.InvalidArgument, "sku_season (code + year) is required to propose a style number")
	}
	proposal, err := s.repo.TechCards().SuggestStyleNumber(ctx, string(code), year)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't suggest style number", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't suggest style number")
	}
	return &pb_admin.SuggestStyleNumberResponse{StyleNumber: proposal}, nil
}

// GetTechCard returns a tech card by id with its nested sections resolved.
func (s *Server) GetTechCard(ctx context.Context, req *pb_admin.GetTechCardRequest) (*pb_admin.GetTechCardResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	tc, err := s.repo.TechCards().GetTechCardById(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't get tech card by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get tech card")
	}
	pbTc := dto.ConvertEntityTechCardToPb(tc, s.costingFxForVatCountry(ctx, req.GetVatCountryCode()))
	if read, _ := s.costingAccess(ctx); !read {
		stripTechCardCosting(pbTc)
	}
	// Tokenized read urls are minted on the RESPONSE only — the release-snapshot path
	// converts the entity separately and must stay token-free (persisted blobs).
	s.patternURLs.FillTechCardPatternURLs(ctx, s.patternURLsBaseURL, pbTc.GetTechCard().GetPatterns())
	return &pb_admin.GetTechCardResponse{
		TechCard: pbTc,
		// The card-viewer token rides the response for the same reason the urls above do:
		// it must never land on common.TechCard, which release snapshots persist. Best
		// effort — "" on any failure, never a failed read.
		PatternViewerToken: s.patternURLs.MintCardViewerToken(ctx, int(req.Id)),
	}, nil
}

// UpdateTechCard updates a tech card, replacing its nested sections.
func (s *Server) UpdateTechCard(ctx context.Context, req *pb_admin.UpdateTechCardRequest) (*pb_admin.UpdateTechCardResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	canReadCosting, canWriteCosting := s.costingAccess(ctx)
	if !canWriteCosting && techCardInsertHasCostingData(req.TechCard) {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to modify cost data (costing block or BOM prices)")
	}
	// §8, rule 1 — ahead of the conversion for the same reason it is ahead of it on Create: it reads
	// the WIRE and owes the entity nothing, and a stale bundle that echoes a type it cannot render
	// must hear «update the admin panel» rather than the converter's «pick a type from the list»,
	// which names a control that bundle does not have. Rule 2 stays where it is — it needs the stored
	// card, which is loaded below.
	if err := machineCapabilityWireGate(req.TechCard); err != nil {
		return nil, err
	}
	// Тот же довод, тот же момент — щит узлов сборки (0307).
	if err := mediaCapabilityWireGate(req.TechCard); err != nil {
		return nil, err
	}
	if err := assemblyCapabilityWireGate(req.TechCard); err != nil {
		return nil, err
	}
	// Тот же довод, тот же момент — щит видов операций (0324).
	if err := operationKindsWireGate(req.TechCard); err != nil {
		return nil, err
	}
	// Тот же довод, тот же момент — щит оси «работа» (0330).
	if err := operationWorkWireGate(req.TechCard); err != nil {
		return nil, err
	}
	// Тот же довод, тот же момент — щит количеств на связях шага (0334).
	if err := bomQtyWireGate(req.TechCard); err != nil {
		return nil, err
	}
	tc, err := dto.ConvertPbTechCardInsertToEntity(req.TechCard)
	if err != nil {
		return nil, techCardConvertErr(err)
	}
	if err := validateStyleNumberOverride(tc); err != nil {
		return nil, err
	}
	stored, err := s.repo.TechCards().GetTechCardByIdConsistent(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't load stored tech card before update",
			slog.Int("tech_card_id", int(req.Id)), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card; try again")
	}
	if stored == nil {
		slog.Default().ErrorContext(ctx, "stored tech card reload returned nil before update",
			slog.Int("tech_card_id", int(req.Id)))
		return nil, status.Error(codes.Internal, "can't load tech card; try again")
	}
	// §8, rule 2 — the first thing done with `stored`, because it is about what this save would
	// DESTROY and every line below either reconciles sign-offs or moves data toward the store.
	if err := machineCapabilityStoredGate(req.TechCard, stored); err != nil {
		return nil, err
	}
	// Щит + контентный бекстоп для узлов: отказывает и устаревшей вкладке, и осведомлённой, но
	// пустой записи, которая стёрла бы разметку молча (параллельная вкладка, AI-черновик,
	// восстановленный до-фичевый черновик).
	if err := mediaCapabilityStoredGate(req.TechCard, stored); err != nil {
		return nil, err
	}
	if err := assemblyCapabilityStoredGate(req.TechCard, stored); err != nil {
		return nil, err
	}
	// Щит видов операций (0324) — правило 2, то самое, что срабатывает на практике: payload
	// отставшей вкладки, выбросившей непонятные ей блоки, выглядит невинно, и только хранилище
	// знает, какие восемнадцать полей на старых парах (глагол, machine_type) — и какие токены,
	// дописанные волной в словари живых колонок, — запись сотрёт.
	if err := operationKindsStoredGate(req.TechCard, stored); err != nil {
		return nil, err
	}
	// Щит оси «работа» (0330) — правило 2 и, следом, правило снятой работы. Оба здесь по одной
	// причине: только хранилище знает, что карточка размечена, и только оно даёт снятой работе
	// право доехать там, где строка уже её несёт.
	if err := operationWorkStoredGate(req.TechCard, stored); err != nil {
		return nil, err
	}
	if err := operationWorkRetiredGate(req.TechCard, stored); err != nil {
		return nil, err
	}
	// Щит количеств на связях шага (0334) — правило 2, то самое, что срабатывает на практике:
	// payload отставшей вкладки про количества не говорит вовсе и выглядит невинно, а полная
	// замена операций стёрла бы их молча и безвозвратно.
	if err := bomQtyStoredGate(req.TechCard, stored); err != nil {
		return nil, err
	}
	// Заявки провенанса 'lays' на процент раскроя (MAJOR 3): чистое эхо сохранённого бейджа едет
	// verbatim; свежая заявка подтверждается пересчётом предложения — совпало, и только тогда,
	// бейдж штампуется; иначе строка ложится как manual: изменил число — источник стал manual, что
	// бы клиент ни прислал.
	if err := s.verifyBomWastageClaims(ctx, stored, tc.BomItems); err != nil {
		return nil, err
	}
	username := authsrv.GetAdminUsername(ctx)
	tc.UpdatedBy = username // server-stamp; created_by is preserved (not in SET)
	freshSignoffs := reconcileUpdateTechCardSignoffs(tc, req.TechCard.Signoffs, stored.Signoffs,
		username, time.Now().UTC())
	if costingSignoffChanged(stored.Signoffs, tc.Signoffs, freshSignoffs) && !canReadCosting {
		return nil, status.Error(codes.PermissionDenied, "costing:read is required to change the costing sign-off")
	}
	// A cost-stripped account's full-replace save must not blank the costing it never saw.
	if !canWriteCosting {
		if err := preserveStoredCostingFrom(stored, tc); err != nil {
			slog.Default().ErrorContext(ctx, "can't preserve stored tech card costing",
				slog.Int("tech_card_id", int(req.Id)), slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "can't preserve stored costing; try again")
		}
	}
	// A field the payload did not speak must not reach the DIGEST as empty. fabric_direction became
	// optional so a stale tab cannot erase it (the store honours that with IF(:omitted, …)), but it
	// also sits in materialsProjection, whose invariant is that it projects only fields that survive
	// the store round-trip unchanged. Without this the projection would hash NULL while the column
	// keeps one_way, and a MATERIALS approval made from exactly the client this optionality exists
	// for would read «changed since sign-off» immediately — and forever, since re-approving from the
	// same client hashes the same absence. purpose/purpose_note/is_sample dodged this by staying out
	// of the projection; direction cannot, it is a cutting fact the approval is ABOUT.
	carryOmittedFabricDirectionFrom(stored, tc)
	// КАК КРОИТСЯ (0275) is the same contract on the CONSTRUCTION section, and it needs all three legs
	// too: `optional` in the proto, IF(:cut_symmetry_omitted, …) in the store, and this carry before
	// the digest is restamped. Skipping this third one is what makes a sign-off born stale and stay
	// that way. It has a second effect here as well: with the stored marking on the payload, a stale
	// tab that edits pieces_per_garment into an odd number is refused in words by
	// entity.ValidatePieceCutSymmetry instead of by a raw two-column CHECK naming a field it did touch
	// and a field it did not.
	carryOmittedPieceCutSymmetryFrom(stored, tc)
	// UNI (0302) — третья ветка того же контракта, на той же секции CONSTRUCTION. Без переноса
	// подпись, поставленная из вкладки, которая про градацию не спрашивает, хешировала бы «не
	// помечено» поверх колонки, где пометка стоит.
	carryOmittedPieceUngradedFrom(stored, tc)
	// КАК ДУБЛИРУЕТСЯ (0304) — четвёртая ветка того же контракта, на той же секции CONSTRUCTION и по
	// той же причине. Отличие одно: перенос ещё и ГАСИТ разметку, если эта же правка сняла галку
	// «дублируется», — иначе дайджест подписал бы режим, который стор в ту же транзакцию обнулит.
	carryOmittedPieceFusingFrom(stored, tc)
	// ГЕОМЕТРИЯ УКАЗАНИЙ НА ЭСКИЗЕ (0309) — тот же контракт присутствия, но на секции DESIGN.
	// Вкладка со старым бандлом шлёт выноски без вида, якорей и цвета; без переноса сохранение
	// такой вкладки стёрло бы каждую мерку и скобку на карточке, а подпись DESIGN, поставленная
	// из неё, хешировала бы «просто точки» поверх эскиза, где нарисовано указание.
	dto.CarryOmittedCalloutGeometry(stored, tc)
	if err := validateFreshSignoffSectionPresence(tc, freshSignoffs); err != nil {
		return nil, apierr.Invalid(err)
	}
	// The two sign-off belts, both between the presence check and the restamp — the last point where
	// the fresh set, the stored card and the payload are all in view, and the last point at which a
	// refusal is still a refusal of the REQUEST rather than a fingerprint already taken.
	if err := validateFreshConstructionCarriesStoredEquipment(tc, stored, freshSignoffs); err != nil {
		return nil, apierr.Invalid(err)
	}
	if err := validateFusingSignGate(tc, freshSignoffs); err != nil {
		return nil, apierr.Invalid(err)
	}
	if err := s.restampFreshSignoffDigests(ctx, int(req.Id), tc, freshSignoffs); err != nil {
		slog.Default().ErrorContext(ctx, "can't finalize fresh tech card sign-off digest",
			slog.Int("tech_card_id", int(req.Id)), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't finalize sign-off approval; try again")
	}
	orphanedPatternURLs, err := s.repo.TechCards().UpdateTechCardAndListOrphanedPatternURLs(
		ctx, int(req.Id), tc, int(req.ExpectedLockVersion))
	if err != nil {
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, apierr.Invalid(ve)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "tech card not found")
		}
		if errors.Is(err, entity.ErrTechCardConflict) {
			return nil, status.Error(codes.Aborted, "tech card was modified concurrently; reload and retry")
		}
		if errors.Is(err, entity.ErrTechCardReleased) {
			return nil, status.Error(codes.FailedPrecondition, "tech card is released and frozen; re-open to draft to edit")
		}
		if errors.Is(err, entity.ErrTechCardPurposeLocked) {
			// err, not the bare sentinel: the store appends the references that actually pin the
			// purpose, and that list is the only actionable part of the message.
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		if s.repo.IsErrUniqueViolation(err) {
			return nil, techCardUniqueViolation(err)
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, techCardFKMsg)
		}
		slog.Default().ErrorContext(ctx, "can't update tech card",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update tech card")
	}
	s.deleteOrphanedPatternObjects(ctx, "tech_card", int(req.Id), orphanedPatternURLs)
	s.seedProductCostsFromTechCard(ctx, int(req.Id), int(req.ExpectedLockVersion)+1)
	s.snapshotReleaseIfReleased(ctx, int(req.Id))
	return &pb_admin.UpdateTechCardResponse{}, nil
}

// restampFreshSignoffDigests re-fingerprints the sections THIS save is approving from the content the
// card will PRESENT ON THE NEXT READ, which is not the same thing as the payload that arrived.
//
// dto.ConvertPbTechCardInsertToEntity stamps a fresh approval's signed_digest at parse time, but the
// admin layer first verifies the request's carry/fresh claim against storage. An explicit fresh-section
// set is then passed here because digest equality cannot prove provenance. The final value cannot be
// known at parse time for two reasons:
//
//  1. LINKED BOM LINES (every account). The read query resolves a linked line's name, supplier,
//     supplier_ref, composition, spec and unit from the catalog material, while the payload legitimately
//     carries an empty string for them. Those fields are hashed by the MATERIALS projection, so a digest
//     taken from the raw payload can never equal the one a later read reports.
//  2. THE COSTING RESTORE (accounts without costing:write). Such a payload carries no prices at all (its
//     read was cost-stripped, and a payload that does carry costing data is refused outright), and
//     preserveStoredCosting puts the stored costing block and BOM purchase prices back afterwards —
//     content that feeds the MATERIALS and COSTING projections.
//
// Either way the brand-new approval would read "changed since sign-off" immediately and forever, which
// is the exact failure the digest mechanism exists to prevent. So the digest is re-taken here, from the
// final payload in its read-model form.
//
// Only sections explicitly classified as fresh by prepareCreateTechCardSignoffs or
// reconcileUpdateTechCardSignoffs move. A carried approval is copied from storage and never restamped.
func (s *Server) restampFreshSignoffDigests(ctx context.Context, techCardID int, tc *entity.TechCardInsert, freshSignoffs map[entity.TechCardSignoffSection]bool) error {
	if tc == nil || len(tc.Signoffs) == 0 {
		return nil
	}
	fresh := make([]*entity.TechCardSignoff, 0, len(tc.Signoffs))
	for i := range tc.Signoffs {
		so := &tc.Signoffs[i]
		if so.State != entity.SignoffStateApproved || !freshSignoffs[so.Section] {
			continue
		}
		fresh = append(fresh, so)
	}
	if len(fresh) == 0 {
		return nil // nothing is being approved: no digest may move, and no catalog read is owed
	}
	identities, err := s.linkedBomMaterialIdentities(ctx, tc)
	if err != nil {
		return err
	}
	// ВХОДЫ СЕБЕСТОИМОСТИ, КОТОРЫХ НЕТ В ЭТОЙ ЗАПИСИ (Ф-П). Площади деталей и назначения деталей на
	// ткань живут в своих таблицах и пишутся своими RPC; в payload карточки их нет и быть не может.
	// Стамп обязан взять их у стора — ровно так же, как берёт разрешённую через каталог идентичность
	// строки BOM выше. Не взять значило бы подписать дайджест, который следующее же чтение объявит
	// изменившимся, — вечно устаревшая подпись, которую нечем погасить.
	//
	// Карточка, которую создают ЭТИМ ЖЕ запросом, площадей ещё не имеет (id появляется в той же
	// транзакции): пустой токен ничего не добавляет в проекцию, и это верный ответ.
	// Только когда УТВЕРЖДАЮТ КОСТИНГ: токен входит лишь в эту проекцию, и заставлять утверждение
	// раздела ЭТИКЕТКИ ходить в площади, листы и рецепт значило бы дать несвязанной подсистеме право
	// уронить чужую подпись.
	if techCardID > 0 && freshSignoffs[entity.SignoffCosting] {
		derived, err := s.repo.TechCards().GetTechCardDerivedCostInputsDigest(ctx, techCardID)
		if err != nil {
			return err
		}
		tc.DerivedCostInputsDigest = derived
	}
	final := dto.TechCardSectionDigestsAsRead(tc, identities)
	for _, so := range fresh {
		d := final[so.Section]
		so.SignedDigest = sql.NullString{String: d, Valid: d != ""}
	}
	return nil
}

// stampFreshTechCardSignoffAudit owns the author and timestamp of an approval made by this request.
// Freshness has already been verified against storage; carried approvals are never selected here.
func stampFreshTechCardSignoffAudit(tc *entity.TechCardInsert, freshSignoffs map[entity.TechCardSignoffSection]bool, username string, now time.Time) {
	if tc == nil {
		return
	}
	for i := range tc.Signoffs {
		if tc.Signoffs[i].State != entity.SignoffStateApproved || !freshSignoffs[tc.Signoffs[i].Section] {
			continue
		}
		tc.Signoffs[i].SignedBy = sql.NullString{String: username, Valid: username != ""}
		tc.Signoffs[i].SignedAt = sql.NullTime{Time: now.UTC(), Valid: true}
	}
}

// prepareCreateTechCardSignoffs makes every approval on a new card fresh. There is no stored row a
// client can legitimately carry, so any supplied digest or audit identity is untrusted and discarded.
func prepareCreateTechCardSignoffs(tc *entity.TechCardInsert, username string, now time.Time) map[entity.TechCardSignoffSection]bool {
	fresh := make(map[entity.TechCardSignoffSection]bool)
	if tc == nil {
		return fresh
	}
	for i := range tc.Signoffs {
		if tc.Signoffs[i].State != entity.SignoffStateApproved {
			continue
		}
		tc.Signoffs[i].SignedDigest = sql.NullString{}
		fresh[tc.Signoffs[i].Section] = true
	}
	dto.StampTechCardSignoffDigests(tc)
	stampFreshTechCardSignoffAudit(tc, fresh, username, now)
	return fresh
}

// reconcileUpdateTechCardSignoffs makes storage authoritative for a carried approval. A non-empty
// wire digest is only a carry request; when storage has an approved row for the same section its audit
// fields and digest replace the request values. Without such a row the approval is fresh and receives
// a new server-owned digest, author and timestamp.
func reconcileUpdateTechCardSignoffs(tc *entity.TechCardInsert, incoming []*pb_common.TechCardSignoff,
	stored []entity.TechCardSignoff, username string, now time.Time) map[entity.TechCardSignoffSection]bool {
	fresh := make(map[entity.TechCardSignoffSection]bool)
	if tc == nil {
		return fresh
	}
	storedBySection := make(map[entity.TechCardSignoffSection]entity.TechCardSignoff, len(stored))
	carried := make(map[entity.TechCardSignoffSection]entity.TechCardSignoff)
	for i := range stored {
		storedBySection[stored[i].Section] = stored[i]
	}
	for i := range tc.Signoffs {
		so := &tc.Signoffs[i]
		if so.State != entity.SignoffStateApproved {
			continue
		}
		wireDigest := ""
		if i < len(incoming) && incoming[i] != nil {
			wireDigest = incoming[i].GetSignedDigest()
		}
		storedSignoff, canCarry := storedBySection[so.Section]
		if wireDigest != "" && canCarry && storedSignoff.State == entity.SignoffStateApproved {
			so.SignedBy = storedSignoff.SignedBy
			so.SignedAt = storedSignoff.SignedAt
			so.SignedDigest = storedSignoff.SignedDigest
			carried[so.Section] = storedSignoff
			continue
		}
		so.SignedDigest = sql.NullString{}
		fresh[so.Section] = true
	}
	dto.StampTechCardSignoffDigests(tc)
	// StampTechCardSignoffDigests also fills an approved row whose digest is NULL. Re-apply carried
	// storage afterwards so an unverifiable legacy approval remains stale instead of being silently
	// blessed with the current content merely because the client sent a non-empty carry claim.
	for i := range tc.Signoffs {
		storedSignoff, ok := carried[tc.Signoffs[i].Section]
		if !ok {
			continue
		}
		tc.Signoffs[i].SignedBy = storedSignoff.SignedBy
		tc.Signoffs[i].SignedAt = storedSignoff.SignedAt
		tc.Signoffs[i].SignedDigest = storedSignoff.SignedDigest
	}
	stampFreshTechCardSignoffAudit(tc, fresh, username, now)
	return fresh
}

// costingSignoffChanged reports a costing-signoff state transition. A fresh re-approval is a change
// even when the stored and requested states are both approved; an unchanged carried approval is not.
func costingSignoffChanged(stored, incoming []entity.TechCardSignoff,
	fresh map[entity.TechCardSignoffSection]bool) bool {
	if fresh[entity.SignoffCosting] {
		return true
	}
	find := func(signoffs []entity.TechCardSignoff) (entity.TechCardSignoffState, bool) {
		for i := range signoffs {
			if signoffs[i].Section == entity.SignoffCosting {
				return signoffs[i].State, true
			}
		}
		return "", false
	}
	storedState, storedOK := find(stored)
	incomingState, incomingOK := find(incoming)
	return storedOK != incomingOK || (storedOK && storedState != incomingState)
}

// validateFreshSignoffSectionPresence rejects an approval over a presence-preserved section the
// update did not carry. Costing restoration runs before this check, so a read-only costing approver's
// hydrated stored block is present; a costing-capable caller who omitted it must include it to approve.
func validateFreshSignoffSectionPresence(tc *entity.TechCardInsert,
	fresh map[entity.TechCardSignoffSection]bool) *entity.ValidationError {
	if tc == nil {
		return nil
	}
	for i := range tc.Signoffs {
		so := tc.Signoffs[i]
		if so.State != entity.SignoffStateApproved || !fresh[so.Section] {
			continue
		}
		present := true
		switch so.Section {
		case entity.SignoffConstruction:
			present = tc.Construction != nil
		case entity.SignoffPackaging:
			present = tc.Packaging != nil
		case entity.SignoffCosting:
			present = tc.Costing != nil
		}
		if !present {
			return entity.NewFieldViolation(fmt.Sprintf("signoffs[%d].section", i),
				fmt.Sprintf("cannot approve %s because this save does not carry that section", so.Section), "",
				"include the section or drop the approval")
		}
	}
	return nil
}

// --- §8: the outdated-client gate, and the sign-off belt behind it -------------------------------

// outdatedMachineClientFix is the ONE way out of both refusals below, so it is written once. A gate
// that says «no» without naming the remedy trains the operator to reload and retry until the data is
// gone by some other route.
const outdatedMachineClientFix = "this version of the admin panel cannot edit those fields, and its save replaces the whole step list — update the admin panel (hard-refresh) and try again"

func outdatedMachineClient(reason string) error {
	return status.Error(codes.FailedPrecondition, "outdated admin client: "+reason+"; "+outdatedMachineClientFix)
}

// The machine capability gate refuses a save from a bundle that never heard of the machine / ВТО
// fields when that save would DESTROY such facts (§8 of the plan).
//
// IT IS TWO FUNCTIONS BECAUSE ITS TWO RULES RUN AT DIFFERENT MOMENTS, and that is not a style
// choice. Rule 1 reads the wire and nothing else, so it runs BEFORE dto conversion — a stale bundle
// that echoes an operation type it cannot render would otherwise be turned away by the converter
// («pick a type from the list»), which points at a control that bundle does not have and cannot be
// acted on. Rule 2 needs the stored card and therefore cannot run until it is loaded. Fusing them
// into one call forced the whole gate down to the later point, and rule 1's sentence with it.
//
// Operations are full-replace with no per-field protection and the equipment park is presence-gated
// one level deeper, so a payload from a pre-0306 bundle simply omits fifteen columns per step and the
// store writes the omission. The deploy window for that bundle is long by decision (the client is
// frozen until a neighbouring branch merges), so «deploy the two together» is not the mitigation it
// usually is — this is.
//
// TWO RULES, and both are needed:
//
//  1. THE PAYLOAD ECHOES what it cannot edit. A stale bundle round-trips values it does not
//     understand as raw enum numbers; a payload that speaks MACHINE / PRESS / PRESS_OPEN, any of the
//     fifteen step fields or the equipment wrapper WITHOUT declaring awareness is such an echo. Read
//     off the WIRE and not off the entity on purpose: the converter canonicalises the nine legacy
//     types into (machine, <machine>) before an entity exists, so an entity-side check would read a
//     perfectly ordinary old-client `lockstitch` step as «speaks MACHINE» and lock out the client
//     this gate exists to keep working.
//  2. THE STORED CARD HOLDS the facts. This is the one that fires in practice: the payload of a
//     bundle that dropped the unknown fields looks innocent, and only storage knows what the save is
//     about to erase. Migration 0306 itself creates such cards (every ex-lockstitch step now carries
//     machine_type), which makes those cards read-only for the old bundle — a price accepted in the
//     plan, because silent erasure is worse and a fifteen-column carry is not a per-save mechanic.
//
// The gate is silent on every card with no machine facts, which today is almost all of them.
func machineCapabilityWireGate(pb *pb_common.TechCardInsert) error {
	if pb.GetMachineFieldsAware() {
		return nil
	}
	if payloadSpeaksMachineFields(pb) {
		return outdatedMachineClient("the payload carries machine / pressing values it does not declare support for")
	}
	return nil
}

// machineCapabilityStoredGate is rule 2, and it runs only after the stored card is in hand. It
// repeats the awareness check rather than assuming the wire gate already passed: the two are called
// from different places, and a rule that silently depends on its sibling having run first is one
// refactor away from not running at all.
func machineCapabilityStoredGate(pb *pb_common.TechCardInsert, stored *entity.TechCard) error {
	if pb.GetMachineFieldsAware() {
		// НАБЛЮДАЕМОСТЬ ВМЕСТО ОТКАЗА — и это осознанный выбор, а не недоделка.
		//
		// У двух младших щитов (узлы, снимки) есть контентный бекстоп: осведомлённая запись, не
		// несущая содержимого против карточки, которая его несёт, отвергается, а выход из отказа
		// даёт парный флаг намерения. Здесь такого флага нет, и заводить его ради этого случая
		// значило бы третий раз расширить контракт: у машинных фактов НЕТ СТАБИЛЬНОГО КЛЮЧА —
		// операции пишутся полной заменой, — поэтому перенести хранимое, как переносится разметка
		// детали, невозможно, и единственной формой защиты остался бы отказ без права на ошибку.
		//
		// Оба реальных пути потери закрыты в источнике: «заменить весь список» ИИ-черновиком теперь
		// называет число шагов с машинками ДО нажатия, а до-0306 черновики выметаются версией ключа
		// хранилища. Остаётся скрипт и сидер — их потеря становится СЧИТАЕМОЙ здесь, а не
		// невидимой. Если счётчик когда-нибудь начнёт расти, это и будет доводом за флаг намерения.
		if storedHasMachineFacts(stored) && !payloadSpeaksMachineFields(pb) {
			slog.Default().Warn("machine gate: aware payload drops stored machine/pressing facts",
				slog.String("gate", "stored"), slog.String("cell", "aware:yes/payload:none/stored:facts"))
		}
		return nil
	}
	if storedHasMachineFacts(stored) {
		return outdatedMachineClient("this tech card holds machine / pressing parameters (equipment profiles, or machine and pressing settings on its steps)")
	}
	return nil
}

// payloadSpeaksMachineFields is rule 1's predicate, read off the WIRE (see the note above).
//
// The three operation types are the ones the split ADDED (13-15); FUSING / HANDWORK / OTHER and the
// nine legacy tokens all predate it and an old bundle sends them legitimately.
func payloadSpeaksMachineFields(pb *pb_common.TechCardInsert) bool {
	if pb == nil {
		return false
	}
	if pb.GetConstruction().GetEquipmentDefaults() != nil {
		return true
	}
	for _, o := range pb.GetOperations() {
		if o == nil {
			continue
		}
		switch o.GetOperationType() {
		case pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS,
			pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS_OPEN:
			return true
		}
		if o.GetMachineType() != pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_UNKNOWN ||
			strings.TrimSpace(o.GetMachineProfileKey()) != "" ||
			o.GetThreadCount() != 0 ||
			o.GetNeedleType() != pb_common.TechCardNeedleType_TECH_CARD_NEEDLE_TYPE_UNKNOWN ||
			o.GetNeedleSizeNm() != 0 ||
			o.GetThreadTension() != pb_common.TechCardThreadTension_TECH_CARD_THREAD_TENSION_UNKNOWN ||
			strings.TrimSpace(o.GetThreadTensionNote()) != "" ||
			strings.TrimSpace(o.GetStitchWidthMm().GetValue()) != "" {
			return true
		}
		if o.GetPressEquipment() != pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_UNKNOWN ||
			strings.TrimSpace(o.GetPressProfileKey()) != "" ||
			o.GetPressTemperatureC() != 0 ||
			o.GetPressDwellSec() != 0 ||
			strings.TrimSpace(o.GetPressPressureNCm2().GetValue()) != "" ||
			o.PressSteam != nil || // three-valued: «без пара» is a stated fact, not an absence
			o.GetPressCloth() != pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_UNKNOWN {
			return true
		}
	}
	return false
}

// storedHasMachineFacts is rule 2's predicate: profiles on the card, or ANY of the fifteen new
// columns filled on ANY step.
//
// machine_type counts, including on a step migration 0306 rewrote from a legacy token. That is not an
// oversight to soften later: the column is what the old bundle would blank, and «it was only the
// machine we lost» is exactly the silent damage this gate exists to stop.
func storedHasMachineFacts(stored *entity.TechCard) bool {
	if stored == nil {
		return false
	}
	if c := stored.Construction; c != nil && c.EquipmentDefaults != nil {
		if len(c.EquipmentDefaults.Machines)+len(c.EquipmentDefaults.Presses) > 0 {
			return true
		}
	}
	for i := range stored.Operations {
		o := &stored.Operations[i]
		if o.MachineType.Valid || o.MachineProfileKey.Valid || o.ThreadCount.Valid ||
			o.NeedleType.Valid || o.NeedleSizeNm.Valid || o.ThreadTension.Valid ||
			o.ThreadTensionNote.Valid || o.StitchWidthMm.Valid ||
			o.PressEquipment.Valid || o.PressProfileKey.Valid || o.PressTemperatureC.Valid ||
			o.PressDwellSec.Valid || o.PressPressureNCm2.Valid || o.PressSteam.Valid ||
			o.PressCloth.Valid {
			return true
		}
	}
	return false
}

// validateFreshConstructionCarriesStoredEquipment refuses a FRESH CONSTRUCTION approval that would be
// stamped without the equipment park the card actually keeps.
//
// The wrapper is the presence signal one level below the section: absent means «do not touch the
// stored park» and the store obeys it — in BOTH shapes, a construction sent without the wrapper and
// a construction not sent at all (preserveAbsentSection). The digest projection cannot see that: an
// absent wrapper projects as «no profiles», which is the same tail an empty park projects, and it has
// no view of storage to tell the two apart. So the approval would be fingerprinted over a park that
// is not there and the very next read would report «changed since sign-off» — permanently, because
// re-approving from the same bundle hashes the same absence. The condition is therefore stated as
// «storage HAS profiles AND the payload's wrapper does not carry them», independent of whether the
// section itself was sent; this is the only place both halves are in view.
//
// The «construction not sent at all» half is normally caught one step earlier by
// validateFreshSignoffSectionPresence with a better sentence («this save does not carry that
// section»). It stays in the condition anyway: this belt must not depend on the ORDER of the two
// checks to be correct, and the capability gate above already makes both mostly unreachable — they
// insure the day the gate is loosened.
func validateFreshConstructionCarriesStoredEquipment(tc *entity.TechCardInsert, stored *entity.TechCard,
	fresh map[entity.TechCardSignoffSection]bool) *entity.ValidationError {
	if tc == nil || !fresh[entity.SignoffConstruction] {
		return nil
	}
	if stored == nil || stored.Construction == nil || stored.Construction.EquipmentDefaults == nil ||
		len(stored.Construction.EquipmentDefaults.Machines)+len(stored.Construction.EquipmentDefaults.Presses) == 0 {
		return nil
	}
	if tc.Construction != nil && tc.Construction.EquipmentDefaults != nil {
		return nil // present, even empty: an empty park is a deliberate «delete them all» and hashes as one
	}
	i, ok := freshConstructionSignoffIndex(tc, fresh)
	if !ok {
		return nil
	}
	return entity.NewFieldViolation(fmt.Sprintf("signoffs[%d].section", i),
		"construction_equipment_not_carried", "",
		"this card holds equipment profiles and this save does not carry them, so the approval would be fingerprinted without them and read as «changed since sign-off» from the moment it is made; send construction.equipment_defaults as read, or drop the approval")
}

// freshConstructionSignoffIndex finds the payload index of the CONSTRUCTION approval this save is
// making fresh, so a refusal can point at the control that carries it.
func freshConstructionSignoffIndex(tc *entity.TechCardInsert,
	fresh map[entity.TechCardSignoffSection]bool) (int, bool) {
	if tc == nil || !fresh[entity.SignoffConstruction] {
		return 0, false
	}
	for i := range tc.Signoffs {
		if tc.Signoffs[i].Section == entity.SignoffConstruction &&
			tc.Signoffs[i].State == entity.SignoffStateApproved {
			return i, true
		}
	}
	return 0, false
}

// --- §9: the fusing sign gate --------------------------------------------------------------------

// validateFusingSignGate refuses a FRESH CONSTRUCTION approval while any fusing step still resolves
// no temperature or no dwell.
//
// The philosophy is 0289's and it is not «required fields by another name»: a DRAFT step may say
// nothing at all — a technologist sketches the order first and fills the recipe later, and a save
// that refuses a half-typed step is a save nobody makes. The SIGNATURE is the different thing: it
// says «this is what the floor makes», and a fusing instruction with neither a temperature nor a
// dwell is not an instruction. The defect it hides is invisible at the machine and shows up after the
// first wash, by which time the card says it was approved.
//
// RESOLUTION IS THE LADDER OF §3, not a column check: the step's own override, else the profile it
// names by key, else the ONE press profile of the card whose equipment matches the step and whose
// process is fusing or universal. Several matching profiles with no key on the step is not «pick the
// first» — it is two answers to one question, and the sheet would print whichever the ladder happened
// to reach, so it is refused as ambiguity with its own sentence.
//
// Pressure, steam and press cloth are deliberately NOT required: they are desirable, and a gate that
// demands everything is a gate that gets worked around by approving from another screen.
func validateFusingSignGate(tc *entity.TechCardInsert,
	fresh map[entity.TechCardSignoffSection]bool) *entity.ValidationError {
	idx, ok := freshConstructionSignoffIndex(tc, fresh)
	if !ok {
		return nil
	}
	// The park of THIS payload, which is what the card will present on the next read: a save that
	// omits the wrapper preserves the stored park, and that case is refused by the belt above rather
	// than resolved against storage here — the ladder must never bless a step from a park the
	// signature is not being taken over.
	var presses []entity.TechCardPressProfile
	if tc.Construction != nil && tc.Construction.EquipmentDefaults != nil {
		presses = tc.Construction.EquipmentDefaults.Presses
	}
	var unresolved, ambiguous []string
	for i := range tc.Operations {
		o := &tc.Operations[i]
		if o.OperationType != entity.OpTypeFusing {
			continue
		}
		needTemp, needDwell := !o.PressTemperatureC.Valid, !o.PressDwellSec.Valid
		if !needTemp && !needDwell {
			continue // the step states both itself; the profile it may also name is irrelevant here
		}
		profile, isAmbiguous := resolveFusingPressProfile(o, presses)
		if isAmbiguous {
			ambiguous = append(ambiguous, fusingStepLabel(o, i))
			continue
		}
		if profile != nil {
			needTemp = needTemp && !profile.PressTemperatureC.Valid
			needDwell = needDwell && !profile.PressDwellSec.Valid
		}
		if needTemp || needDwell {
			unresolved = append(unresolved, fusingStepLabel(o, i))
		}
	}
	switch {
	case len(ambiguous) > 0:
		return entity.NewFieldViolation(fmt.Sprintf("signoffs[%d]", idx),
			"fusing_press_profile_ambiguous", "",
			fmt.Sprintf("fusing step(s) %s name no press profile and the card holds more than one that fits, so the temperature and dwell they run at are not decided; point each step at a profile (press_profile_key) or state the values on the step before approving",
				strings.Join(ambiguous, ", ")))
	case len(unresolved) > 0:
		return entity.NewFieldViolation(fmt.Sprintf("signoffs[%d]", idx),
			"fusing_missing_temperature_or_dwell", "",
			fmt.Sprintf("fusing step(s) %s resolve no temperature and/or no dwell — a fusing instruction without them is not one, and the defect shows up after the first wash; set press_temperature_c and press_dwell_sec on the step, or point it at a press profile that carries them",
				strings.Join(unresolved, ", ")))
	}
	return nil
}

// resolveFusingPressProfile walks steps 2 and 3 of the §3 ladder for a fusing step. The second return
// value is «ambiguous»: the step named no profile and the card holds more than one that fits.
//
// A key that names nothing resolves to «not set» rather than to an error, which is the same detach
// rule the converter applies (a profile deleted from the park must not veto the save) — the step then
// has to state the values itself, and the gate says so.
func resolveFusingPressProfile(o *entity.TechCardOperation,
	presses []entity.TechCardPressProfile) (*entity.TechCardPressProfile, bool) {
	if key := strings.TrimSpace(o.PressProfileKey.String); o.PressProfileKey.Valid && key != "" {
		for i := range presses {
			if presses[i].ProfileKey != key {
				continue
			}
			// THE KEY IS NOT A BYPASS AROUND THE PROCESS. Step 3 has always refused a profile
			// declared for pressing, and step 2 used to take whatever the key pointed at — so the
			// same park answered one question two ways, and the softer answer was reachable by
			// simply putting a key on the step. It is reachable BY THE SERVER: the AI mapper
			// attaches a profile to a drafted step, and attaching an ironing profile to a fusing
			// step handed this gate a ВТО temperature to approve дублирование with. A mismatch
			// resolves to «not set» and never falls through to step 3: the step names THIS profile,
			// and quietly signing it off against a different one is a second wrong answer.
			if !pressProfileFitsStep(&presses[i], entity.OpTypeFusing) {
				return nil, false
			}
			return &presses[i], false
		}
		return nil, false
	}
	if !o.PressEquipment.Valid {
		return nil, false // no equipment named, so there is no type to inherit by
	}
	var found *entity.TechCardPressProfile
	for i := range presses {
		p := &presses[i]
		if p.PressEquipment != o.PressEquipment.String || !pressProfileFitsStep(p, entity.OpTypeFusing) {
			continue
		}
		if found != nil {
			return nil, true
		}
		found = p
	}
	return found, false
}

// pressProfileFitsStep is THE rule for «may this press profile be applied to a step of this process»,
// and it is one function on purpose. A profile may declare WHICH process it is for: NULL is universal
// and fits any ВТО step, a declared one fits only its own. An ironing profile is not a fusing recipe
// however well the equipment matches — same machine, different program, and the difference shows up
// as a delamination after the first wash rather than at the press.
//
// It was written out twice, and the copies drifted (see the caller above and aiSolePressProfiles):
// wherever the process was dropped from the question, the answer silently widened.
func pressProfileFitsStep(p *entity.TechCardPressProfile, stepType entity.TechCardOperationType) bool {
	return !p.PressOperationType.Valid || p.PressOperationType.String == string(stepType)
}

// fusingStepLabel names a step the way the sheet does — «оп. 30» — falling back to the position when
// the number is not stamped yet (the converter assigns (i+1)*10, so that fallback is the same value).
func fusingStepLabel(o *entity.TechCardOperation, i int) string {
	if o.OperationNumber.Valid {
		return fmt.Sprint(o.OperationNumber.Int32)
	}
	return fmt.Sprint((i + 1) * 10)
}

// linkedBomMaterialIdentities loads the catalog identity of every material the payload's BOM links, so a
// digest can be taken in the read model's terms. Nil when the BOM links nothing — the common case, which
// then costs no query at all.
//
// A catalog failure is returned to the fresh-approval path: hashing the raw linked line would create
// an approval that can never match the resolved read model.
func (s *Server) linkedBomMaterialIdentities(ctx context.Context, tc *entity.TechCardInsert) (map[int64]dto.BomMaterialIdentity, error) {
	linked := make(map[int64]bool, len(tc.BomItems))
	for _, b := range tc.BomItems {
		if b.MaterialId.Valid && b.MaterialId.Int64 > 0 {
			linked[b.MaterialId.Int64] = true
		}
	}
	if len(linked) == 0 {
		return nil, nil
	}
	// includeArchived: the read query LEFT JOINs material unconditionally, so an archived material still
	// resolves the lines that link it. One list beats N GetMaterial round trips.
	mats, err := s.repo.TechCards().ListMaterials(ctx, "", true)
	if err != nil {
		return nil, fmt.Errorf("load material catalog for sign-off digest: %w", err)
	}
	out := make(map[int64]dto.BomMaterialIdentity, len(linked))
	for i := range mats {
		m := &mats[i]
		if !linked[int64(m.Id)] {
			continue
		}
		out[int64(m.Id)] = dto.BomMaterialIdentity{
			Name:        m.Name,
			Supplier:    m.Supplier.String,
			SupplierRef: m.SupplierRef.String,
			Composition: m.Composition.String,
			Spec:        m.Spec.String,
			Unit:        m.Unit.String,
		}
	}
	return out, nil
}

// snapshotReleaseIfReleased captures an immutable release snapshot (task 11) when a card is in
// the `released` state after a successful save. Because a released card is frozen — the store
// rejects any non-draft edit — a successful save that ends in `released` is always a genuine
// release transition (an already-released card can only move to draft), so this fires exactly
// once per release episode. The snapshot is the enriched read-model as proto-JSON plus the
// computed base-currency unit cost. It is best-effort because the release itself already committed;
// a persistence failure is logged loudly with the exact release episode, never surfaced as a failed
// release RPC.
func (s *Server) snapshotReleaseIfReleased(ctx context.Context, techCardID int) {
	card, err := s.repo.TechCards().GetTechCardByIdConsistent(ctx, techCardID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "release snapshot: can't reload tech card",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return
	}
	if card == nil || card.ApprovalState != entity.TechCardApprovalReleased {
		return
	}
	fx := s.costingFx(ctx)
	blob, err := protojson.Marshal(dto.ConvertEntityTechCardToPb(card, fx))
	if err != nil {
		slog.Default().ErrorContext(ctx, "release snapshot: can't marshal snapshot",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return
	}
	unit, currency := dto.ComputeTechCardUnitCost(card, fx)
	username := authsrv.GetAdminUsername(ctx)
	releaseEpisode := "unknown"
	if card.ReleasedAt.Valid {
		releaseEpisode = card.ReleasedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	rel := entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{
			TechCardId: techCardID,
			ReleasedBy: sql.NullString{String: username, Valid: username != ""},
			UnitCost:   unit,
			Currency:   sql.NullString{String: currency, Valid: unit.Valid && currency != ""},
		},
		Snapshot: string(blob),
	}
	if err := s.repo.TechCards().SaveTechCardRelease(ctx, rel); err != nil {
		slog.Default().ErrorContext(ctx, "RELEASE SNAPSHOT LOST: released card has no immutable snapshot",
			slog.Int("tech_card_id", techCardID), slog.String("release_episode", releaseEpisode), slog.String("err", err.Error()))
		return
	}
	slog.Default().InfoContext(ctx, "captured tech card release snapshot",
		slog.Int("tech_card_id", techCardID), slog.String("release_episode", releaseEpisode))
}

// ListTechCardReleases returns a card's release history (newest-first, metadata only).
func (s *Server) ListTechCardReleases(ctx context.Context, req *pb_admin.ListTechCardReleasesRequest) (*pb_admin.ListTechCardReleasesResponse, error) {
	if req.TechCardId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	rows, err := s.repo.TechCards().ListTechCardReleases(ctx, int(req.TechCardId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list tech card releases", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list tech card releases")
	}
	read, _ := s.costingAccess(ctx)
	out := make([]*pb_common.TechCardReleaseMeta, 0, len(rows))
	for _, r := range rows {
		m := dto.ConvertTechCardReleaseMetaToPb(r)
		if !read {
			stripReleaseMetaCosting(m)
		}
		out = append(out, m)
	}
	return &pb_admin.ListTechCardReleasesResponse{Releases: out}, nil
}

// GetTechCardRelease returns a single release: its metadata plus the frozen contract TechCard
// parsed from the stored blob. An incompatible/corrupt blob degrades to metadata + snapshot_error
// rather than a 500 (hero-v2 rule), so old releases stay readable as the contract evolves.
func (s *Server) GetTechCardRelease(ctx context.Context, req *pb_admin.GetTechCardReleaseRequest) (*pb_admin.GetTechCardReleaseResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "release id is required")
	}
	rel, err := s.repo.TechCards().GetTechCardRelease(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card release not found")
		}
		slog.Default().ErrorContext(ctx, "can't get tech card release", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get tech card release")
	}
	read, _ := s.costingAccess(ctx)
	resp := &pb_admin.GetTechCardReleaseResponse{Release: dto.ConvertTechCardReleaseMetaToPb(rel.TechCardReleaseMeta)}
	if !read {
		stripReleaseMetaCosting(resp.Release)
	}
	var snap pb_common.TechCard
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(rel.Snapshot), &snap); err != nil {
		// The parser error quotes the offending field (and can quote its value) straight out of the
		// frozen snapshot — which embeds the costing block and BOM prices — so a cost-blind caller
		// gets the generic sentence only. The full detail is logged server-side either way.
		resp.SnapshotError = "stored snapshot is incompatible with the current schema"
		if read {
			resp.SnapshotError += ": " + err.Error()
		}
		slog.Default().WarnContext(ctx, "tech card release snapshot won't parse",
			slog.Int("release_id", int(req.Id)), slog.String("err", err.Error()))
	} else {
		// The frozen snapshot embeds the full costing block + BOM prices; redact them too.
		if !read {
			stripTechCardCosting(&snap)
		}
		resp.Snapshot = &snap
	}
	return resp, nil
}

// seedProductCostsFromTechCard best-effort propagates a saved tech card's computed unit
// cost to its linked products' cost_price for margin analytics. It is intentionally
// non-fatal (a failure never blocks the tech card save) and only runs when the costing is
// already in the base currency — the shop has no live FX, so a non-base costing cannot be
// converted. Only products whose PRIMARY card is this one are seeded, and a manually-set
// cost is never overwritten (use SyncProductCostFromTechCard to force). Newly-linked
// products with no primary yet adopt this card as their primary.
func (s *Server) seedProductCostsFromTechCard(ctx context.Context, techCardID, expectedMinLockVersion int) {
	card, err := s.repo.TechCards().GetTechCardByIdConsistent(ctx, techCardID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't reload tech card for product cost seed",
			slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
		return
	}
	if card == nil {
		slog.Default().ErrorContext(ctx, "can't seed product costs: tech card reload returned nil",
			slog.Int("tech_card_id", techCardID))
		return
	}
	if card.LockVersion < expectedMinLockVersion {
		slog.Default().WarnContext(ctx, "skipping product cost seed from stale tech card read",
			slog.Int("tech_card_id", techCardID),
			slog.Int("lock_version", card.LockVersion),
			slog.Int("expected_min_lock_version", expectedMinLockVersion))
		return
	}
	linkedProducts := card.LinkedProductIDs()
	if len(linkedProducts) == 0 {
		return
	}
	if err := s.repo.Products().AssignPrimaryTechCardIfUnset(ctx, techCardID, linkedProducts); err != nil {
		slog.Default().ErrorContext(ctx, "can't assign primary tech card to products",
			slog.Int("tech_card_id", techCardID), slog.Any("product_ids", linkedProducts), slog.String("err", err.Error()))
		return
	}
	// Each colourway is seeded its OWN unit cost (its pins, its norms) — one shared figure was
	// the primary colourway's number written over every product, erasing exactly the divergence
	// per-colourway pinning creates. The card-level figure stays as the fallback for a linked
	// product the card's colourway list somehow misses. Base currency only, as before; a product
	// whose cost is manually set (or run-sourced) is never overwritten — the combined seed enforces
	// provenance, ownership, and the observed card version atomically in SQL.
	fx := s.costingFx(ctx)
	base := cache.GetBaseCurrency()
	rootUnit, rootCcy := dto.ComputeTechCardUnitCost(card, fx)
	var seeded int64
	for _, pid := range linkedProducts {
		unit, currency := dto.ComputeColorwayUnitCost(card, pid, fx)
		if !unit.Valid && !dto.HasColorwayForProduct(card, pid) {
			// Defensive only: a linked product the card's colourway list misses. A colourway
			// with an EMPTY recipe already inherits the style figure inside
			// ComputeColorwayUnitCost, so this fallback is not the per-colourway erasure it
			// looks like. A colourway that EXISTS but cannot be costed completely (an unpriced
			// or unconvertible line) must NOT borrow the style number — that would publish the
			// primary colourway's cost as this one's and hide the very gap the gate exists for.
			unit, currency = rootUnit, rootCcy
		}
		if !unit.Valid || !strings.EqualFold(currency, base) {
			// Say WHICH colourway was withheld and why: the aggregate warning below fires only
			// when the whole card seeded nothing, so a card with 1 of 3 colourways gated would
			// otherwise skip silently while the tab still shows a unit cost.
			if !unit.Valid && dto.HasColorwayForProduct(card, pid) {
				slog.Default().InfoContext(ctx, "colourway cost not seeded: recipe incomplete or not computable",
					slog.Int("tech_card_id", techCardID), slog.Int("product_id", pid))
			}
			continue
		}
		// The COGS decomposition rides the same per-colourway figure (its materials component is
		// THIS colourway's, pins included). A non-convertible breakdown intentionally stays NULL to
		// clear a stale one; a marshal failure skips the combined write and retains both stored fields.
		breakdownJSON := sql.NullString{}
		if bd, ok := dto.ComputeColorwayCostBreakdownBase(card, pid, fx); ok {
			b, merr := json.Marshal(bd)
			if merr != nil {
				slog.Default().ErrorContext(ctx, "can't marshal product cost_breakdown from tech card",
					slog.Int("tech_card_id", techCardID), slog.Int("product_id", pid), slog.String("err", merr.Error()))
				continue
			}
			breakdownJSON = sql.NullString{String: string(b), Valid: true}
		}
		updated, uerr := s.repo.Products().SeedProductCostFromTechCard(
			ctx, pid, techCardID, card.LockVersion, unit.Decimal, breakdownJSON)
		if uerr != nil {
			slog.Default().ErrorContext(ctx, "can't seed product cost from tech card",
				slog.Int("tech_card_id", techCardID), slog.Int("product_id", pid),
				slog.Int("tech_card_lock_version", card.LockVersion), slog.String("err", uerr.Error()))
			continue
		}
		if !updated {
			slog.Default().WarnContext(ctx, "product cost seed predicate rejected the observed tech card snapshot",
				slog.Int("tech_card_id", techCardID), slog.Int("product_id", pid),
				slog.Int("tech_card_lock_version", card.LockVersion))
			continue
		}
		seeded++
	}
	if seeded == 0 {
		slog.Default().InfoContext(ctx, "no product cost seeded from tech card (no base-convertible cost, or provenance elsewhere)",
			slog.Int("tech_card_id", techCardID))
	} else {
		slog.Default().InfoContext(ctx, "seeded product cost_price from tech card",
			slog.Int("tech_card_id", techCardID), slog.Int64("products_updated", seeded))
	}
}

// costingFx loads the effective manual FX rates and pairs them with the base currency, so the
// tech-card costing can be folded into a base-currency unit cost. A load failure degrades to no
// rates (base rollup only for already-base costings) rather than failing the request.
func (s *Server) costingFx(ctx context.Context) dto.CostingFx {
	rates, err := s.repo.TechCards().GetCostingFxRatesToBase(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load costing fx rates", slog.String("err", err.Error()))
		rates = nil
	}
	fx := dto.CostingFx{ToBase: rates, Base: cache.GetBaseCurrency()}
	// The house margin target rides along: it is a costing constant every tech-card read needs. Read
	// from the in-memory cache (loaded at boot, refreshed by UpsertAlertSettings) rather than the
	// settings table — this runs on every tech-card read, and a per-read query for a single number
	// that changes a few times a year would be a poor trade.
	if house := cache.GetTargetMarginPct(); house > 0 {
		fx.HouseTargetMarginPct = decimal.NullDecimal{Decimal: decimal.NewFromFloat(house), Valid: true}
	}
	return fx
}

// costingFxForVatCountry is costingFx plus the VAT scenario a margin on this read is drawn for.
//
// Catalogue prices are VAT-inclusive throughout this system — the order snapshot extracts VAT out of
// them, the accounting engine derives output VAT from them, and the margin-by-style report divides
// them by (1+rate) before comparing to cost. The tech-card costing tab did not, so the two admin
// screens showed the same style at margins a whole VAT rate apart. Netting here closes that.
//
// Country: the caller's if it names one (modelling another market), else the company's domestic VAT
// country — what a studio pricing a style means by "the margin" unless it says otherwise. The rate
// comes from the same `vat_rate` table everything else reads; a country with no rate on file nets
// nothing (an export destination has no VAT to remove) and the tab is told so via vat_country_code
// with an absent vat_rate_pct.
func (s *Server) costingFxForVatCountry(ctx context.Context, requested string) dto.CostingFx {
	fx := s.costingFx(ctx)
	country := strings.ToUpper(strings.TrimSpace(requested))
	if country == "" {
		country = accounting.RegimeRateCountry(entity.VatRegimePLDomestic, "", "")
	}
	fx.VatCountry = country
	if country == "" {
		return fx
	}
	rates, err := s.repo.Accounting().GetVatRatesFor(ctx, []string{country})
	if err != nil {
		// Same degradation as the FX rates above: report the country, net nothing, never fail the read
		// over a missing rate — a costing tab that says "no rate" beats one that will not open.
		slog.Default().ErrorContext(ctx, "can't load vat rate for costing margin",
			slog.String("country", country), slog.String("err", err.Error()))
		return fx
	}
	if r, ok := rates[country]; ok && r.IsPositive() {
		fx.VatRatePct = decimal.NullDecimal{Decimal: r, Valid: true}
	}
	return fx
}

// GetCostingFxRates returns the CURRENT effective FX rate per currency (the latest valid_from on or
// before today), not the full dated history. The rates are auto-maintained by the fxsync ECB worker,
// so the stored history grows daily and only the effective rate is useful to clients (the admin
// margin view and the OPEX/dev-cost base-currency previews). Manual entry has been removed:
// UpsertCostingFxRates is no longer implemented (the RPC falls back to Unimplemented).
func (s *Server) GetCostingFxRates(ctx context.Context, _ *pb_admin.GetCostingFxRatesRequest) (*pb_admin.GetCostingFxRatesResponse, error) {
	// The whole response exists to serve the costing surfaces (margin view, OPEX/dev-cost base
	// previews), so without costing:read it is denied outright like ListOpexLines — there is no
	// non-money structure left to shape, which is the only reason GetStyleCostEstimate can strip
	// instead. The RPC map only requires tech_cards:read, so a cost-blind constructor reached it.
	if read, _ := s.costingAccess(ctx); !read {
		return nil, status.Error(codes.PermissionDenied, "costing:read is required to view costing FX rates")
	}
	rates, err := s.repo.TechCards().ListCostingFxRates(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list costing fx rates", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list costing fx rates")
	}
	// ListCostingFxRates is ordered by currency, valid_from DESC; keep the first row per currency
	// effective today, mirroring GetCostingFxRatesToBase's as-of semantics and bounding the payload
	// to one row per currency regardless of how much history has accumulated.
	now := time.Now().UTC()
	seen := make(map[string]struct{}, len(rates))
	out := make([]*pb_admin.CostingFxRate, 0, len(rates))
	for _, r := range rates {
		if _, ok := seen[r.Currency]; ok {
			continue
		}
		if r.ValidFrom.After(now) {
			continue // not yet effective — look for an earlier row for this currency
		}
		seen[r.Currency] = struct{}{}
		out = append(out, &pb_admin.CostingFxRate{
			Currency:   r.Currency,
			RateToBase: &pb_decimal.Decimal{Value: r.RateToBase.String()},
			ValidFrom:  timestamppb.New(r.ValidFrom),
		})
	}
	return &pb_admin.GetCostingFxRatesResponse{Rates: out}, nil
}

// DeleteTechCard deletes a tech card by id (nested sections cascade). A readable field-tagged
// FailedPrecondition (apierr) is returned when the card is still referenced elsewhere — a sample with
// material movements, a use as an assembly component in another style, or (residual) any other RESTRICT
// the store guard does not explicitly enumerate — never a raw Internal (P4-flyover M2/S24-regression).
func (s *Server) DeleteTechCard(ctx context.Context, req *pb_admin.DeleteTechCardRequest) (*pb_admin.DeleteTechCardResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	orphanedPatternURLs, err := s.repo.TechCards().DeleteTechCardAndListOrphanedPatternURLs(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		if errors.Is(err, entity.ErrSampleHasMovements) {
			return nil, status.Error(codes.FailedPrecondition, "a sample of this tech card has material movements; delete/return them first")
		}
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, apierr.FailedPrecondition(ve)
		}
		slog.Default().ErrorContext(ctx, "can't delete tech card",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't delete tech card")
	}
	s.deleteOrphanedPatternObjects(ctx, "tech_card", int(req.Id), orphanedPatternURLs)
	return &pb_admin.DeleteTechCardResponse{}, nil
}

// ListTechCards returns a paged list of tech-card headers with optional filters.
func (s *Server) ListTechCards(ctx context.Context, req *pb_admin.ListTechCardsRequest) (*pb_admin.ListTechCardsResponse, error) {
	stage, err := dto.ConvertPbTechCardStageToEntityString(req.Stage)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid stage filter: %v", err)
	}

	gender := ""
	if req.Gender != pb_common.GenderEnum_GENDER_ENUM_UNKNOWN {
		g, err := dto.ConvertPbGenderEnumToEntityGenderEnum(req.Gender)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid gender filter: %v", err)
		}
		gender = string(g)
	}

	purpose := strings.ToLower(strings.TrimSpace(req.Purpose))
	if purpose != "" && !entity.ValidTechCardPurposes[entity.TechCardPurpose(purpose)] {
		return nil, status.Errorf(codes.InvalidArgument, "invalid purpose filter: must be sellable|auxiliary")
	}
	seasonCode, seasonYear, err := dto.ConvertPbSkuSeasonToEntity(req.SkuSeason)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid sku_season filter: %v", err)
	}

	categoryIDs := make([]int, 0, len(req.GetCategoryIds()))
	for _, id := range req.GetCategoryIds() {
		if id <= 0 {
			return nil, status.Error(codes.InvalidArgument, "category_ids must be positive")
		}
		categoryIDs = append(categoryIDs, int(id))
	}
	filter := entity.TechCardListFilter{
		Stage:       stage,
		Gender:      gender,
		Brand:       strings.TrimSpace(req.Brand),
		SeasonCode:  seasonCode,
		SeasonYear:  seasonYear,
		Name:        strings.TrimSpace(req.Name),
		ProductId:   int(req.ProductId),
		Purpose:     purpose,
		CategoryIds: categoryIDs,
	}

	cards, total, err := s.repo.TechCards().ListTechCards(ctx, int(req.Limit), int(req.Offset),
		dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor), filter)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list tech cards",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't list tech cards")
	}

	items := make([]*pb_common.TechCardListItem, 0, len(cards))
	for i := range cards {
		items = append(items, dto.ConvertEntityTechCardToListItemPb(&cards[i]))
	}
	return &pb_admin.ListTechCardsResponse{TechCards: items, Total: int32(total)}, nil
}

// GetStylePipeline returns the development board: per-stage counts + a few light preview cards per
// column, so the whole idea→prod pipeline loads in one call (gap-01).
func (s *Server) GetStylePipeline(ctx context.Context, req *pb_admin.GetStylePipelineRequest) (*pb_admin.GetStylePipelineResponse, error) {
	cols, err := s.repo.TechCards().GetStylePipeline(ctx, int(req.GetCardsPerStage()))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get style pipeline", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get style pipeline")
	}
	return dto.ConvertStylePipelineToPb(cols), nil
}
