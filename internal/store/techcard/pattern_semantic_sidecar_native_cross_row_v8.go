package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

func rejectNativeCrossRowReviewOverwriteV8(ctx context.Context, db dependency.DB, patternID int) error {
	_, err := storeutil.QueryNamedOne[struct {
		MediaType string `db:"media_type"`
	}](ctx, db, `
		SELECT media_type
		FROM tech_card_pattern_semantic_sidecar
		WHERE pattern_row_id = :id AND media_type = :protected_media
		FOR UPDATE`, map[string]any{"id": patternID, "protected_media": entity.PatternProductCompositeSidecarV8MediaType})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("check protected native cross-row review: %w", err)
	default:
		return fmt.Errorf("current native cross-row review cannot be replaced: %w", entity.ErrTechCardPatternSidecarBindingMoved)
	}
}

// The caller holds the card, anchor row and prior-slot locks. Use the same facts as Complete GET
// so all members, including those other than the storage anchor, are covered by the write CAS.
func validateNativeCrossRowReviewWriteV8(ctx context.Context, rep dependency.Repository, in entity.PatternSemanticSidecarWrite, pattern savedPatternAuthorityRow, lock int) error {
	db := rep.DB()
	clock, err := readTechCardAssemblyClock(ctx, db, in.TechCardId)
	if err != nil {
		return err
	}
	revision := clock.Token()
	if revision != *in.ExpectedServerAssemblyRevision {
		return fmt.Errorf("assembly changed before native cross-row review: %w", entity.ErrTechCardPatternSidecarBindingMoved)
	}
	if err := validateCompleteSourcePieceInputOwnership(ctx, db, in.TechCardId); err != nil {
		if errors.Is(err, entity.ErrTechCardSavedAssemblyUnsupported) {
			return fmt.Errorf("%v: %w", err, entity.ErrTechCardPatternSidecarBindingMoved)
		}
		return err
	}
	card, err := rep.TechCards().GetTechCardById(ctx, in.TechCardId)
	if err != nil {
		return err
	}
	lines, err := loadRollGoodsLines(ctx, db, in.TechCardId)
	if err != nil {
		return err
	}
	row := entity.SavedPatternRowIdentity{
		TechCardID: in.TechCardId, CardLockVersion: lock, PatternLineKey: pattern.LineKey,
		ScopeKey:         entity.FabricScopeIdentity(pattern.FabricPurpose.String, pattern.BomLineKey.String, lines),
		SavedRowRevision: pattern.SavedRowRevision, PatternVersion: pattern.PatternVersion,
		URL: pattern.URL, SizeBytes: pattern.SizeBytes.Int64, SHA256: pattern.SHA256.String,
	}
	source, err := buildCheckedSavedAssemblyCompleteSourceV1(card, row, lines, revision, *in.ExpectedAssemblyTopologySHA256)
	if errors.Is(err, entity.ErrTechCardSavedAssemblyMoved) || errors.Is(err, entity.ErrTechCardSavedAssemblyUnsupported) {
		return fmt.Errorf("complete source moved before native cross-row review: %w", entity.ErrTechCardPatternSidecarBindingMoved)
	}
	if err != nil {
		return err
	}
	current, err := nativeCrossRowReviewCurrentV8(ctx, db, row, card, source, revision)
	if err != nil {
		return err
	}
	return entity.ValidateFirstPatternProductNativeCrossRowReviewV8(in, current)
}

func nativeCrossRowReviewCurrentV8(
	ctx context.Context, db dependency.DB, row entity.SavedPatternRowIdentity, card *entity.TechCard,
	source *entity.SavedAssemblyCompleteSourceV1, revision string,
) (entity.PatternProductNativeCrossRowCurrentV8, error) {
	current := entity.PatternProductNativeCrossRowCurrentV8{
		AnchorRow: row, Card: card, Source: source, ServerAssemblyRevision: revision,
	}
	if len(card.SizeIds) == 0 {
		return current, nil
	}
	sizes, err := storeutil.QueryListNamed[struct {
		ID   int    `db:"id"`
		Name string `db:"name"`
	}](ctx, db, `SELECT id, name FROM size WHERE id IN (:ids)`, map[string]any{"ids": card.SizeIds})
	if err != nil {
		return current, fmt.Errorf("read native cross-row review size dictionary: %w", err)
	}
	current.SizeTokensByID = make(map[int]string, len(sizes))
	for _, size := range sizes {
		// The Workbench uses the first dictionary spelling as identity (m_32bo_m → M).
		// A display/fallback alias is not a declared token for this profile.
		token, _, _ := strings.Cut(strings.TrimSpace(size.Name), "_")
		if token != "" {
			current.SizeTokensByID[size.ID] = strings.ToUpper(token)
		}
	}
	return current, nil
}
