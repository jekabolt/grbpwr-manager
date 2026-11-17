package techcard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	savedAssemblyTopologyDomainV1      = "pattern-import-dxf-phase-e-saved-row-server-topology/1"
	savedFullAssemblyAOContentDomainV1 = "saved-full-assembly-physical-ao-content/1"
)

var phaseEStructuralWorkTokensV1 = map[string]bool{
	"join_lockstitch": true, "topstitch": true, "overlock_serge": true,
	"coverstitch": true, "coverlock": true, "chainstitch": true,
	"blindhem": true, "zigzag": true, "amf_handstitch_imitation": true,
}

type savedPatternAuthorityRow struct {
	LineKey          string         `db:"line_key"`
	FabricPurpose    sql.NullString `db:"fabric_purpose"`
	BomLineKey       sql.NullString `db:"bom_line_key"`
	URL              string         `db:"url"`
	SizeBytes        sql.NullInt64  `db:"size_bytes"`
	SHA256           sql.NullString `db:"sha256"`
	PatternVersion   int            `db:"version"`
	SavedRowRevision uint32         `db:"saved_row_revision"`
	Filename         sql.NullString `db:"filename"`
}

// GetTechCardSavedAssemblySnapshot returns selected topology or the opt-in complete saved source.
// The card, pattern rows, clock and existing anchor sidecar share one REPEATABLE READ snapshot.
func (s *Store) GetTechCardSavedAssemblySnapshot(ctx context.Context, in entity.SavedAssemblySnapshotRequest) (entity.SavedAssemblySnapshot, error) {
	if err := validateSavedAssemblySnapshotRequest(in); err != nil {
		return entity.SavedAssemblySnapshot{}, err
	}
	var out entity.SavedAssemblySnapshot
	err := s.readTxFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if in.Profile == entity.SavedAssemblySnapshotCompleteSourceV1 {
			if err := validateCompleteSourcePieceInputOwnership(ctx, rep.DB(), in.ExpectedRow.TechCardID); err != nil {
				return err
			}
		}
		card, err := rep.TechCards().GetTechCardById(ctx, in.ExpectedRow.TechCardID)
		if err != nil {
			return err
		}
		pattern, err := storeutil.QueryNamedOne[savedPatternAuthorityRow](ctx, rep.DB(), `
			SELECT COALESCE(line_key, '') AS line_key, fabric_purpose, bom_line_key, url,
			       size_bytes, sha256, version, saved_row_revision, filename
			FROM tech_card_size_pattern
			WHERE tech_card_id = :card AND line_key = :line`, map[string]any{
			"card": in.ExpectedRow.TechCardID,
			"line": in.ExpectedRow.PatternLineKey,
		})
		if err != nil {
			return fmt.Errorf("read saved pattern authority row: %w", err)
		}
		lines, err := loadRollGoodsLines(ctx, rep.DB(), in.ExpectedRow.TechCardID)
		if err != nil {
			return err
		}
		scope := entity.ResolveFabricScope(pattern.FabricPurpose.String, pattern.BomLineKey.String, lines)
		if !scope.Live() || strings.TrimSpace(scope.Key) == "" {
			return fmt.Errorf("pattern line %s has no live fabric scope: %w", pattern.LineKey, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		scopeKey := scope.Key
		if in.Profile == entity.SavedAssemblySnapshotCompleteSourceV1 {
			scopeKey = entity.FabricScopeIdentity(pattern.FabricPurpose.String, pattern.BomLineKey.String, lines)
		}
		row := entity.SavedPatternRowIdentity{
			TechCardID: card.Id, ScopeKey: scopeKey, PatternLineKey: pattern.LineKey,
			SavedRowRevision: pattern.SavedRowRevision, CardLockVersion: card.LockVersion,
			URL: pattern.URL, SizeBytes: pattern.SizeBytes.Int64, SHA256: pattern.SHA256.String,
			PatternVersion: pattern.PatternVersion,
			Filename:       pattern.Filename.String,
		}
		if !pattern.SizeBytes.Valid || row.SizeBytes <= 0 || !pattern.SHA256.Valid ||
			!entity.ValidPatternSemanticSidecarSHA256(row.SHA256) || row.SavedRowRevision == 0 {
			return fmt.Errorf("pattern line %s lacks exact size/hash/revision: %w", pattern.LineKey, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		if !sameSavedPatternRow(row, in.ExpectedRow) {
			return fmt.Errorf("pattern line %s no longer matches expected row: %w", pattern.LineKey, entity.ErrTechCardSavedAssemblyMoved)
		}

		clock, err := readTechCardAssemblyClock(ctx, rep.DB(), row.TechCardID)
		if err != nil {
			return err
		}
		token := clock.Token()
		if in.ExpectedServerAssemblyRevision != "" && in.ExpectedServerAssemblyRevision != token {
			return fmt.Errorf("assembly revision changed: %w", entity.ErrTechCardSavedAssemblyMoved)
		}
		if in.Profile == entity.SavedAssemblySnapshotCompleteSourceV1 {
			source, err := buildSavedAssemblyCompleteSourceV1(card, row, lines, token)
			if err != nil {
				return err
			}
			if in.ExpectedCompleteSourceSHA256 != "" && source.SourceSHA256 != in.ExpectedCompleteSourceSHA256 {
				return fmt.Errorf("complete Assembly source changed: %w", entity.ErrTechCardSavedAssemblyMoved)
			}
			sidecar, err := readSavedAssemblyAnchorSidecar(ctx, rep.DB(), row)
			if err != nil {
				return err
			}
			out = entity.SavedAssemblySnapshot{
				Row: row, Card: card, ServerAssemblyRevision: token, CompleteSourceV1: source,
				SemanticSidecar: sidecar,
			}
			return nil
		}
		selected, err := selectSavedAssemblySeams(card, in.SelectedOperationNumber)
		if err != nil {
			return err
		}
		canonical, topologySHA, err := savedAssemblyTopology(card.Operations, selected, in.SelectedOperationNumber)
		if err != nil {
			return err
		}
		if in.ExpectedTopologySHA256 != "" && in.ExpectedTopologySHA256 != topologySHA {
			return fmt.Errorf("assembly topology changed: %w", entity.ErrTechCardSavedAssemblyMoved)
		}

		sidecar, err := readSavedAssemblyAnchorSidecar(ctx, rep.DB(), row)
		if err != nil {
			return err
		}
		out = entity.SavedAssemblySnapshot{
			Row: row, SelectedOperationNumber: in.SelectedOperationNumber, Card: card,
			SelectedSeams: selected, ServerAssemblyRevision: token,
			TopologySHA256: topologySHA, TopologyCanonicalJSON: append([]byte(nil), canonical...),
			SemanticSidecar: sidecar,
		}
		return nil
	})
	if err != nil {
		return entity.SavedAssemblySnapshot{}, err
	}
	return out, nil
}

func readSavedAssemblyAnchorSidecar(ctx context.Context, db dependency.DB, row entity.SavedPatternRowIdentity) (*entity.PatternSemanticSidecar, error) {
	stored, err := readPatternSemanticSidecar(ctx, db, row.TechCardID, row.PatternLineKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &stored, nil
}

func validateSavedAssemblySnapshotRequest(in entity.SavedAssemblySnapshotRequest) error {
	r := in.ExpectedRow
	if r.TechCardID <= 0 {
		return entity.NewFieldViolation("tech_card_id", "required", "", "")
	}
	if !entity.ValidTechCardSeamKey(r.PatternLineKey) || r.PatternLineKey != entity.NormalizeTechCardSeamKey(r.PatternLineKey) {
		return entity.NewFieldViolation("pattern_line_key", "invalid", r.PatternLineKey, "send the exact canonical 26-char line key")
	}
	if strings.TrimSpace(r.ScopeKey) == "" || len(r.ScopeKey) > 64 {
		return entity.NewFieldViolation("expected_scope_key", "invalid", r.ScopeKey, "send the server-resolved fabric scope")
	}
	if r.SavedRowRevision == 0 || r.CardLockVersion < 0 || r.URL == "" || r.SizeBytes <= 0 ||
		!entity.ValidPatternSemanticSidecarSHA256(r.SHA256) {
		return entity.NewFieldViolation("expected_row", "incomplete", "", "send all eight exact saved-row fields")
	}
	switch in.Profile {
	case entity.SavedAssemblySnapshotSelected:
		if in.SelectedOperationNumber <= 0 {
			return entity.NewFieldViolation("selected_operation_number", "invalid", fmt.Sprint(in.SelectedOperationNumber), "send a positive stored operation number")
		}
		if in.ExpectedCompleteSourceSHA256 != "" {
			return entity.NewFieldViolation("expected_complete_source_sha256", "invalid", "", "select the complete-source profile to check this digest")
		}
	case entity.SavedAssemblySnapshotCompleteSourceV1:
		if in.SelectedOperationNumber != 0 || in.ExpectedTopologySHA256 != "" {
			return entity.NewFieldViolation("profile", "incompatible", "", "omit selected-operation fields for the complete-source profile")
		}
		if len(r.URL) > savedAssemblyMaxSourceURLBytes || r.SizeBytes > savedAssemblyMaxJSSafeInteger {
			return entity.NewFieldViolation("expected_row", "out_of_bounds", "", "send a bounded URL and exact byte count")
		}
		if in.ExpectedCompleteSourceSHA256 != "" && !entity.ValidPatternSemanticSidecarSHA256(in.ExpectedCompleteSourceSHA256) {
			return entity.NewFieldViolation("expected_complete_source_sha256", "invalid", "", "send a lowercase SHA-256")
		}
		if in.ExpectedServerAssemblyRevision != "" && !validCompleteSourceAssemblyRevision(in.ExpectedServerAssemblyRevision) {
			return entity.NewFieldViolation("expected_server_assembly_revision", "invalid", "", "send the complete Assembly clock token returned by this RPC")
		}
	default:
		return entity.NewFieldViolation("profile", "invalid", "", "send a supported saved Assembly snapshot profile")
	}
	if in.ExpectedServerAssemblyRevision != "" && !strings.HasPrefix(in.ExpectedServerAssemblyRevision, "asm:v1:") {
		return entity.NewFieldViolation("expected_server_assembly_revision", "invalid", in.ExpectedServerAssemblyRevision, "send the opaque token returned by this RPC")
	}
	if in.ExpectedTopologySHA256 != "" && !entity.ValidPatternSemanticSidecarSHA256(in.ExpectedTopologySHA256) {
		return entity.NewFieldViolation("expected_assembly_topology_sha256", "invalid", in.ExpectedTopologySHA256, "send a lowercase SHA-256")
	}
	return nil
}

func sameSavedPatternRow(a, b entity.SavedPatternRowIdentity) bool {
	return a.TechCardID == b.TechCardID && a.ScopeKey == b.ScopeKey &&
		a.PatternLineKey == b.PatternLineKey &&
		a.SavedRowRevision == b.SavedRowRevision && a.CardLockVersion == b.CardLockVersion &&
		a.URL == b.URL && a.SizeBytes == b.SizeBytes && a.SHA256 == b.SHA256
}

func selectSavedAssemblySeams(card *entity.TechCard, selectedOperationNumber int) ([]entity.TechCardSeam, error) {
	var selected *entity.TechCardOperation
	operationNumbers := make(map[int]struct{}, len(card.Operations))
	for i := range card.Operations {
		op := &card.Operations[i]
		if !op.OperationNumber.Valid || op.OperationNumber.Int32 <= 0 {
			return nil, fmt.Errorf("operation at index %d has no positive number: %w", i, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		number := int(op.OperationNumber.Int32)
		if _, duplicate := operationNumbers[number]; duplicate {
			return nil, fmt.Errorf("operation number %d is duplicated: %w", number, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		operationNumbers[number] = struct{}{}
		if number != selectedOperationNumber {
			continue
		}
		selected = op
	}
	if selected == nil || len(selected.SeamKeys) == 0 {
		return nil, fmt.Errorf("operation %d must own at least one seam: %w", selectedOperationNumber, entity.ErrTechCardSavedAssemblyUnsupported)
	}
	if !phaseEStructuralWorkTokensV1[selected.Work.String] {
		return nil, fmt.Errorf("operation %d does not name a structural stitch work: %w", selectedOperationNumber, entity.ErrTechCardSavedAssemblyUnsupported)
	}
	owned := make(map[string]bool, len(selected.SeamKeys))
	for _, key := range selected.SeamKeys {
		normalized := entity.NormalizeTechCardSeamKey(key)
		if normalized == "" || owned[normalized] {
			return nil, fmt.Errorf("operation %d must own distinct seam keys: %w", selectedOperationNumber, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		owned[normalized] = true
	}
	for i := range card.Operations {
		op := &card.Operations[i]
		if op == selected {
			continue
		}
		for _, key := range op.SeamKeys {
			if owned[entity.NormalizeTechCardSeamKey(key)] {
				return nil, fmt.Errorf("selected seam has a second operation owner: %w", entity.ErrTechCardSavedAssemblyUnsupported)
			}
		}
	}
	byKey := make(map[string][]entity.TechCardSeam, len(card.Seams))
	for _, seam := range card.Seams {
		key := entity.NormalizeTechCardSeamKey(seam.SeamKey)
		byKey[key] = append(byKey[key], seam)
	}
	result := make([]entity.TechCardSeam, 0, len(selected.SeamKeys))
	for _, rawKey := range selected.SeamKeys {
		key := entity.NormalizeTechCardSeamKey(rawKey)
		matches := byKey[key]
		if len(matches) != 1 {
			return nil, fmt.Errorf("selected seam %s is missing or ambiguous: %w", key, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		seam := matches[0]
		if seam.Status != entity.TechCardSeamConfirmed || seam.Stale || !seam.Kind.IsSeam() ||
			seam.Direction == entity.TechCardSeamDirectionUnknown || strings.TrimSpace(seam.AnchoredSize) == "" ||
			len(seam.SideA) != 1 || len(seam.SideB) != 1 {
			return nil, fmt.Errorf("selected seam %s is not exact/current: %w", key, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		if !savedAssemblyKnownSeamValues(seam, false) {
			return nil, fmt.Errorf("selected seam %s has an unknown enum value: %w", key, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		currentPair := []string{seam.SideA[0].PieceLineKey, seam.SideB[0].PieceLineKey}
		if currentPair[0] == "" || currentPair[1] == "" || currentPair[0] == currentPair[1] {
			return nil, fmt.Errorf("selected seam %s has an invalid piece pair: %w", key, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		result = append(result, seam)
	}
	return result, nil
}

// selectSavedAssemblySeamsV3 preserves the original composite V3 contract. V5 and the saved
// snapshot authority can represent one or many seams, but V3's exact-topology component encodes
// exactly two seams joining one piece pair and must remain fail-closed on every other shape.
func selectSavedAssemblySeamsV3(card *entity.TechCard, selectedOperationNumber int) ([]entity.TechCardSeam, error) {
	selected, err := selectSavedAssemblySeams(card, selectedOperationNumber)
	if err != nil {
		return nil, err
	}
	if len(selected) != 2 {
		return nil, fmt.Errorf("operation %d must own exactly two distinct seams for composite V3: %w",
			selectedOperationNumber, entity.ErrTechCardSavedAssemblyUnsupported)
	}
	var pair []string
	for _, seam := range selected {
		currentPair := []string{seam.SideA[0].PieceLineKey, seam.SideB[0].PieceLineKey}
		sort.Strings(currentPair)
		if pair == nil {
			pair = currentPair
			continue
		}
		if pair[0] != currentPair[0] || pair[1] != currentPair[1] {
			return nil, fmt.Errorf("selected composite V3 seams do not share one piece pair: %w",
				entity.ErrTechCardSavedAssemblyUnsupported)
		}
	}
	return selected, nil
}

func savedAssemblyKnownSeamValues(seam entity.TechCardSeam, allowWorkflow bool) bool {
	switch seam.Kind {
	case entity.TechCardSeamKindEdge, entity.TechCardSeamKindPartial, entity.TechCardSeamKindComposite,
		entity.TechCardSeamKindSurface, entity.TechCardSeamKindClosure:
	case entity.TechCardSeamKindPose:
		if !allowWorkflow {
			return false
		}
	default:
		return false
	}
	if seam.Direction != entity.TechCardSeamSame && seam.Direction != entity.TechCardSeamReversed &&
		!(allowWorkflow && seam.Direction == entity.TechCardSeamDirectionUnknown) {
		return false
	}
	switch seam.Source {
	case entity.TechCardSeamSourceGraph, entity.TechCardSeamSourceDoll, entity.TechCardSeamSourceOrder,
		entity.TechCardSeamSourceManual, entity.TechCardSeamSourceAI:
	default:
		return false
	}
	for _, side := range [][]entity.TechCardSeamAnchor{seam.SideA, seam.SideB} {
		for _, anchor := range side {
			if anchor.Layer != "" && anchor.Layer != entity.TechCardSeamLayerUnder && anchor.Layer != entity.TechCardSeamLayerOver {
				return false
			}
		}
	}
	return true
}

func savedAssemblyStringArray(values []string) []string {
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func savedAssemblyTopology(operations []entity.TechCardOperation, seams []entity.TechCardSeam, selectedOperationNumber int) ([]byte, string, error) {
	ops := make([]any, 0, len(operations))
	for _, op := range operations {
		var phaseEKind any
		if phaseEStructuralWorkTokensV1[op.Work.String] {
			phaseEKind = "stitch"
		}
		ops = append(ops, map[string]any{
			"authoringOperationId": nil, "inputKeys": savedAssemblyStringArray(op.InputKeys),
			"number": int(op.OperationNumber.Int32), "phaseEKind": phaseEKind,
			"outputUnitKey": op.OutputUnitKey.String, "outputUnitName": op.OutputUnitName.String,
			"seamKeys": savedAssemblyStringArray(op.SeamKeys), "work": op.Work.String,
		})
	}
	seamValues := make([]any, 0, len(seams))
	for _, seam := range seams {
		value, err := savedAssemblySeamCanonicalValue(seam)
		if err != nil {
			return nil, "", err
		}
		seamValues = append(seamValues, value)
	}
	value := map[string]any{
		"operations": ops, "seams": seamValues, "selectedOperationNumber": selectedOperationNumber,
	}
	canonical, err := savedAssemblyCanonicalJSON(value)
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize saved assembly topology: %w", err)
	}
	sum := sha256.Sum256(append([]byte(savedAssemblyTopologyDomainV1+"\x00"), canonical...))
	return canonical, hex.EncodeToString(sum[:]), nil
}

// savedFullAssemblyAOContent reproduces the browser's complete AO-only fingerprint from server
// authority. It deliberately excludes the stored outer sidecar, the full-graph payload and the
// joined snapshot digest. The operation JSON comes from the same protobuf projection and
// EmitUnpopulated option as the production admin gateway; hand-building it from entity fields would
// silently omit workflow fields and make the browser/server contract diverge.
func savedFullAssemblyAOContent(row entity.SavedPatternRowIdentity, serverAssemblyRevision string, card *entity.TechCard) ([]byte, string, error) {
	if card == nil || row.TechCardID <= 0 || row.SavedRowRevision == 0 || row.CardLockVersion < 0 ||
		row.URL == "" || row.SizeBytes <= 0 || row.SizeBytes > 1<<53-1 ||
		!entity.ValidPatternSemanticSidecarSHA256(row.SHA256) ||
		!strings.HasPrefix(serverAssemblyRevision, "asm:v1:") {
		return nil, "", fmt.Errorf("incomplete Full Assembly authority: %w", entity.ErrTechCardSavedAssemblyUnsupported)
	}
	operationsCanonical, err := savedAssemblyOperationsWireCanonicalJSON(card.Operations)
	if err != nil {
		return nil, "", err
	}
	receipts := make([]any, 0, len(card.Operations))
	for i := range card.Operations {
		op := card.Operations[i]
		if len(op.SeamKeys) == 0 {
			continue
		}
		if !op.OperationNumber.Valid || op.OperationNumber.Int32 <= 0 {
			return nil, "", fmt.Errorf("seam-bearing operation at index %d has no positive number: %w",
				i, entity.ErrTechCardSavedAssemblyUnsupported)
		}
		number := int(op.OperationNumber.Int32)
		selected, err := selectSavedAssemblySeams(card, number)
		if err != nil {
			return nil, "", err
		}
		canonical, digest, err := savedAssemblyTopology(card.Operations, selected, number)
		if err != nil {
			return nil, "", err
		}
		receipts = append(receipts, map[string]any{
			"sourceOperationNumber": number,
			"sha256":                digest,
			"canonicalJson":         string(canonical),
		})
	}
	if len(receipts) == 0 {
		return nil, "", fmt.Errorf("full assembly has no seam-bearing operation: %w", entity.ErrTechCardSavedAssemblyUnsupported)
	}
	payload := map[string]any{
		"row": map[string]any{
			"techCardId":      row.TechCardID,
			"scopeKey":        row.ScopeKey,
			"patternLineKey":  row.PatternLineKey,
			"revision":        int(row.SavedRowRevision),
			"cardLockVersion": row.CardLockVersion,
			"url":             row.URL,
			"sizeBytes":       int(row.SizeBytes),
			"sha256":          row.SHA256,
		},
		"serverAssemblyRevision":         serverAssemblyRevision,
		"operationsPayloadCanonicalJson": string(operationsCanonical),
		"selectedTopologyReceipts":       receipts,
	}
	canonical, err := savedAssemblyCanonicalJSON(payload)
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize complete Assembly Order content: %w", err)
	}
	sum := sha256.Sum256(append([]byte(savedFullAssemblyAOContentDomainV1+"\x00"), canonical...))
	return canonical, hex.EncodeToString(sum[:]), nil
}

func savedAssemblyOperationsWireCanonicalJSON(operations []entity.TechCardOperation) ([]byte, error) {
	wire, err := (protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: false}).Marshal(
		&pb_admin.GetTechCardSavedAssemblySnapshotResponse{
			Operations: dto.TechCardOperationsToPbForSavedAssembly(operations),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("marshal complete Assembly Order operations: %w", err)
	}
	var response map[string]any
	if err := json.Unmarshal(wire, &response); err != nil {
		return nil, fmt.Errorf("decode complete Assembly Order operations wire JSON: %w", err)
	}
	operationsValue, ok := response["operations"]
	if !ok {
		return nil, fmt.Errorf("operations missing from production wire projection: %w", entity.ErrTechCardSavedAssemblyUnsupported)
	}
	canonical, err := savedAssemblyCanonicalJSON(operationsValue)
	if err != nil {
		return nil, fmt.Errorf("canonicalize complete Assembly Order operations: %w", err)
	}
	return canonical, nil
}

// savedAssemblyCanonicalJSON mirrors the browser contract's sorted-key JSON.stringify encoder.
// encoding/json's default HTML and U+2028/U+2029 escaping is valid JSON but would hash different
// bytes from JavaScript for a persisted human-readable operation name.
func savedAssemblyCanonicalJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	if err := appendSavedAssemblyCanonicalJSON(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func appendSavedAssemblyCanonicalJSON(out *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(typed))
	case int:
		out.WriteString(strconv.Itoa(typed))
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || (typed == 0 && math.Signbit(typed)) {
			return fmt.Errorf("non-canonical numeric value: %w", entity.ErrTechCardSavedAssemblyUnsupported)
		}
		encoded, err := json.Marshal(typed)
		if err != nil {
			return err
		}
		out.Write(encoded)
	case string:
		if !utf8.ValidString(typed) {
			return fmt.Errorf("invalid UTF-8 string: %w", entity.ErrTechCardSavedAssemblyUnsupported)
		}
		appendSavedAssemblyJSONString(out, typed)
	case []string:
		out.WriteByte('[')
		for index, child := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			appendSavedAssemblyJSONString(out, child)
		}
		out.WriteByte(']')
	case []any:
		out.WriteByte('[')
		for index, child := range typed {
			if index > 0 {
				out.WriteByte(',')
			}
			if err := appendSavedAssemblyCanonicalJSON(out, child); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				out.WriteByte(',')
			}
			appendSavedAssemblyJSONString(out, key)
			out.WriteByte(':')
			if err := appendSavedAssemblyCanonicalJSON(out, typed[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical value %T: %w", value, entity.ErrTechCardSavedAssemblyUnsupported)
	}
	return nil
}

func appendSavedAssemblyJSONString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	for _, scalar := range value {
		switch scalar {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(scalar)
		case '\b':
			out.WriteString(`\b`)
		case '\t':
			out.WriteString(`\t`)
		case '\n':
			out.WriteString(`\n`)
		case '\f':
			out.WriteString(`\f`)
		case '\r':
			out.WriteString(`\r`)
		default:
			if scalar < 0x20 {
				out.WriteString(`\u00`)
				const hexDigits = "0123456789abcdef"
				out.WriteByte(hexDigits[byte(scalar)>>4])
				out.WriteByte(hexDigits[byte(scalar)&0x0f])
			} else {
				out.WriteRune(scalar)
			}
		}
	}
	out.WriteByte('"')
}

func savedAssemblySeamCanonicalValue(seam entity.TechCardSeam) (map[string]any, error) {
	canonicalSide := func(anchors []entity.TechCardSeamAnchor) ([]any, error) {
		out := make([]any, 0, len(anchors))
		for _, anchor := range anchors {
			floats := []float64{anchor.LenMm, anchor.RangeFrom, anchor.RangeTo}
			for _, sample := range anchor.Samples {
				floats = append(floats, sample.U, sample.V)
			}
			for _, number := range floats {
				if math.IsNaN(number) || math.IsInf(number, 0) || (number == 0 && math.Signbit(number)) {
					return nil, fmt.Errorf("seam %s contains non-canonical numeric evidence: %w", seam.SeamKey, entity.ErrTechCardSavedAssemblyUnsupported)
				}
			}
			samples := make([]any, 0, len(anchor.Samples))
			for _, sample := range anchor.Samples {
				samples = append(samples, []any{sample.U, sample.V})
			}
			value := map[string]any{
				"contourSig": anchor.ContourSig, "edgeHint": anchor.EdgeHint, "lenMm": anchor.LenMm,
				"pieceLineKey": anchor.PieceLineKey, "range": []any{anchor.RangeFrom, anchor.RangeTo},
				"samples": samples,
			}
			if anchor.Layer != "" {
				value["layer"] = string(anchor.Layer)
			}
			out = append(out, value)
		}
		return out, nil
	}
	sideA, err := canonicalSide(seam.SideA)
	if err != nil {
		return nil, err
	}
	sideB, err := canonicalSide(seam.SideB)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"anchoredSize": seam.AnchoredSize, "direction": string(seam.Direction), "kind": string(seam.Kind),
		"confirmed": seam.Status == entity.TechCardSeamConfirmed, "seamKey": seam.SeamKey,
		"sideA": sideA, "sideB": sideB, "source": string(seam.Source), "stale": seam.Stale,
	}, nil
}
