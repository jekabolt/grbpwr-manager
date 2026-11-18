package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type patternSemanticSidecarRow struct {
	entity.PatternSemanticSidecar
	CurrentDxfSHA256        string         `db:"current_dxf_sha256"`
	CurrentPatternVersion   int            `db:"current_pattern_version"`
	CurrentSavedRowRevision uint32         `db:"current_saved_row_revision"`
	CurrentCardLockVersion  int            `db:"current_card_lock_version"`
	CurrentURL              string         `db:"current_url"`
	CurrentSizeBytes        sql.NullInt64  `db:"current_size_bytes"`
	CurrentFabricPurpose    sql.NullString `db:"current_fabric_purpose"`
	CurrentBomLineKey       sql.NullString `db:"current_bom_line_key"`
}

type priorPatternSemanticSidecar struct {
	CardLockVersion       int    `db:"card_lock_version"`
	SavedRowRevision      uint32 `db:"saved_row_revision"`
	DxfSHA256             string `db:"dxf_sha256"`
	PatternVersion        int    `db:"pattern_version"`
	SHA256                string `db:"sidecar_sha256"`
	MediaType             string `db:"media_type"`
	ByteLength            int    `db:"byte_length"`
	Payload               []byte `db:"payload"`
	PostSaveReceiptSHA256 string `db:"post_save_receipt_sha256"`
}

// PutTechCardPatternSemanticSidecar atomically attaches already-built G4F4 bytes to the exact DXF
// revision G4F3 reread. The pattern row and card are locked before comparison; a concurrent save
// therefore either wins first and makes this CAS fail, or waits until these bytes have committed.
func (s *Store) PutTechCardPatternSemanticSidecar(ctx context.Context, in entity.PatternSemanticSidecarWrite) (entity.PatternSemanticSidecar, error) {
	if err := entity.ValidatePatternSemanticSidecarWrite(&in); err != nil {
		return entity.PatternSemanticSidecar{}, err
	}
	expectedCardLockVersion := *in.ExpectedCardLockVersion
	// Validation reads the caller's slice. Own it before the transaction and re-hash the owned copy,
	// so a caller cannot mutate bytes between the validation and INSERT. The scalar lock token above
	// likewise owns the pointed-to request value before the transaction starts.
	in.Payload = append([]byte(nil), in.Payload...)
	if actual := entity.PatternSemanticSidecarPayloadSHA256(in.Payload); actual != in.SidecarSHA256 {
		return entity.PatternSemanticSidecar{}, entity.NewFieldViolation(
			"sidecar_sha256", "digest_mismatch", in.SidecarSHA256, "hash the exact sidecar_bytes")
	}

	var out entity.PatternSemanticSidecar
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		cardLockVersion, err := lockTechCardRowVersion(ctx, db, in.TechCardId)
		if err != nil {
			return err
		}
		if cardLockVersion != expectedCardLockVersion {
			return fmt.Errorf("tech card lock_version %d does not match expected %d: %w",
				cardLockVersion, expectedCardLockVersion, entity.ErrTechCardPatternSidecarBindingMoved)
		}
		if err := storeutil.RequireMutableTechCard(ctx, db, in.TechCardId); err != nil {
			return err
		}
		pattern, err := storeutil.QueryNamedOne[struct {
			Id               int            `db:"id"`
			SHA256           string         `db:"sha256"`
			Version          int            `db:"version"`
			SavedRowRevision uint32         `db:"saved_row_revision"`
			FabricPurpose    sql.NullString `db:"fabric_purpose"`
			BomLineKey       sql.NullString `db:"bom_line_key"`
			URL              string         `db:"url"`
			SizeBytes        sql.NullInt64  `db:"size_bytes"`
		}](ctx, db, `
			SELECT id, COALESCE(sha256, '') AS sha256, version, saved_row_revision,
			       fabric_purpose, bom_line_key, url, size_bytes
			FROM tech_card_size_pattern
			WHERE tech_card_id = :tech_card_id AND line_key = :line_key
			FOR UPDATE`, map[string]any{
			"tech_card_id": in.TechCardId,
			"line_key":     in.PatternLineKey,
		})
		if err != nil {
			return fmt.Errorf("load current pattern row for semantic sidecar: %w", err)
		}
		if pattern.SHA256 != in.ExpectedDxfSHA256 || pattern.Version != in.ExpectedPatternVersion ||
			(in.ExpectedSavedRowRevision != nil && pattern.SavedRowRevision != *in.ExpectedSavedRowRevision) {
			return fmt.Errorf("pattern line %s: %w", in.PatternLineKey, entity.ErrTechCardPatternSidecarBindingMoved)
		}
		var prior *priorPatternSemanticSidecar
		if in.ExpectedPreviousSidecarSHA256 != nil {
			storedPrior, priorErr := storeutil.QueryNamedOne[priorPatternSemanticSidecar](ctx, db,
				`SELECT card_lock_version, saved_row_revision, dxf_sha256, pattern_version,
				        sidecar_sha256, media_type, byte_length, payload, post_save_receipt_sha256
				 FROM tech_card_pattern_semantic_sidecar WHERE pattern_row_id = :id FOR UPDATE`,
				map[string]any{"id": pattern.Id})
			expected := *in.ExpectedPreviousSidecarSHA256
			switch {
			case errors.Is(priorErr, sql.ErrNoRows) && expected == "":
			case priorErr == nil && storedPrior.SHA256 == expected:
				prior = &storedPrior
			case priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows):
				return fmt.Errorf("read prior semantic sidecar CAS: %w", priorErr)
			default:
				return fmt.Errorf("prior semantic sidecar changed: %w", entity.ErrTechCardPatternSidecarBindingMoved)
			}
		}
		if prior != nil && isNativeCrossRowCarrier(prior.MediaType) &&
			in.MediaType != entity.PatternProductCompositeSidecarV10MediaType &&
			(in.MediaType != entity.PatternProductCompositeSidecarV9MediaType || prior.MediaType == entity.PatternProductCompositeSidecarV10MediaType) {
			return fmt.Errorf("current native cross-row carrier cannot be replaced: %w", entity.ErrTechCardPatternSidecarBindingMoved)
		}
		if in.ExpectedPreviousSidecarSHA256 == nil {
			// Legacy writers may omit their prior digest. Protect the native
			// media under the card and row lock without changing legacy CAS.
			if err := rejectProtectedNativeCrossRowOverwrite(ctx, db, pattern.Id); err != nil {
				return err
			}
		}
		if isNativeCrossRowCarrier(in.MediaType) {
			current, err := loadCurrentNativeCrossRowWrite(ctx, rep, in, savedPatternAuthorityRow{
				LineKey: in.PatternLineKey, FabricPurpose: pattern.FabricPurpose, BomLineKey: pattern.BomLineKey,
				URL: pattern.URL, SizeBytes: pattern.SizeBytes, SHA256: sql.NullString{String: pattern.SHA256, Valid: true},
				PatternVersion: pattern.Version, SavedRowRevision: pattern.SavedRowRevision,
			}, cardLockVersion)
			if err != nil {
				return err
			}
			if in.MediaType == entity.PatternProductCompositeSidecarV8MediaType {
				err = entity.ValidateFirstPatternProductNativeCrossRowReviewV8(in, current)
			} else {
				err = validateNativePlacementWrite(ctx, db, in, prior, current)
			}
			if err != nil {
				return err
			}
		} else if isPatternProductCompositeSidecarMediaType(in.MediaType) {
			clock, err := readTechCardAssemblyClock(ctx, db, in.TechCardId)
			if err != nil {
				return err
			}
			card, err := rep.TechCards().GetTechCardById(ctx, in.TechCardId)
			if err != nil {
				return err
			}
			selected, err := selectSavedAssemblySeamsForCompositeMedia(card, in.SelectedOperationNumber, in.MediaType)
			if err != nil {
				return err
			}
			_, topologySHA, err := savedAssemblyTopology(card.Operations, selected, in.SelectedOperationNumber)
			if err != nil {
				return err
			}
			if clock.Token() != *in.ExpectedServerAssemblyRevision || topologySHA != *in.ExpectedAssemblyTopologySHA256 {
				return fmt.Errorf("assembly changed before composite sidecar write: %w", entity.ErrTechCardPatternSidecarBindingMoved)
			}
			lines, err := loadRollGoodsLines(ctx, db, in.TechCardId)
			if err != nil {
				return err
			}
			scope := entity.ResolveFabricScope(pattern.FabricPurpose.String, pattern.BomLineKey.String, lines)
			if !scope.Live() || !pattern.SizeBytes.Valid || pattern.SizeBytes.Int64 <= 0 {
				return fmt.Errorf("pattern line %s has no exact saved-row scope/size: %w",
					in.PatternLineKey, entity.ErrTechCardPatternSidecarBindingMoved)
			}
			currentSavedRow := entity.SavedPatternRowIdentity{
				TechCardID: in.TechCardId, ScopeKey: scope.Key, PatternLineKey: in.PatternLineKey,
				SavedRowRevision: pattern.SavedRowRevision, CardLockVersion: cardLockVersion,
				URL: pattern.URL, SizeBytes: pattern.SizeBytes.Int64, SHA256: pattern.SHA256,
			}
			var assemblyOrderContentSHA string
			if in.MediaType == entity.PatternProductCompositeSidecarV7MediaType {
				_, assemblyOrderContentSHA, err = savedFullAssemblyAOContent(currentSavedRow, clock.Token(), card)
				if err != nil {
					return err
				}
			}
			if err := validatePatternProductCompositeSidecarBinding(in.MediaType, in.Payload,
				entity.PatternProductCompositeSidecarExpected{
					Row:            currentSavedRow,
					PatternVersion: pattern.Version, PostSaveReceiptSHA256: in.ExpectedPostSaveReceiptSHA256,
					ExpectedPreviousSidecarSHA256: *in.ExpectedPreviousSidecarSHA256,
					ServerAssemblyRevision:        clock.Token(),
					AssemblyTopologySHA256:        topologySHA,
					AssemblyOrderContentSHA256:    assemblyOrderContentSHA,
					SelectedOperationNumber:       in.SelectedOperationNumber,
				}); err != nil {
				return err
			}
			if in.MediaType == entity.PatternProductCompositeSidecarV4MediaType && prior != nil {
				if err := entity.ValidatePatternProductCompositeSidecarV4Transition(in.Payload, prior.MediaType, prior.Payload); err != nil {
					return err
				}
			}
			if in.MediaType == entity.PatternProductCompositeSidecarV5MediaType {
				if prior == nil {
					return entity.NewFieldViolation("semantic_sidecar", "composite_v5_prior_required", "", "persist and read back Composite V4 before writing Composite V5")
				}
				if err := validatePriorCompositeSidecarForTransition(*prior, currentSavedRow, pattern.Version); err != nil {
					return err
				}
				if err := entity.ValidatePatternProductCompositeSidecarV5Transition(in.Payload, prior.MediaType, prior.Payload); err != nil {
					return err
				}
			}
			if in.MediaType == entity.PatternProductCompositeSidecarV6MediaType {
				if prior == nil {
					return entity.NewFieldViolation("semantic_sidecar", "composite_v6_prior_required", "", "persist and read back Composite V5 before writing Composite V6")
				}
				if err := validatePriorCompositeSidecarForTransition(*prior, currentSavedRow, pattern.Version); err != nil {
					return err
				}
				if err := entity.ValidatePatternProductCompositeSidecarV6Transition(in.Payload, prior.MediaType, prior.Payload); err != nil {
					return err
				}
			}
			if in.MediaType == entity.PatternProductCompositeSidecarV7MediaType {
				if prior == nil {
					return entity.NewFieldViolation("semantic_sidecar", "composite_v7_prior_required", "", "persist and read back Composite V6 before writing Composite V7")
				}
				if err := validatePriorCompositeSidecarForTransition(*prior, currentSavedRow, pattern.Version); err != nil {
					return err
				}
				if err := entity.ValidatePatternProductCompositeSidecarV7Transition(in.Payload, prior.MediaType, prior.Payload); err != nil {
					return err
				}
			}
		}
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO tech_card_pattern_semantic_sidecar
				(pattern_row_id, card_lock_version, saved_row_revision, dxf_sha256, pattern_version, sidecar_sha256, media_type,
				 byte_length, payload, post_save_receipt_sha256, created_by)
			VALUES
				(:pattern_row_id, :card_lock_version, :saved_row_revision, :dxf_sha256, :pattern_version, :sidecar_sha256, :media_type,
				 :byte_length, :payload, :post_save_receipt_sha256, :created_by)
			ON DUPLICATE KEY UPDATE
				card_lock_version = VALUES(card_lock_version), saved_row_revision = VALUES(saved_row_revision), dxf_sha256 = VALUES(dxf_sha256),
				pattern_version = VALUES(pattern_version),
				sidecar_sha256 = VALUES(sidecar_sha256), media_type = VALUES(media_type),
				byte_length = VALUES(byte_length), payload = VALUES(payload),
				post_save_receipt_sha256 = VALUES(post_save_receipt_sha256),
				created_at = CURRENT_TIMESTAMP(6), created_by = VALUES(created_by)`, map[string]any{
			"pattern_row_id":           pattern.Id,
			"card_lock_version":        cardLockVersion,
			"saved_row_revision":       pattern.SavedRowRevision,
			"dxf_sha256":               in.ExpectedDxfSHA256,
			"pattern_version":          in.ExpectedPatternVersion,
			"sidecar_sha256":           in.SidecarSHA256,
			"media_type":               in.MediaType,
			"byte_length":              len(in.Payload),
			"payload":                  in.Payload,
			"post_save_receipt_sha256": in.ExpectedPostSaveReceiptSHA256,
			"created_by":               in.By,
		}); err != nil {
			return fmt.Errorf("store pattern semantic sidecar: %w", err)
		}
		stored, err := readPatternSemanticSidecar(ctx, db, in.TechCardId, in.PatternLineKey)
		if err != nil {
			return err
		}
		out = stored
		return nil
	})
	return out, err
}

// GetTechCardPatternSemanticSidecar returns sql.ErrNoRows when the current pattern has no attached
// sidecar. A stored row whose metadata/bytes no longer match the active DXF is corruption, not a
// missing result, and fails explicitly.
func (s *Store) GetTechCardPatternSemanticSidecar(ctx context.Context, techCardID int, patternLineKey string) (entity.PatternSemanticSidecar, error) {
	patternLineKey = entity.NormalizeTechCardSeamKey(patternLineKey)
	if techCardID <= 0 {
		return entity.PatternSemanticSidecar{}, entity.NewFieldViolation("tech_card_id", "required", "", "")
	}
	if !entity.ValidTechCardSeamKey(patternLineKey) {
		return entity.PatternSemanticSidecar{}, entity.NewFieldViolation("pattern_line_key", "invalid", patternLineKey, "send the current pattern row's 26-char ULID")
	}
	return readPatternSemanticSidecar(ctx, s.DB, techCardID, patternLineKey)
}

func readPatternSemanticSidecar(ctx context.Context, db dependency.DB, techCardID int, patternLineKey string) (entity.PatternSemanticSidecar, error) {
	row, err := storeutil.QueryNamedOne[patternSemanticSidecarRow](ctx, db, `
		SELECT s.pattern_row_id, p.tech_card_id, COALESCE(p.line_key, '') AS pattern_line_key,
		       s.card_lock_version, s.saved_row_revision,
		       s.dxf_sha256, s.pattern_version, s.sidecar_sha256, s.media_type, s.byte_length,
		       s.payload, s.post_save_receipt_sha256, s.created_at, s.created_by,
		       COALESCE(p.sha256, '') AS current_dxf_sha256, p.version AS current_pattern_version,
		       p.saved_row_revision AS current_saved_row_revision,
		       card.lock_version AS current_card_lock_version, p.url AS current_url,
		       p.size_bytes AS current_size_bytes, p.fabric_purpose AS current_fabric_purpose,
		       p.bom_line_key AS current_bom_line_key
		FROM tech_card_size_pattern p
		JOIN tech_card card ON card.id = p.tech_card_id
		JOIN tech_card_pattern_semantic_sidecar s ON s.pattern_row_id = p.id
		WHERE p.tech_card_id = :tech_card_id AND p.line_key = :line_key`, map[string]any{
		"tech_card_id": techCardID,
		"line_key":     patternLineKey,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity.PatternSemanticSidecar{}, sql.ErrNoRows
		}
		return entity.PatternSemanticSidecar{}, fmt.Errorf("load pattern semantic sidecar: %w", err)
	}
	if err := verifyPatternSemanticSidecarRow(row); err != nil {
		return entity.PatternSemanticSidecar{}, err
	}
	if isPatternProductCompositeSidecarMediaType(row.MediaType) {
		lines, err := loadRollGoodsLines(ctx, db, row.TechCardId)
		if err != nil {
			return entity.PatternSemanticSidecar{}, err
		}
		scope := entity.ResolveFabricScope(row.CurrentFabricPurpose.String, row.CurrentBomLineKey.String, lines)
		if err := verifyStoredCompositePatternSemanticSidecar(row, scope); err != nil {
			return entity.PatternSemanticSidecar{}, err
		}
	}
	row.Payload = append([]byte(nil), row.Payload...)
	return row.PatternSemanticSidecar, nil
}

// verifyStoredCompositePatternSemanticSidecar proves that the carrier still describes the same
// pattern row, bytes and material scope it was written for. The card's live lock_version is not
// part of this corruption check: an Assembly-only edit legitimately moves that version while the
// old carrier remains readable. Its persisted CardLockVersion stays in the returned sidecar, while
// GetTechCardSavedAssemblySnapshot returns the new row/Assembly receipts; the client therefore
// rejects the old carrier as stale and rebuilds it through the normal CAS path.
func verifyStoredCompositePatternSemanticSidecar(row patternSemanticSidecarRow, scope entity.FabricScope) error {
	corrupt := func(reason string) error {
		return fmt.Errorf("pattern semantic sidecar row %d %s: %w",
			row.PatternRowId, reason, entity.ErrTechCardPatternSidecarCorrupt)
	}
	if !scope.Live() || !row.CurrentSizeBytes.Valid || row.CurrentSizeBytes.Int64 <= 0 {
		return corrupt("no longer matches its exact saved row")
	}
	if err := validateStoredPatternProductCompositeSidecarExact(row.MediaType, row.Payload,
		entity.SavedPatternRowIdentity{
			TechCardID: row.TechCardId, ScopeKey: scope.Key, PatternLineKey: row.PatternLineKey,
			SavedRowRevision: row.CurrentSavedRowRevision,
			// Validate against the lock version persisted with the carrier. A newer live card lock is
			// staleness evidence for the caller, not evidence that these stored bytes are corrupt.
			CardLockVersion: row.CardLockVersion,
			URL:             row.CurrentURL,
			SizeBytes:       row.CurrentSizeBytes.Int64,
			SHA256:          row.CurrentDxfSHA256,
		}, row.CurrentPatternVersion, row.PostSaveReceiptSHA256); err != nil {
		return corrupt("exact saved-row binding mismatch")
	}
	return nil
}

func isPatternProductCompositeSidecarMediaType(mediaType string) bool {
	return mediaType == entity.PatternProductCompositeSidecarMediaType ||
		mediaType == entity.PatternProductCompositeSidecarV4MediaType ||
		mediaType == entity.PatternProductCompositeSidecarV5MediaType ||
		mediaType == entity.PatternProductCompositeSidecarV6MediaType ||
		mediaType == entity.PatternProductCompositeSidecarV7MediaType
}

func selectSavedAssemblySeamsForCompositeMedia(card *entity.TechCard, selectedOperationNumber int, mediaType string) ([]entity.TechCardSeam, error) {
	switch mediaType {
	case entity.PatternProductCompositeSidecarMediaType:
		return selectSavedAssemblySeamsV3(card, selectedOperationNumber)
	case entity.PatternProductCompositeSidecarV4MediaType:
		return selectSavedAssemblySeams(card, selectedOperationNumber)
	case entity.PatternProductCompositeSidecarV5MediaType:
		return selectSavedAssemblySeams(card, selectedOperationNumber)
	case entity.PatternProductCompositeSidecarV6MediaType:
		return selectSavedAssemblySeams(card, selectedOperationNumber)
	case entity.PatternProductCompositeSidecarV7MediaType:
		return selectSavedAssemblySeams(card, selectedOperationNumber)
	default:
		return nil, fmt.Errorf("unsupported composite sidecar media type %q: %w",
			mediaType, entity.ErrTechCardSavedAssemblyUnsupported)
	}
}

func validatePatternProductCompositeSidecarBinding(mediaType string, payload []byte, expected entity.PatternProductCompositeSidecarExpected) error {
	switch mediaType {
	case entity.PatternProductCompositeSidecarMediaType:
		return entity.ValidatePatternProductCompositeSidecarBinding(payload, expected)
	case entity.PatternProductCompositeSidecarV4MediaType:
		return entity.ValidatePatternProductCompositeSidecarV4Binding(payload, expected)
	case entity.PatternProductCompositeSidecarV5MediaType:
		return entity.ValidatePatternProductCompositeSidecarV5Binding(payload, expected)
	case entity.PatternProductCompositeSidecarV6MediaType:
		return entity.ValidatePatternProductCompositeSidecarV6Binding(payload, expected)
	case entity.PatternProductCompositeSidecarV7MediaType:
		return entity.ValidatePatternProductCompositeSidecarV7Binding(payload, expected)
	default:
		return entity.NewFieldViolation("media_type", "unsupported", mediaType, "send a supported composite sidecar media type")
	}
}

func validateStoredPatternProductCompositeSidecarExact(mediaType string, payload []byte, row entity.SavedPatternRowIdentity, patternVersion int, receiptSHA string) error {
	switch mediaType {
	case entity.PatternProductCompositeSidecarMediaType:
		return entity.ValidateStoredPatternProductCompositeSidecarExact(payload, row, patternVersion, receiptSHA)
	case entity.PatternProductCompositeSidecarV4MediaType:
		return entity.ValidateStoredPatternProductCompositeSidecarV4Exact(payload, row, patternVersion, receiptSHA)
	case entity.PatternProductCompositeSidecarV5MediaType:
		return entity.ValidateStoredPatternProductCompositeSidecarV5Exact(payload, row, patternVersion, receiptSHA)
	case entity.PatternProductCompositeSidecarV6MediaType:
		return entity.ValidateStoredPatternProductCompositeSidecarV6Exact(payload, row, patternVersion, receiptSHA)
	case entity.PatternProductCompositeSidecarV7MediaType:
		return entity.ValidateStoredPatternProductCompositeSidecarV7Exact(payload, row, patternVersion, receiptSHA)
	default:
		return entity.NewFieldViolation("media_type", "unsupported", mediaType, "send a supported composite sidecar media type")
	}
}

// validatePriorCompositeSidecarForTransition re-proves the current stored carrier before its bytes
// become transition authority. The CAS digest alone is not enough: an out-of-band metadata or blob
// corruption must fail closed instead of being preserved into the next V5/V6/V7 carrier.
func validatePriorCompositeSidecarForTransition(prior priorPatternSemanticSidecar, currentRow entity.SavedPatternRowIdentity, currentPatternVersion int) error {
	corrupt := func(reason string) error {
		return fmt.Errorf("prior pattern semantic sidecar %s: %w", reason, entity.ErrTechCardPatternSidecarCorrupt)
	}
	if !isPatternProductCompositeSidecarMediaType(prior.MediaType) ||
		!entity.ValidPatternSemanticSidecarSHA256(prior.SHA256) ||
		!entity.ValidPatternSemanticSidecarSHA256(prior.DxfSHA256) ||
		!entity.ValidPatternSemanticSidecarSHA256(prior.PostSaveReceiptSHA256) {
		return corrupt("has invalid media or digest metadata")
	}
	if prior.ByteLength <= 0 || prior.ByteLength > entity.PatternSemanticSidecarMaxBytes ||
		prior.ByteLength != len(prior.Payload) || entity.PatternSemanticSidecarPayloadSHA256(prior.Payload) != prior.SHA256 {
		return corrupt("payload length or digest mismatch")
	}
	if prior.SavedRowRevision == 0 || prior.SavedRowRevision != currentRow.SavedRowRevision ||
		prior.DxfSHA256 != currentRow.SHA256 || prior.PatternVersion != currentPatternVersion {
		return corrupt("does not match the current saved pattern row")
	}
	priorRow := currentRow
	priorRow.CardLockVersion = prior.CardLockVersion
	if err := validateStoredPatternProductCompositeSidecarExact(
		prior.MediaType, prior.Payload, priorRow, prior.PatternVersion, prior.PostSaveReceiptSHA256); err != nil {
		return corrupt("exact stored-row or nested component validation failed")
	}
	return nil
}

func verifyPatternSemanticSidecarRow(row patternSemanticSidecarRow) error {
	corrupt := func(reason string) error {
		return fmt.Errorf("pattern semantic sidecar row %d %s: %w", row.PatternRowId, reason, entity.ErrTechCardPatternSidecarCorrupt)
	}
	if row.CurrentDxfSHA256 != row.DxfSHA256 || row.CurrentPatternVersion != row.PatternVersion {
		return corrupt("does not match the active pattern revision")
	}
	if row.CurrentSavedRowRevision != row.SavedRowRevision || row.SavedRowRevision == 0 {
		return corrupt("does not match the active saved row revision")
	}
	if !entity.ValidPatternSemanticSidecarMediaType(row.MediaType) {
		return corrupt("has an unsupported media type")
	}
	if row.ByteLength <= 0 || row.ByteLength > entity.PatternSemanticSidecarMaxBytes || row.ByteLength != len(row.Payload) {
		return corrupt("has inconsistent byte length")
	}
	if entity.PatternSemanticSidecarPayloadSHA256(row.Payload) != row.SidecarSHA256 {
		return corrupt("payload digest mismatch")
	}
	if !entity.ValidPatternSemanticSidecarSHA256(row.DxfSHA256) ||
		!entity.ValidPatternSemanticSidecarSHA256(row.SidecarSHA256) ||
		!entity.ValidPatternSemanticSidecarSHA256(row.PostSaveReceiptSHA256) {
		return corrupt("has malformed digest metadata")
	}
	if err := entity.ValidatePatternSemanticSidecarPayloadBinding(
		row.Payload,
		row.TechCardId,
		row.PatternLineKey,
		row.CardLockVersion,
		row.DxfSHA256,
		row.PatternVersion,
		row.SavedRowRevision,
		row.PostSaveReceiptSHA256,
		row.MediaType,
	); err != nil {
		return corrupt("payload binding mismatch")
	}
	return nil
}

// invalidatePatternSemanticSidecarsForDocumentRewrite runs inside UpdateTechCardTx before any
// pattern row is rewritten. G4F4 binds more than the DXF blob: its receipt also contains scope,
// size, piece-line identities and block aliases. The beta table deliberately does not duplicate
// that whole graph, so the only honest invalidation rule is conservative: every full card rewrite
// drops every attachment and the browser may rebuild it after the save. A future server-owned graph
// fingerprint can narrow this without weakening the receipt.
func invalidatePatternSemanticSidecarsForDocumentRewrite(ctx context.Context, db dependency.DB, techCardID int) error {
	if err := storeutil.ExecNamed(ctx, db, `
		DELETE sidecar
		FROM tech_card_pattern_semantic_sidecar sidecar
		JOIN tech_card_size_pattern pattern ON pattern.id = sidecar.pattern_row_id
		WHERE pattern.tech_card_id = :tech_card_id`, map[string]any{"tech_card_id": techCardID}); err != nil {
		return fmt.Errorf("invalidate pattern semantic sidecars for card %d rewrite: %w", techCardID, err)
	}
	return nil
}
