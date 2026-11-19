package techcard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/model"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	hints "github.com/jekabolt/grbpwr-manager/internal/techcardseamhints"
)

// renameAttachedPieceUnderLock is the name-only branch of UpdateTechCardTx. The
// exact saved pattern rows and attached V10 are untouched so the previous
// placement stays available as an explicit Rebuild predecessor.
func renameAttachedPieceUnderLock(ctx context.Context, rep dependency.Repository, cardID, lock int, actor string, in *entity.GuardedZeroOperationPieceRename) error {
	piece, err := verifyAttachedPieceRename(ctx, rep, cardID, lock, in)
	if err != nil {
		return err
	}
	return writeAttachedPieceRename(ctx, rep.DB(), cardID, lock, piece.Id, actor, in)
}

func writeAttachedPieceRename(ctx context.Context, db dependency.DB, cardID, lock, pieceID int, actor string, in *entity.GuardedZeroOperationPieceRename) error {
	rows, err := storeutil.ExecNamedRows(ctx, db, `
		UPDATE tech_card_piece SET name = :next_name
		WHERE id = :piece AND tech_card_id = :card AND line_key = :line
		      AND CAST(name AS BINARY) = CAST(:expected_name AS BINARY)`, map[string]any{
		"piece": pieceID, "card": cardID, "line": in.PieceLineKey,
		"expected_name": in.ExpectedName, "next_name": in.NextName,
	})
	if err != nil {
		return fmt.Errorf("rename saved piece: %w", err)
	}
	if rows != 1 {
		return entity.ErrTechCardConflict
	}
	rows, err = storeutil.ExecNamedRows(ctx, db, `
		UPDATE tech_card SET lock_version = lock_version + 1, updated_by = :actor
		WHERE id = :card AND lock_version = :lock`, map[string]any{
		"card": cardID, "lock": lock, "actor": actor,
	})
	if err != nil {
		return fmt.Errorf("advance attached piece rename card lock: %w", err)
	}
	if rows != 1 {
		return entity.ErrTechCardConflict
	}
	if err := bumpTechCardAssemblyClock(ctx, db, cardID); err != nil {
		return err
	}
	return appendTechCardRevision(ctx, db, cardID, actor, "pieces", "updated",
		fmt.Sprintf("Renamed saved piece %s from %q to %q", in.PieceLineKey, in.ExpectedName, in.NextName))
}

func verifyAttachedPieceRename(ctx context.Context, rep dependency.Repository, cardID, lock int, in *entity.GuardedZeroOperationPieceRename) (entity.TechCardPiece, error) {
	var empty entity.TechCardPiece
	db := rep.DB()
	authority := in.Attached
	if !entity.ValidTechCardAssemblyRevision(authority.ExpectedServerAssemblyRevision) ||
		!entity.ValidPatternSemanticSidecarSHA256(authority.ExpectedCompleteSourceSHA256) ||
		!entity.ValidPatternSemanticSidecarSHA256(authority.ExpectedCurrentSidecarSHA256) ||
		!entity.ValidTechCardSeamKey(in.PieceLineKey) || !entity.ValidTechCardSeamKey(in.PatternLineKey) ||
		in.SavedRowRevision == 0 || !entity.ValidPatternSemanticSidecarSHA256(in.DxfSHA256) || strings.TrimSpace(in.ExpectedName) == "" ||
		in.NextName == in.ExpectedName || in.NextName == "" || strings.TrimSpace(in.NextName) != in.NextName || len(in.NextName) > 255 {
		return empty, entity.ErrTechCardAttachedPieceRenameUnsupported
	}
	clock, err := readTechCardAssemblyClock(ctx, db, cardID)
	if err != nil {
		return empty, err
	}
	revision := clock.Token()
	if revision != authority.ExpectedServerAssemblyRevision {
		return empty, entity.ErrTechCardSavedAssemblyMoved
	}
	if err := validateCompleteSourcePieceInputOwnership(ctx, db, cardID); err != nil {
		return empty, err
	}
	card, err := rep.TechCards().GetTechCardById(ctx, cardID)
	if err != nil {
		return empty, err
	}
	if card == nil || card.Id != cardID || card.LockVersion != lock {
		return empty, entity.ErrTechCardConflict
	}
	piece, err := entity.GuardedAttachedPieceRenamePiece(card, in)
	if err != nil {
		return empty, err
	}
	lines, err := loadRollGoodsLines(ctx, db, cardID)
	if err != nil {
		return empty, err
	}
	row, err := attachedPieceRenameAnchor(card, lines, in)
	if err != nil {
		return empty, err
	}
	source, err := buildCheckedSavedAssemblyCompleteSourceV1(card, row, lines, revision, authority.ExpectedCompleteSourceSHA256)
	if err != nil {
		return empty, err
	}
	if !attachedPieceRenameSourceOwnsPiece(source, row, piece.LineKey) {
		return empty, entity.ErrTechCardAttachedPieceRenameUnsupported
	}
	if err := verifyAttachedPieceRenameCarrier(ctx, db, card, row, source, revision, authority.ExpectedCurrentSidecarSHA256); err != nil {
		return empty, err
	}
	if err := refuseAttachedPieceRenameHintNames(ctx, db, cardID, in.ExpectedName, in.NextName); err != nil {
		return empty, err
	}
	return piece, nil
}

func attachedPieceRenameAnchor(card *entity.TechCard, lines []entity.RollGoodsLine, in *entity.GuardedZeroOperationPieceRename) (entity.SavedPatternRowIdentity, error) {
	var anchor entity.SavedPatternRowIdentity
	for _, p := range card.Patterns {
		if p.LineKey != in.PatternLineKey {
			continue
		}
		scope := entity.ResolveFabricScope(p.FabricPurpose.String, p.BomLineKey.String, lines)
		if anchor.PatternLineKey != "" || !scope.Live() ||
			entity.FabricScopeIdentity(p.FabricPurpose.String, p.BomLineKey.String, lines) != in.ScopeKey ||
			p.SavedRowRevision != in.SavedRowRevision || !p.SHA256.Valid || p.SHA256.String != in.DxfSHA256 ||
			!p.SizeBytes.Valid || !isDXFPattern(p) {
			return entity.SavedPatternRowIdentity{}, entity.ErrTechCardSavedAssemblyMoved
		}
		anchor = entity.SavedPatternRowIdentity{
			TechCardID: card.Id, CardLockVersion: card.LockVersion, ScopeKey: in.ScopeKey, PatternLineKey: p.LineKey,
			SavedRowRevision: p.SavedRowRevision, PatternVersion: p.Version,
			URL: p.URL, SizeBytes: p.SizeBytes.Int64, SHA256: p.SHA256.String, Filename: p.Filename.String,
		}
	}
	if anchor.PatternLineKey == "" {
		return entity.SavedPatternRowIdentity{}, entity.ErrTechCardSavedAssemblyMoved
	}
	return anchor, nil
}

func attachedPieceRenameSourceOwnsPiece(source *entity.SavedAssemblyCompleteSourceV1, anchor entity.SavedPatternRowIdentity, pieceLineKey string) bool {
	rows, aliases := 0, 0
	for _, row := range source.Rows {
		if row.ScopeKey == anchor.ScopeKey {
			rows++
		}
	}
	for _, alias := range source.Aliases {
		if alias.ScopeKey == anchor.ScopeKey && alias.PieceLineKey == pieceLineKey {
			aliases++
		}
	}
	return rows == 1 && aliases == 1
}

func verifyAttachedPieceRenameCarrier(ctx context.Context, db dependency.DB, card *entity.TechCard, row entity.SavedPatternRowIdentity, source *entity.SavedAssemblyCompleteSourceV1, revision, expectedSHA string) error {
	stored, err := readPatternSemanticSidecar(ctx, db, card.Id, row.PatternLineKey)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.ErrTechCardSavedAssemblyMoved
	}
	if err != nil {
		return err
	}
	if stored.SidecarSHA256 != expectedSHA {
		return entity.ErrTechCardSavedAssemblyMoved
	}
	if stored.MediaType != entity.PatternProductCompositeSidecarV10MediaType {
		return entity.ErrTechCardAttachedPieceRenameUnsupported
	}
	attachments, err := storeutil.QueryCountNamed(ctx, db, `
		SELECT COUNT(*) FROM tech_card_pattern_semantic_sidecar s
		JOIN tech_card_size_pattern p ON p.id = s.pattern_row_id WHERE p.tech_card_id = :card`, map[string]any{"card": card.Id})
	if err != nil {
		return err
	}
	if attachments != 1 {
		return entity.ErrTechCardAttachedPieceRenameUnsupported
	}
	current, err := nativeCrossRowReviewCurrentV8(ctx, db, row, card, source, revision)
	if err != nil {
		return err
	}
	binding, err := entity.ReadPatternProductNativeWholePlacementBindingV10(stored)
	if err != nil {
		return err
	}
	lockedModel, err := model.LockNeutralPlacementModelAuthorityV1(ctx, db, binding.ModelID)
	if err != nil {
		err = nativeCrossRowPlacementModelError(err)
	} else {
		err = entity.ValidateStoredPatternProductNativeWholePlacementV10(stored, current, lockedModel)
	}
	if errors.Is(err, entity.ErrTechCardPatternSidecarBindingMoved) {
		return fmt.Errorf("saved V10 is not current: %w", entity.ErrTechCardSavedAssemblyMoved)
	}
	return err
}

func refuseAttachedPieceRenameHintNames(ctx context.Context, db dependency.DB, cardID int, previous, next string) error {
	overrides, err := storeutil.QueryListNamed[struct {
		Kind  string         `db:"kind"`
		Key   string         `db:"hint_key"`
		Value sql.NullString `db:"value"`
	}](ctx, db, `SELECT kind, hint_key, value FROM tech_card_seam_hint_override
		WHERE tech_card_id = :card FOR UPDATE`, map[string]any{"card": cardID})
	if err != nil {
		return fmt.Errorf("check name-bound seam hints: %w", err)
	}
	matches := func(name string) bool {
		return strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(previous)) ||
			strings.EqualFold(strings.TrimSpace(name), next)
	}
	for _, override := range overrides {
		if override.Kind == "role" && matches(override.Key) {
			return fmt.Errorf("the piece name is used in an authored seam hint: %w", entity.ErrTechCardAttachedPieceRenameUnsupported)
		}
		if override.Kind != "join" || !override.Value.Valid {
			continue
		}
		var join hints.JoinValue
		if err := json.Unmarshal([]byte(override.Value.String), &join); err != nil {
			return fmt.Errorf("could not prove the stored seam hint's piece names: %w", entity.ErrTechCardAttachedPieceRenameUnsupported)
		}
		for _, names := range [][]string{join.A.Pieces, join.B.Pieces, join.Caught} {
			for _, name := range names {
				if matches(name) {
					return fmt.Errorf("the piece name is used in an authored seam hint: %w", entity.ErrTechCardAttachedPieceRenameUnsupported)
				}
			}
		}
	}
	return nil
}
