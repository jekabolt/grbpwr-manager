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

func rejectProtectedNativeCrossRowOverwrite(ctx context.Context, db dependency.DB, patternID int) error {
	_, err := storeutil.QueryNamedOne[struct {
		MediaType string `db:"media_type"`
	}](ctx, db, `
		SELECT media_type
		FROM tech_card_pattern_semantic_sidecar
		WHERE pattern_row_id = :id AND media_type IN (:review, :placement, :whole)
		FOR UPDATE`, map[string]any{
		"id": patternID, "review": entity.PatternProductCompositeSidecarV8MediaType,
		"placement": entity.PatternProductCompositeSidecarV9MediaType,
		"whole":     entity.PatternProductCompositeSidecarV10MediaType,
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("check protected native cross-row carrier: %w", err)
	default:
		return fmt.Errorf("current native cross-row carrier cannot be replaced: %w", entity.ErrTechCardPatternSidecarBindingMoved)
	}
}

// The caller holds the card, anchor row and prior-slot locks. Use the same facts as Complete GET
// so all members, including those other than the storage anchor, are covered by the write CAS.
func loadCurrentNativeCrossRowWrite(
	ctx context.Context, rep dependency.Repository, in entity.PatternSemanticSidecarWrite, pattern savedPatternAuthorityRow, lock int,
) (entity.PatternProductNativeCrossRowCurrentV8, error) {
	var empty entity.PatternProductNativeCrossRowCurrentV8
	db := rep.DB()
	clock, err := readTechCardAssemblyClock(ctx, db, in.TechCardId)
	if err != nil {
		return empty, err
	}
	revision := clock.Token()
	if revision != *in.ExpectedServerAssemblyRevision {
		return empty, fmt.Errorf("assembly changed before native cross-row review: %w", entity.ErrTechCardPatternSidecarBindingMoved)
	}
	if err := validateCompleteSourcePieceInputOwnership(ctx, db, in.TechCardId); err != nil {
		if errors.Is(err, entity.ErrTechCardSavedAssemblyUnsupported) {
			return empty, fmt.Errorf("%v: %w", err, entity.ErrTechCardPatternSidecarBindingMoved)
		}
		return empty, err
	}
	card, err := rep.TechCards().GetTechCardById(ctx, in.TechCardId)
	if err != nil {
		return empty, err
	}
	lines, err := loadRollGoodsLines(ctx, db, in.TechCardId)
	if err != nil {
		return empty, err
	}
	row := entity.SavedPatternRowIdentity{
		TechCardID: in.TechCardId, CardLockVersion: lock, PatternLineKey: pattern.LineKey,
		ScopeKey:         entity.FabricScopeIdentity(pattern.FabricPurpose.String, pattern.BomLineKey.String, lines),
		SavedRowRevision: pattern.SavedRowRevision, PatternVersion: pattern.PatternVersion,
		URL: pattern.URL, SizeBytes: pattern.SizeBytes.Int64, SHA256: pattern.SHA256.String,
	}
	source, err := buildCheckedSavedAssemblyCompleteSourceV1(card, row, lines, revision, *in.ExpectedAssemblyTopologySHA256)
	if errors.Is(err, entity.ErrTechCardSavedAssemblyMoved) || errors.Is(err, entity.ErrTechCardSavedAssemblyUnsupported) {
		return empty, fmt.Errorf("complete source moved before native cross-row review: %w", entity.ErrTechCardPatternSidecarBindingMoved)
	}
	if err != nil {
		return empty, err
	}
	return nativeCrossRowReviewCurrentV8(ctx, db, row, card, source, revision)
}

func nativeCrossRowReviewCurrentV8(
	ctx context.Context, db dependency.DB, row entity.SavedPatternRowIdentity, card *entity.TechCard,
	source *entity.SavedAssemblyCompleteSourceV1, revision string,
) (entity.PatternProductNativeCrossRowCurrentV8, error) {
	current := entity.PatternProductNativeCrossRowCurrentV8{
		AnchorRow: row, Card: card, Source: source, ServerAssemblyRevision: revision,
	}
	sizes, err := readSavedAssemblyCardSizeTokens(ctx, db, card.SizeIds)
	if err != nil {
		return current, fmt.Errorf("read native cross-row review size dictionary: %w", err)
	}
	current.SizeTokensByID = sizes
	return current, nil
}

func readSavedAssemblyCardSizeTokens(ctx context.Context, db dependency.DB, sizeIDs []int) (map[int]string, error) {
	if len(sizeIDs) == 0 {
		return nil, nil
	}
	sizes, err := storeutil.QueryListNamed[struct {
		ID   int    `db:"id"`
		Name string `db:"name"`
	}](ctx, db, `SELECT id, name FROM size WHERE id IN (:ids)`, map[string]any{"ids": sizeIDs})
	if err != nil {
		return nil, err
	}
	tokens := make(map[int]string, len(sizes))
	for _, size := range sizes {
		// The Workbench uses the first dictionary spelling as identity (m_32bo_m → M).
		// A display/fallback alias is not a declared token for this profile.
		token, _, _ := strings.Cut(strings.TrimSpace(size.Name), "_")
		if token != "" {
			tokens[size.ID] = strings.ToUpper(token)
		}
	}
	return tokens, nil
}
