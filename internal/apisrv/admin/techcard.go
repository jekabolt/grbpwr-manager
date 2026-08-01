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
	if _, write := s.costingAccess(ctx); !write && techCardInsertHasCostingData(req.TechCard) {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to set cost data (costing block or BOM prices)")
	}
	tc, err := dto.ConvertPbTechCardInsertToEntity(req.TechCard)
	if err != nil {
		return nil, techCardConvertErr(err)
	}
	if err := validateStyleNumberOverride(tc); err != nil {
		return nil, err
	}
	// Server-stamp the audit trail (norm §2.11); client-sent values are ignored.
	username := authsrv.GetAdminUsername(ctx)
	tc.CreatedBy, tc.UpdatedBy = username, username
	stampFreshTechCardSignoffAudit(tc, req.TechCard.Signoffs, username, time.Now().UTC())
	// A card can be created with sections already approved, and a linked BOM line reads back enriched
	// here exactly as it does on update — so the same correction applies. Nothing mutates the payload
	// between the parse and the write on this path, so the parse-time digests ARE the "as parsed" set.
	if err := s.restampFreshSignoffDigests(ctx, tc, dto.TechCardSectionDigests(tc)); err != nil {
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
			return nil, styleNumberTaken()
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
	return &pb_admin.GetTechCardResponse{TechCard: pbTc}, nil
}

// UpdateTechCard updates a tech card, replacing its nested sections.
func (s *Server) UpdateTechCard(ctx context.Context, req *pb_admin.UpdateTechCardRequest) (*pb_admin.UpdateTechCardResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	_, canWriteCosting := s.costingAccess(ctx)
	if !canWriteCosting && techCardInsertHasCostingData(req.TechCard) {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to modify cost data (costing block or BOM prices)")
	}
	tc, err := dto.ConvertPbTechCardInsertToEntity(req.TechCard)
	if err != nil {
		return nil, techCardConvertErr(err)
	}
	if err := validateStyleNumberOverride(tc); err != nil {
		return nil, err
	}
	username := authsrv.GetAdminUsername(ctx)
	tc.UpdatedBy = username // server-stamp; created_by is preserved (not in SET)
	stampFreshTechCardSignoffAudit(tc, req.TechCard.Signoffs, username, time.Now().UTC())
	// Snapshot the fingerprints of the payload AS PARSED — that is what dto stamped a fresh approval
	// with, and the only way to tell this save's approvals from ones carried back verbatim. Taken
	// before the costing restore below changes the priced sections underneath it.
	asParsed := dto.TechCardSectionDigests(tc)
	// A cost-stripped account's full-replace save must not blank the costing it never saw.
	if !canWriteCosting {
		if err := s.preserveStoredCosting(ctx, int(req.Id), tc); err != nil {
			slog.Default().ErrorContext(ctx, "can't preserve stored tech card costing",
				slog.Int("tech_card_id", int(req.Id)), slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "can't preserve stored costing; try again")
		}
	}
	if err := s.restampFreshSignoffDigests(ctx, tc, asParsed); err != nil {
		slog.Default().ErrorContext(ctx, "can't finalize fresh tech card sign-off digest",
			slog.Int("tech_card_id", int(req.Id)), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't finalize sign-off approval; try again")
	}
	if err := s.repo.TechCards().UpdateTechCard(ctx, int(req.Id), tc, int(req.ExpectedLockVersion)); err != nil {
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
			return nil, status.Error(codes.FailedPrecondition, entity.ErrTechCardPurposeLocked.Error())
		}
		if s.repo.IsErrUniqueViolation(err) {
			return nil, styleNumberTaken()
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, techCardFKMsg)
		}
		slog.Default().ErrorContext(ctx, "can't update tech card",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update tech card")
	}
	s.seedProductCostsFromTechCard(ctx, int(req.Id), int(req.ExpectedLockVersion)+1)
	s.snapshotReleaseIfReleased(ctx, int(req.Id))
	return &pb_admin.UpdateTechCardResponse{}, nil
}

// restampFreshSignoffDigests re-fingerprints the sections THIS save is approving from the content the
// card will PRESENT ON THE NEXT READ, which is not the same thing as the payload that arrived.
//
// dto.ConvertPbTechCardInsertToEntity stamps a fresh approval's signed_digest at parse time — the only
// moment it is certain which sections are being approved (an empty digest on the wire), but too early to
// know the value, for two reasons:
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
// Only the sections stamped by THIS save move. A digest the client carried back verbatim ("I am not
// re-approving, just saving") was computed from the read model, so it does not match asParsed and is left
// exactly where the approver put it. The one case where a carried-back digest does match asParsed is a
// section whose content has not moved since it was approved — pointing that approval at the read-model
// fingerprint of the very same content re-blesses nothing, and it self-heals the rows stamped before
// this correction existed.
func (s *Server) restampFreshSignoffDigests(ctx context.Context, tc *entity.TechCardInsert, asParsed map[entity.TechCardSignoffSection]string) error {
	if tc == nil || len(tc.Signoffs) == 0 {
		return nil
	}
	fresh := make([]*entity.TechCardSignoff, 0, len(tc.Signoffs))
	for i := range tc.Signoffs {
		so := &tc.Signoffs[i]
		if so.State != entity.SignoffStateApproved || !so.SignedDigest.Valid {
			continue
		}
		if so.SignedDigest.String != asParsed[so.Section] {
			continue // carried back verbatim: an older approval, not this save's
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
	final := dto.TechCardSectionDigestsAsRead(tc, identities)
	for _, so := range fresh {
		d := final[so.Section]
		so.SignedDigest = sql.NullString{String: d, Valid: d != ""}
	}
	return nil
}

// stampFreshTechCardSignoffAudit owns the author and timestamp of an approval made by this request.
// The parsed entity's digest is already populated, so freshness must come from the original wire
// intent: APPROVED with an empty incoming digest. Carried-back approvals keep their stored audit
// fields verbatim. parseTechCardSignoffs preserves order and rejects duplicates, so the two slices
// correspond index-for-index after a successful conversion.
func stampFreshTechCardSignoffAudit(tc *entity.TechCardInsert, incoming []*pb_common.TechCardSignoff, username string, now time.Time) {
	if tc == nil {
		return
	}
	for i := range tc.Signoffs {
		if i >= len(incoming) || incoming[i] == nil ||
			tc.Signoffs[i].State != entity.SignoffStateApproved || incoming[i].SignedDigest != "" {
			continue
		}
		tc.Signoffs[i].SignedBy = sql.NullString{String: username, Valid: username != ""}
		tc.Signoffs[i].SignedAt = sql.NullTime{Time: now.UTC(), Valid: true}
	}
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
	if err := protojson.Unmarshal([]byte(rel.Snapshot), &snap); err != nil {
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
	// whose cost is manually set (or run-sourced) is never overwritten — the same provenance
	// predicate SeedProductsCostPriceFromTechCard enforced in SQL.
	fx := s.costingFx(ctx)
	base := cache.GetBaseCurrency()
	rootUnit, rootCcy := dto.ComputeTechCardUnitCost(card, fx)
	var seeded int64
	for _, pid := range linkedProducts {
		unit, currency := dto.ComputeColorwayUnitCost(card, pid, fx)
		if !unit.Valid {
			// Defensive only: a linked product the card's colourway list misses. A colourway
			// with an EMPTY recipe already inherits the style figure inside
			// ComputeColorwayUnitCost, so this fallback is not the per-colourway erasure it
			// looks like.
			unit, currency = rootUnit, rootCcy
		}
		if !unit.Valid || !strings.EqualFold(currency, base) {
			continue
		}
		// Provenance + primary-card ownership are enforced ATOMICALLY in the UPDATE's predicate —
		// a read-then-force here would let a concurrent manual edit or run receipt be overwritten
		// between the read and the write.
		updated, uerr := s.repo.Products().SeedProductCostPriceFromTechCard(ctx, pid, techCardID, unit.Decimal)
		if uerr != nil {
			slog.Default().ErrorContext(ctx, "can't seed product cost_price from tech card",
				slog.Int("tech_card_id", techCardID), slog.Int("product_id", pid), slog.String("err", uerr.Error()))
			continue
		}
		if !updated {
			continue // another card is authoritative, or manual/run provenance wins
		}
		seeded++
		// The COGS decomposition rides the same per-colourway figure (its materials component is
		// THIS colourway's, pins included) under the same predicate, so cost_price and
		// cost_breakdown can never describe two different colourways. A non-convertible breakdown
		// intentionally stays NULL to clear a stale one; a marshal failure must retain the stored value.
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
		if berr := s.repo.Products().SeedProductCostBreakdownFromTechCard(ctx, pid, techCardID, breakdownJSON); berr != nil {
			slog.Default().ErrorContext(ctx, "can't seed product cost_breakdown from tech card",
				slog.Int("tech_card_id", techCardID), slog.Int("product_id", pid), slog.String("err", berr.Error()))
		}
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
	if err := s.repo.TechCards().DeleteTechCard(ctx, int(req.Id)); err != nil {
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
