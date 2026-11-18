package techcard

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// A saved native Join has two existing lines and one independently minted result line.
// The caller binds their block names to the verified native review, before entering this
// read-only stage. This type is intentionally private until the guarded document RPC is wired.
type joinLineGroup struct {
	lineKey    string
	blockNames []string
}

type joinRetirement struct {
	row                  entity.SavedPatternRowIdentity
	inputs               [2]joinLineGroup
	resultLineKey        string
	resultBlock          string
	assemblyRevision     string
	completeSourceSHA256 string
}

type joinRetirementRows struct {
	// Existing aliases outside the two reviewed groups retain all stored fields and order.
	keptAliases []entity.TechCardPieceDxfAlias
	sourceIDs   [2]int
	// A reference on another piece in the replacement fabric changes its stored fingerprint too.
	stableAnchors map[string]bool
}

func blockedJoin(detail string) error {
	return fmt.Errorf("saved native join: %s: %w", detail, entity.ErrTechCardSavedAssemblyUnsupported)
}

// The UpdateTechCardTx caller must already hold the shared card row FOR UPDATE. No locked
// write path uses this prerequisite yet. It neither mints a receipt nor mutates the card.
func readJoinRetirementUnderLock(ctx context.Context, rep dependency.Repository, want joinRetirement) (*joinRetirementRows, error) {
	if err := validateSavedAssemblySnapshotRequest(entity.SavedAssemblySnapshotRequest{
		ExpectedRow: want.row, Profile: entity.SavedAssemblySnapshotCompleteSourceV1,
		ExpectedServerAssemblyRevision: want.assemblyRevision, ExpectedCompleteSourceSHA256: want.completeSourceSHA256,
	}); err != nil {
		return nil, err
	}
	if want.assemblyRevision == "" || want.completeSourceSHA256 == "" {
		return nil, blockedJoin("missing complete Assembly binding")
	}
	if err := validateCompleteSourcePieceInputOwnership(ctx, rep.DB(), want.row.TechCardID); err != nil {
		return nil, err
	}
	clock, err := readTechCardAssemblyClock(ctx, rep.DB(), want.row.TechCardID)
	if err != nil {
		return nil, err
	}
	if clock.Token() != want.assemblyRevision {
		return nil, entity.ErrTechCardSavedAssemblyMoved
	}
	card, err := rep.TechCards().GetTechCardById(ctx, want.row.TechCardID)
	if err != nil {
		return nil, err
	}
	if card == nil {
		return nil, entity.ErrTechCardSavedAssemblyMoved
	}
	lines := entity.RollGoodsLinesOfBom(card.BomItems)
	if _, err := buildCheckedSavedAssemblyCompleteSourceV1(card, want.row, lines, clock.Token(), want.completeSourceSHA256); err != nil {
		return nil, err
	}
	rows, err := planJoinRetirementReferences(card, lines, want)
	if err != nil {
		return nil, err
	}
	// GetTechCard deliberately tolerates a corrupt side, and complete-source hashes operation-
	// owned seams only. This query sees every persisted physical and POSE row, including an
	// unowned or rejected row. Unknown or partially decoded references cannot prove absence.
	seams, err := storeutil.QueryListNamed[struct {
		A []byte `db:"side_a"`
		B []byte `db:"side_b"`
	}](ctx, rep.DB(),
		`SELECT side_a, side_b FROM tech_card_seam WHERE tech_card_id = :id`, map[string]any{"id": card.Id})
	if err != nil {
		return nil, err
	}
	for _, s := range seams {
		for _, side := range [][]byte{s.A, s.B} {
			if err := rows.checkStoredSeamSide(side); err != nil {
				return nil, err
			}
		}
	}
	// The display read deliberately hides corrupt operation annotations too. For a
	// retirement, read the raw JSON while the same transaction owns the document.
	annotations, err := storeutil.QueryScalarListNamed[[]byte](ctx, rep.DB(), `SELECT m.annotations
		FROM tech_card_operation_media m JOIN tech_card_operation o ON o.id = m.tech_card_operation_id
		WHERE o.tech_card_id = :id AND m.annotations IS NOT NULL`, map[string]any{"id": card.Id})
	if err != nil {
		return nil, err
	}
	for _, raw := range annotations {
		var values []entity.TechCardAnnotation
		if err := json.Unmarshal(raw, &values); err != nil || values == nil {
			return nil, blockedJoin("unreadable operation annotations")
		}
		for _, a := range values {
			if want.involves(a.PieceLineKey) || slices.ContainsFunc(a.PieceLineKeys, want.involves) {
				return nil, blockedJoin("stored operation annotation")
			}
		}
	}
	// Sidecars, markers, and cached area rows are written independently of this form. Reuse
	// the conservative zero-operation rename policy for the first Join. They are outside
	// the reviewed full-card document, so this step cannot intentionally retire them.
	protected, err := hasGuardedPatternAttachments(ctx, rep.DB(), card.Id)
	if err != nil {
		return nil, err
	}
	if protected {
		return nil, blockedJoin("semantic, area, or marker attachment")
	}
	// The document diff deletes recipe usages before deleting a piece. Read their actual
	// FKs even if an archived colourway or legacy row is absent from the enriched form.
	usage, err := storeutil.QueryNamedOne[struct {
		Used bool `db:"used"`
	}](ctx, rep.DB(),
		`SELECT EXISTS(SELECT 1 FROM tech_card_colorway_usage WHERE piece_id IN (:ids)) AS used`,
		map[string]any{"ids": rows.sourceIDs[:]})
	if err != nil {
		return nil, err
	}
	if usage.Used {
		return nil, blockedJoin("saved material recipe references a retiring piece")
	}
	return rows, nil
}

func (j joinRetirement) involves(key string) bool {
	key = entity.NormalizeTechCardSeamKey(key)
	return key == j.inputs[0].lineKey || key == j.inputs[1].lineKey || key == j.resultLineKey
}

func (r *joinRetirementRows) checkStoredSeamSide(raw []byte) error {
	var side []entity.TechCardSeamAnchor
	if err := json.Unmarshal(raw, &side); err != nil || len(side) == 0 || len(side) > entity.TechCardSeamMaxAnchorsPerSide {
		return blockedJoin("unreadable saved seam side")
	}
	for _, part := range side {
		if !entity.ValidTechCardSeamKey(part.PieceLineKey) || !r.stableAnchors[part.PieceLineKey] {
			return blockedJoin("saved seam or label placement")
		}
	}
	return nil
}

func planJoinRetirementReferences(card *entity.TechCard, lines []entity.RollGoodsLine, j joinRetirement) (*joinRetirementRows, error) {
	keys := []string{j.inputs[0].lineKey, j.inputs[1].lineKey, j.resultLineKey}
	if card == nil || card.Id != j.row.TechCardID || card.LockVersion != j.row.CardLockVersion || j.resultBlock == "" {
		return nil, entity.ErrTechCardSavedAssemblyMoved
	}
	for i, key := range keys {
		if !entity.ValidTechCardSeamKey(key) || slices.Contains(keys[:i], key) {
			return nil, blockedJoin("invalid or reused piece key")
		}
	}
	scoped := 0
	for _, p := range card.Patterns {
		if entity.FabricScopeIdentity(p.FabricPurpose.String, p.BomLineKey.String, lines) != j.row.ScopeKey {
			continue
		}
		scoped++
		if p.LineKey != j.row.PatternLineKey || p.SavedRowRevision != j.row.SavedRowRevision || !p.SHA256.Valid || p.SHA256.String != j.row.SHA256 ||
			p.URL != j.row.URL || !p.SizeBytes.Valid || p.SizeBytes.Int64 != j.row.SizeBytes || p.Version != j.row.PatternVersion {
			return nil, entity.ErrTechCardSavedAssemblyMoved
		}
	}
	if scoped != 1 {
		return nil, blockedJoin("ambiguous source scope")
	}
	result := &joinRetirementRows{stableAnchors: map[string]bool{}}
	changingScopes := map[string]bool{}
	clothScopes := map[string]bool{}
	for _, line := range lines {
		if line.Section != entity.BomSectionInterlining {
			clothScopes[entity.EffectiveFabricScopeKey(line)] = true
		}
	}
	if !clothScopes[j.row.ScopeKey] {
		return nil, blockedJoin("source is not a live cloth scope")
	}
	for _, alias := range card.PieceDxfAliases {
		scope := entity.FabricScopeIdentity(alias.FabricPurpose, alias.BomLineKey, lines)
		if scope == j.row.ScopeKey {
			changingScopes[alias.PieceLineKey] = true
		} else if clothScopes[scope] {
			result.stableAnchors[alias.PieceLineKey] = true
		}
	}
	for k := range changingScopes {
		delete(result.stableAnchors, k)
	}
	for _, l := range card.GarmentLabels {
		result.stableAnchors[l.LineKey] = true
	}
	for _, s := range card.CutStrips {
		result.stableAnchors[s.LineKey] = true
	}
	for _, p := range card.Pieces {
		if strings.EqualFold(strings.TrimSpace(p.Name), j.resultBlock) {
			return nil, blockedJoin("result piece name already exists")
		}
		if !j.involves(p.LineKey) {
			if p.FusingZone != nil && p.FusingZone.DxfPieceKey != "" {
				return nil, blockedJoin("unresolved DXF fusing reference")
			}
			continue
		}
		i := slices.Index(keys[:2], p.LineKey)
		if i < 0 || p.Id <= 0 || result.sourceIDs[i] != 0 || !plainJoinSourcePiece(p) {
			return nil, blockedJoin("piece row reused or contains dependent metadata")
		}
		for _, co := range card.Callouts {
			if len(co.PartsRaw) > 0 {
				var parts []string
				if err := json.Unmarshal(co.PartsRaw, &parts); err != nil || !slices.Equal(parts, co.Parts) {
					return nil, blockedJoin("unreadable callout parts")
				}
			}
			if strings.EqualFold(strings.TrimSpace(co.Part.String), p.Name) || slices.ContainsFunc(co.Parts, func(name string) bool { return strings.EqualFold(strings.TrimSpace(name), p.Name) }) {
				return nil, blockedJoin("callout refers to retiring piece")
			}
		}
		result.sourceIDs[i] = p.Id
	}
	if result.sourceIDs[0] == 0 || result.sourceIDs[1] == 0 || result.sourceIDs[0] == result.sourceIDs[1] {
		return nil, blockedJoin("missing or ambiguous piece rows")
	}
	if err := j.checkCardReferences(card, result); err != nil {
		return nil, err
	}
	expected := map[string]int{}
	norm := func(name string) string { return strings.ToLower(strings.Join(strings.Fields(name), " ")) }
	for i, g := range j.inputs {
		if len(g.blockNames) < 1 || len(g.blockNames) > 2 {
			return nil, blockedJoin("missing or unsupported alias group")
		}
		for _, n := range g.blockNames {
			if n == "" || n != strings.TrimSpace(n) || norm(n) == norm(j.resultBlock) {
				return nil, blockedJoin("invalid alias name")
			}
			if _, duplicate := expected[norm(n)]; duplicate {
				return nil, blockedJoin("duplicate alias name")
			}
			expected[norm(n)] = i
		}
	}
	found := map[string]bool{}
	for _, a := range card.PieceDxfAliases {
		scope := entity.FabricScopeIdentity(a.FabricPurpose, a.BomLineKey, lines)
		name := norm(a.BlockName)
		i, matched := expected[name]
		target := j.involves(a.PieceLineKey) || slices.Contains(result.sourceIDs[:], a.PieceId)
		if scope == j.row.ScopeKey && name == norm(j.resultBlock) {
			return nil, blockedJoin("result alias exists")
		}
		if !target && !(scope == j.row.ScopeKey && matched) {
			result.keptAliases = append(result.keptAliases, a)
			continue
		}
		if !matched || scope != j.row.ScopeKey || a.PieceLineKey != keys[i] || a.PieceId != result.sourceIDs[i] ||
			!slices.Contains(j.inputs[i].blockNames, a.BlockName) || found[name] {
			return nil, blockedJoin("source alias changed or shared")
		}
		found[name] = true
	}
	if len(found) != len(expected) {
		return nil, blockedJoin("source alias missing")
	}
	return result, nil
}

// A new or previously unknown persisted field must not disappear along with a source row.
func plainJoinSourcePiece(p entity.TechCardPiece) bool {
	if p.PiecesPerGarment != 1 || !p.CutSymmetry.Valid || p.CutSymmetry.String != string(entity.PieceCutSymmetryIdentical) {
		return false
	}
	allowed := entity.TechCardPiece{Id: p.Id, LineKey: p.LineKey, Name: p.Name, PiecesPerGarment: 1, CutSymmetry: p.CutSymmetry}
	if len(p.Materials) == 0 {
		p.Materials = nil
	}
	if len(p.FusingEdges) == 0 {
		p.FusingEdges = nil
	}
	return reflect.DeepEqual(p, allowed)
}

func (j joinRetirement) checkCardReferences(card *entity.TechCard, r *joinRetirementRows) error {
	for _, op := range card.Operations {
		for _, set := range [][]string{op.InputKeys, op.PieceLineKeys, op.LayerOrder, op.LabelKeys, {op.OutputUnitKey.String, op.AutoFusingPieceKey.String}} {
			if slices.ContainsFunc(set, j.involves) {
				return blockedJoin("Assembly operation references piece")
			}
		}
		for _, input := range op.AssemblyInputs {
			if j.involves(input.Key) {
				return blockedJoin("typed Assembly reference")
			}
		}
		for _, id := range op.PieceIds {
			if slices.Contains(r.sourceIDs[:], id) {
				return blockedJoin("Assembly piece FK")
			}
		}
		for _, run := range op.TopstitchLines {
			if len(run.Parts) == 0 || slices.ContainsFunc(run.PieceLineKeys(), func(key string) bool { return !r.stableAnchors[key] }) {
				return blockedJoin("topstitch reference")
			}
		}
		for _, m := range op.Media {
			for _, a := range m.Annotations {
				if j.involves(a.PieceLineKey) || slices.ContainsFunc(a.PieceLineKeys, j.involves) {
					return blockedJoin("operation annotation")
				}
			}
		}
	}
	for _, label := range card.GarmentLabels {
		if j.involves(label.LineKey) {
			return blockedJoin("garment label line collision")
		}
	}
	for _, strip := range card.CutStrips {
		if j.involves(strip.LineKey) {
			return blockedJoin("strip line collision")
		}
	}
	for _, fold := range card.PieceFolds {
		if !r.stableAnchors[fold.PieceLineKey] {
			return blockedJoin("piece fold")
		}
	}
	for _, cw := range card.Colorways {
		for _, u := range cw.Usages {
			if j.involves(u.PieceLineKey) || (u.PieceId.Valid && slices.Contains(r.sourceIDs[:], int(u.PieceId.Int64))) || u.PieceIndex.Valid {
				return blockedJoin("material assignment or legacy position binding")
			}
		}
	}
	return nil
}

// Checks the piece/alias part of the document delta before commit. The caller must
// also verify the uploaded file, pattern history, and remaining document sections.
func verifyJoinLineDelta(before, after *entity.TechCard, j joinRetirement, plan *joinRetirementRows, resultAlias entity.TechCardPieceDxfAlias) error {
	if before == nil || after == nil || plan == nil || before.Id != j.row.TechCardID || before.LockVersion != j.row.CardLockVersion ||
		after.Id != before.Id || after.LockVersion != before.LockVersion+1 || len(after.Pieces) != len(before.Pieces)-1 ||
		len(after.PieceDxfAliases) != len(plan.keptAliases)+1 {
		return blockedJoin("unexpected committed piece set or document clock")
	}
	if resultAlias.PieceLineKey != j.resultLineKey || resultAlias.BlockName != j.resultBlock || resultAlias.PieceId != 0 ||
		entity.FabricScopeIdentity(resultAlias.FabricPurpose, resultAlias.BomLineKey, entity.RollGoodsLinesOfBom(after.BomItems)) != j.row.ScopeKey {
		return blockedJoin("unbound result alias")
	}
	kept := map[string]entity.TechCardPiece{}
	for _, p := range before.Pieces {
		if !slices.Contains(plan.sourceIDs[:], p.Id) {
			kept[p.LineKey] = p
		}
	}
	afterIDs := map[int]bool{}
	for _, p := range after.Pieces {
		if p.Id <= 0 || afterIDs[p.Id] || slices.Contains(plan.sourceIDs[:], p.Id) {
			return blockedJoin("reused numeric piece identity")
		}
		afterIDs[p.Id] = true
		if p.LineKey == j.resultLineKey {
			if resultAlias.PieceId != 0 || p.Name != j.resultBlock || !plainJoinSourcePiece(p) {
				return blockedJoin("unexpected result piece")
			}
			resultAlias.PieceId = p.Id
			continue
		}
		original, ok := kept[p.LineKey]
		if !ok || !reflect.DeepEqual(original, p) {
			return blockedJoin("changed surviving piece")
		}
		delete(kept, p.LineKey)
	}
	if len(kept) != 0 || resultAlias.PieceId == 0 {
		return blockedJoin("missing result or surviving piece")
	}
	counts := map[entity.TechCardPieceDxfAlias]int{resultAlias: 1}
	for _, a := range plan.keptAliases {
		counts[a]++
	}
	for _, a := range after.PieceDxfAliases {
		if counts[a] == 0 {
			return blockedJoin("changed alias outside the reviewed replacement")
		}
		counts[a]--
	}
	return nil
}
