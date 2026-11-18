package techcard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	savedAssemblyCompleteSourceDomainV1 = "tech-card-saved-assembly-complete-source/1"
	savedAssemblyMaxSourceRows          = 512
	savedAssemblyMaxSourceAliases       = 8192
	savedAssemblyMaxSourceOperations    = 4096
	savedAssemblyMaxSourceBytes         = 8 << 20
	savedAssemblyMaxSourceURLBytes      = 4096
	savedAssemblyMaxJSSafeInteger       = 1<<53 - 1
)

// The card loader omits typed piece inputs whose stored piece line is NULL. Inspect those
// relations before hydration so the exact profile cannot attest to a silently incomplete AO.
// As in the loader, a legacy piece relation applies only when the operation has no typed inputs.
func validateCompleteSourcePieceInputOwnership(ctx context.Context, db dependency.DB, cardID int) error {
	result, err := storeutil.QueryNamedOne[struct {
		Invalid bool `db:"invalid"`
	}](ctx, db, `
		SELECT EXISTS (
			SELECT 1
			FROM tech_card_operation o
			JOIN tech_card_operation_input i ON i.operation_id = o.id
			LEFT JOIN tech_card_piece p ON p.id = i.piece_id AND p.tech_card_id = o.tech_card_id
			WHERE o.tech_card_id = :card AND i.piece_id IS NOT NULL AND p.line_key IS NULL
		) OR EXISTS (
			SELECT 1
			FROM tech_card_operation o
			JOIN tech_card_operation_piece i ON i.operation_id = o.id
			LEFT JOIN tech_card_piece p ON p.id = i.piece_id AND p.tech_card_id = o.tech_card_id
			WHERE o.tech_card_id = :card AND p.line_key IS NULL AND NOT EXISTS (
				SELECT 1 FROM tech_card_operation_input typed WHERE typed.operation_id = o.id
			)
		) AS invalid`, map[string]any{"card": cardID})
	if err != nil {
		return fmt.Errorf("validate stored Assembly piece inputs: %w", err)
	}
	if result.Invalid {
		return fmt.Errorf("stored Assembly piece input has no local piece line: %w", entity.ErrTechCardSavedAssemblyUnsupported)
	}
	return nil
}

// buildCheckedSavedAssemblyCompleteSourceV1 hands out only a complete projection that still
// matches the caller's pinned source, even when the requested anchor row has not changed.
func buildCheckedSavedAssemblyCompleteSourceV1(
	card *entity.TechCard,
	anchor entity.SavedPatternRowIdentity,
	lines []entity.RollGoodsLine,
	revision string,
	expectedSourceSHA256 string,
) (*entity.SavedAssemblyCompleteSourceV1, error) {
	source, err := buildSavedAssemblyCompleteSourceV1(card, anchor, lines, revision)
	if err != nil {
		return nil, err
	}
	if expectedSourceSHA256 != "" && source.SourceSHA256 != expectedSourceSHA256 {
		return nil, fmt.Errorf("complete Assembly source changed: %w", entity.ErrTechCardSavedAssemblyMoved)
	}
	return source, nil
}

// buildSavedAssemblyCompleteSourceV1 is intentionally separate from the strict selected-operation
// read. It commits stored source metadata, including unanchored, rejected or stale rows as such.
func buildSavedAssemblyCompleteSourceV1(
	card *entity.TechCard,
	anchor entity.SavedPatternRowIdentity,
	lines []entity.RollGoodsLine,
	revision string,
) (*entity.SavedAssemblyCompleteSourceV1, error) {
	if !validCompleteSourceAssemblyRevision(revision) {
		return nil, fmt.Errorf("complete Assembly source has an invalid clock: %w", entity.ErrTechCardSavedAssemblyUnsupported)
	}
	if len(card.Patterns) > savedAssemblyMaxSourceRows ||
		len(card.GarmentLabels) > savedAssemblyMaxSourceRows ||
		len(card.PieceDxfAliases) > savedAssemblyMaxSourceAliases ||
		len(card.Operations) > savedAssemblyMaxSourceOperations ||
		len(card.Seams) > entity.TechCardSeamMaxRowsPerCard {
		return nil, fmt.Errorf("complete Assembly source exceeds its row bounds: %w", entity.ErrTechCardSavedAssemblyUnsupported)
	}
	rows, scopes, err := completeSourceRows(card, anchor, lines)
	if err != nil {
		return nil, err
	}
	aliases, pieceScopes, err := completeSourceAliases(card, scopes, lines)
	if err != nil {
		return nil, err
	}
	labels, err := completeSourceCardLabels(card)
	if err != nil {
		return nil, err
	}
	seams, err := completeSourceOwnedSeams(card, pieceScopes, labels)
	if err != nil {
		return nil, err
	}
	usedLabels, err := completeSourceReferencedLabels(card.Operations, seams, labels)
	if err != nil {
		return nil, err
	}
	source := &entity.SavedAssemblyCompleteSourceV1{Rows: rows, OwnedSeams: seams, Aliases: aliases, GarmentLabels: usedLabels}
	data, err := completeSourceCanonicalJSON(card.Operations, source, revision)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte(savedAssemblyCompleteSourceDomainV1+"\x00"), data...))
	source.SourceSHA256 = hex.EncodeToString(sum[:])
	source.CanonicalJSON = data
	return source, nil
}

func completeSourceRows(
	card *entity.TechCard,
	anchor entity.SavedPatternRowIdentity,
	lines []entity.RollGoodsLine,
) ([]entity.SavedPatternRowIdentity, map[string]bool, error) {
	artifacts := make([]entity.FabricArtifact[entity.TechCardSizePattern], 0, len(card.Patterns))
	for _, p := range card.Patterns {
		purpose, line := p.FabricPurpose.String, p.BomLineKey.String
		if entity.ResolveFabricScope(purpose, line, lines).Live() {
			artifacts = append(artifacts, entity.FabricArtifact[entity.TechCardSizePattern]{
				FabricPurpose: purpose, BomLineKey: line, Value: p,
			})
		}
	}
	rows := make([]entity.SavedPatternRowIdentity, 0, len(artifacts))
	scopes := make(map[string]bool)
	seen := make(map[string]bool)
	anchorFound := false
	for scope, patterns := range entity.PreferredFabricArtifactsByScope(artifacts, lines) {
		for _, p := range patterns {
			if !entity.ValidTechCardSeamKey(p.LineKey) || seen[p.LineKey] || len(scope) > 64 ||
				!p.SizeBytes.Valid || p.SizeBytes.Int64 <= 0 || p.SizeBytes.Int64 > savedAssemblyMaxJSSafeInteger ||
				!p.SHA256.Valid || !entity.ValidPatternSemanticSidecarSHA256(p.SHA256.String) ||
				p.SavedRowRevision == 0 || p.Version <= 0 || p.Version > math.MaxInt32 ||
				len(p.URL) > savedAssemblyMaxSourceURLBytes || strings.TrimSpace(p.URL) != p.URL || p.URL == "" {
				return nil, nil, fmt.Errorf("live pattern %q has no unique exact source identity: %w", p.LineKey, entity.ErrTechCardSavedAssemblyUnsupported)
			}
			seen[p.LineKey] = true
			row := entity.SavedPatternRowIdentity{
				TechCardID: card.Id, ScopeKey: scope, PatternLineKey: p.LineKey,
				SavedRowRevision: p.SavedRowRevision, CardLockVersion: card.LockVersion,
				URL: p.URL, SizeBytes: p.SizeBytes.Int64, SHA256: p.SHA256.String,
				PatternVersion: p.Version, Filename: p.Filename.String,
			}
			if sameSavedPatternRow(row, anchor) && row.PatternVersion == anchor.PatternVersion {
				anchorFound = true
			}
			rows = append(rows, row)
			scopes[scope] = true
		}
	}
	if !anchorFound {
		return nil, nil, fmt.Errorf("anchor pattern is not in the live source set: %w", entity.ErrTechCardSavedAssemblyMoved)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ScopeKey != rows[j].ScopeKey {
			return rows[i].ScopeKey < rows[j].ScopeKey
		}
		return rows[i].PatternLineKey < rows[j].PatternLineKey
	})
	return rows, scopes, nil
}

func completeSourceAliases(
	card *entity.TechCard,
	scopes map[string]bool,
	lines []entity.RollGoodsLine,
) ([]entity.SavedAssemblySourceAliasV1, map[string]bool, error) {
	pieces := make(map[string]bool, len(card.Pieces))
	for _, piece := range card.Pieces {
		if piece.LineKey == "" {
			continue // an unreferenced legacy piece is still visible in the card
		}
		if !entity.ValidTechCardSeamKey(piece.LineKey) || pieces[piece.LineKey] {
			return nil, nil, fmt.Errorf("card piece %q has an invalid or duplicate identity: %w", piece.LineKey, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		pieces[piece.LineKey] = true
	}
	artifacts := make([]entity.FabricArtifact[entity.TechCardPieceDxfAlias], 0, len(card.PieceDxfAliases))
	for _, alias := range card.PieceDxfAliases {
		artifacts = append(artifacts, entity.FabricArtifact[entity.TechCardPieceDxfAlias]{
			FabricPurpose: alias.FabricPurpose, BomLineKey: alias.BomLineKey, Value: alias,
		})
	}
	aliases := make([]entity.SavedAssemblySourceAliasV1, 0, len(artifacts))
	pieceScopes := make(map[string]bool)
	for scope, group := range entity.PreferredFabricArtifactsByScope(artifacts, lines) {
		if !scopes[scope] {
			continue
		}
		seen := make(map[string]bool, len(group))
		for _, a := range group {
			key := strings.ToLower(strings.Join(strings.Fields(a.BlockName), " "))
			if key == "" || seen[key] || !pieces[a.PieceLineKey] {
				return nil, nil, fmt.Errorf("live scope %q has an ambiguous, empty, or off-card alias: %w", scope, entity.ErrTechCardSavedAssemblyUnsupported)
			}
			seen[key] = true
			aliases = append(aliases, entity.SavedAssemblySourceAliasV1{
				ScopeKey: scope, BlockName: a.BlockName, PieceLineKey: a.PieceLineKey,
			})
			pieceScopes[a.PieceLineKey] = true
		}
	}
	sort.Slice(aliases, func(i, j int) bool {
		if aliases[i].ScopeKey != aliases[j].ScopeKey {
			return aliases[i].ScopeKey < aliases[j].ScopeKey
		}
		return aliases[i].BlockName < aliases[j].BlockName
	})
	return aliases, pieceScopes, nil
}

func completeSourceCardLabels(card *entity.TechCard) (map[string]entity.TechCardGarmentLabel, error) {
	labels := make(map[string]entity.TechCardGarmentLabel, len(card.GarmentLabels))
	if len(card.GarmentLabels) == 0 {
		return labels, nil
	}
	// Garment labels, cut strips and DXF pieces share the stable-line key space.
	taken := make(map[string]bool, len(card.Pieces)+len(card.CutStrips)+len(card.GarmentLabels))
	for _, piece := range card.Pieces {
		taken[entity.NormalizeTechCardSeamKey(piece.LineKey)] = true
	}
	for _, strip := range card.CutStrips {
		taken[entity.NormalizeTechCardSeamKey(strip.LineKey)] = true
	}
	for _, label := range card.GarmentLabels {
		key := label.LineKey
		if key == "" {
			continue // pre-line-key labels cannot supply a saved geometry reference
		}
		if !entity.ValidTechCardSeamKey(key) || key != entity.NormalizeTechCardSeamKey(key) || taken[key] {
			return nil, fmt.Errorf("garment label %q has no unique local line key: %w", key, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		taken[key] = true
		labels[key] = label
	}
	return labels, nil
}

func completeSourceReferencedLabels(ops []entity.TechCardOperation, seams []entity.TechCardSeam, labels map[string]entity.TechCardGarmentLabel) ([]entity.TechCardGarmentLabel, error) {
	used := make(map[string]bool, len(labels))
	for _, op := range ops {
		seen := make(map[string]bool, len(op.LabelKeys))
		for _, key := range op.LabelKeys {
			if _, ok := labels[key]; !ok || seen[key] {
				return nil, fmt.Errorf("operation %d label %q is missing or repeated: %w", op.OperationNumber.Int32, key, entity.ErrTechCardSavedAssemblyUnsupported)
			}
			seen[key] = true
			used[key] = true
		}
	}
	for _, seam := range seams {
		for _, side := range [][]entity.TechCardSeamAnchor{seam.SideA, seam.SideB} {
			for _, anchor := range side {
				if _, ok := labels[anchor.PieceLineKey]; ok {
					used[anchor.PieceLineKey] = true
				}
			}
		}
	}
	out := make([]entity.TechCardGarmentLabel, 0, len(used))
	for key := range used {
		out = append(out, labels[key])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LineKey < out[j].LineKey })
	return out, nil
}

func completeSourceOwnedSeams(card *entity.TechCard, pieceScopes map[string]bool, labels map[string]entity.TechCardGarmentLabel) ([]entity.TechCardSeam, error) {
	byKey := make(map[string]entity.TechCardSeam, len(card.Seams))
	for _, seam := range card.Seams {
		if !entity.ValidTechCardSeamKey(seam.SeamKey) || seam.TechCardId != card.Id {
			return nil, fmt.Errorf("seam %q has an invalid or off-card identity: %w", seam.SeamKey, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		if _, duplicate := byKey[seam.SeamKey]; duplicate {
			return nil, fmt.Errorf("saved seam %q is duplicated: %w", seam.SeamKey, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		byKey[seam.SeamKey] = seam
	}
	numbers := make(map[int32]bool, len(card.Operations))
	owned := make(map[string]bool)
	out := make([]entity.TechCardSeam, 0, len(card.Seams))
	for _, op := range card.Operations {
		number := op.OperationNumber.Int32
		if !op.OperationNumber.Valid || number <= 0 || numbers[number] ||
			len(op.SeamKeys) > entity.TechCardOperationMaxSeamKeys {
			return nil, fmt.Errorf("operation %d has invalid number or seam count: %w", number, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		numbers[number] = true
		// PieceLineKeys is the store's projection of typed piece inputs. Unit and strip references
		// remain in ordered operations, without being mistaken for saved DXF piece identities.
		for _, key := range op.PieceLineKeys {
			if !pieceScopes[key] {
				return nil, fmt.Errorf("operation %d piece %q has no live saved source scope: %w", number, key, entity.ErrTechCardSavedAssemblyUnsupported)
			}
		}
		for _, key := range op.SeamKeys {
			seam, exists := byKey[key]
			if !exists || owned[key] {
				return nil, fmt.Errorf("operation %d seam %q is missing or has multiple owners: %w", number, key, entity.ErrTechCardSavedAssemblyUnsupported)
			}
			if err := validateCompleteSourceSeam(seam, pieceScopes, labels); err != nil {
				return nil, err
			}
			owned[key] = true
			out = append(out, seam)
		}
	}
	return out, nil
}

func validateCompleteSourceSeam(seam entity.TechCardSeam, pieceScopes map[string]bool, labels map[string]entity.TechCardGarmentLabel) error {
	// The wire converter maps unknown stored enum values to unspecified. Refuse those values
	// here so distinct stored values cannot get the same exact-source digest.
	if !savedAssemblyKnownSeamValues(seam, true) ||
		(seam.Status != entity.TechCardSeamConfirmed && seam.Status != entity.TechCardSeamRejected) {
		return fmt.Errorf("seam %q has an unknown stored enum: %w", seam.SeamKey, entity.ErrTechCardSavedAssemblyUnsupported)
	}
	// Do not run write-time physical/placement validation on legacy data. The source still
	// records missing sizes, UNKNOWN directions, original side order and stale/rejected status.
	for _, side := range [][]entity.TechCardSeamAnchor{seam.SideA, seam.SideB} {
		if len(side) == 0 || len(side) > entity.TechCardSeamMaxAnchorsPerSide {
			return fmt.Errorf("seam %q has an invalid anchor count: %w", seam.SeamKey, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		for _, a := range side {
			_, label := labels[a.PieceLineKey]
			if (!pieceScopes[a.PieceLineKey] && !label) || len(a.Samples) > entity.TechCardSeamSamplesPerAnchor {
				return fmt.Errorf("seam %q anchor has invalid samples or no local saved source: %w", seam.SeamKey, entity.ErrTechCardSavedAssemblyUnsupported)
			}
			numbers := []float64{a.PerimShare, a.LenMm, a.TurnDeg, a.RangeFrom, a.RangeTo}
			if a.Placement != nil {
				numbers = append(numbers, a.Placement.XMm, a.Placement.YMm, a.Placement.RotDeg)
			}
			for _, sample := range a.Samples {
				numbers = append(numbers, sample.U, sample.V)
			}
			for _, value := range numbers {
				if math.IsNaN(value) || math.IsInf(value, 0) || (value == 0 && math.Signbit(value)) {
					return fmt.Errorf("seam %q has non-canonical anchor numbers: %w", seam.SeamKey, entity.ErrTechCardSavedAssemblyUnsupported)
				}
			}
		}
	}
	return nil
}

func validCompleteSourceAssemblyRevision(token string) bool {
	value, ok := strings.CutPrefix(token, "asm:v1:")
	if !ok {
		return false
	}
	lineage, rawRevision, found := strings.Cut(value, ":")
	if !found || len(lineage) != 32 || strings.ToLower(lineage) != lineage {
		return false
	}
	if _, err := hex.DecodeString(lineage); err != nil {
		return false
	}
	revision, err := strconv.ParseUint(rawRevision, 10, 64)
	return err == nil && revision > 0 && rawRevision == strconv.FormatUint(revision, 10)
}

// Hash the same protobuf JSON fields the caller sees. Remove download capabilities: they are
// minted after REPEATABLE READ, and their lifetime is independent of every source byte.
func completeSourceCanonicalJSON(
	operations []entity.TechCardOperation,
	source *entity.SavedAssemblyCompleteSourceV1,
	revision string,
) ([]byte, error) {
	wire, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(
		&pb_admin.GetTechCardSavedAssemblySnapshotResponse{
			Operations:       dto.TechCardOperationsToPbForSavedAssembly(operations),
			CompleteSourceV1: dto.SavedAssemblyCompleteSourceV1ToPb(source),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("marshal complete Assembly source: %w", err)
	}
	if len(wire) > savedAssemblyMaxSourceBytes {
		return nil, fmt.Errorf("complete Assembly source exceeds its byte bound: %w", entity.ErrTechCardSavedAssemblyUnsupported)
	}
	var value struct {
		Operations       []any
		CompleteSourceV1 struct {
			Rows          []map[string]any
			OwnedSeams    []any
			Aliases       []any
			GarmentLabels []any
		}
	}
	if err := json.Unmarshal(wire, &value); err != nil {
		return nil, fmt.Errorf("decode complete Assembly source wire JSON: %w", err)
	}
	for _, row := range value.CompleteSourceV1.Rows {
		delete(row, "downloadUrl")
	}
	rows := make([]any, 0, len(value.CompleteSourceV1.Rows))
	for _, row := range value.CompleteSourceV1.Rows {
		rows = append(rows, row)
	}
	return savedAssemblyCanonicalJSON(map[string]any{
		"serverAssemblyRevision": revision,
		"operations":             value.Operations, "rows": rows,
		"ownedSeams": value.CompleteSourceV1.OwnedSeams, "aliases": value.CompleteSourceV1.Aliases,
		"garmentLabels": value.CompleteSourceV1.GarmentLabels,
	})
}
