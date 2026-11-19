package techcard

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/patternjoin"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"reflect"
	"slices"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type lockedNativeJoin struct {
	before  *entity.TechCard
	want    joinRetirement
	plan    *joinRetirementRows
	alias   entity.TechCardPieceDxfAlias
	command *entity.GuardedNativePieceJoin
}

func prepareLockedNativeJoin(ctx context.Context, rep dependency.Repository, card *entity.TechCard, tc *entity.TechCardInsert) (*lockedNativeJoin, error) {
	j := tc.GuardedPieceJoin
	if j == nil || tc.GuardedPieceRename != nil {
		return nil, blockedJoin("missing or conflicting native verification")
	}
	if !slices.Contains(card.SizeIds, j.Selection.SizeID) {
		return nil, blockedJoin("selected size is outside the saved card range")
	}
	size, err := storeutil.QueryNamedOne[struct {
		Name string `db:"name"`
	}](ctx, rep.DB(), "SELECT name FROM size WHERE id=:id FOR SHARE", map[string]any{"id": j.Selection.SizeID})
	if err != nil {
		return nil, err
	}
	if strings.ToUpper(size.Name) != j.Selection.SizeToken {
		return nil, blockedJoin("selected rank differs from current size dictionary")
	}
	want, err := nativeJoinRetirement(card, j.Document, j.Selection)
	if err != nil {
		return nil, err
	}
	// The writer may have normalized the proposed alias, so compare its actual persisted form.
	var found []entity.TechCardPieceDxfAlias
	for _, a := range tc.PieceDxfAliases {
		if a.PieceLineKey == want.resultLineKey {
			found = append(found, a)
		}
	}
	if len(found) != 1 || found[0].BlockName != want.resultBlock {
		return nil, blockedJoin("reviewed result alias missing")
	}
	var plan *joinRetirementRows
	check := func(ctx context.Context, d entity.JoinPatternDocument, s entity.JoinBoundarySelection) error {
		if d != j.Document || s != j.Selection {
			return blockedJoin("signed intent changed")
		}
		// The transaction has held this card lock since before the current card projection.
		plan, err = readJoinRetirementUnderLock(ctx, rep, want)
		return err
	}
	managed, ok := j.ManagedPatterns.(*bucket.Bucket)
	if !ok || managed == nil || managed.Config == nil {
		return nil, blockedJoin("missing managed native bucket")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signature, err := patternjoin.SignNativeLineage(ctx, managed, managed.Config, j.Document, j.Reviewed, j.Selection, check, private)
	if err == nil {
		err = patternjoin.MatchNativeLineage(ctx, managed, managed.Config, j.Document, j.Reviewed, j.Selection, check, public, signature)
	}
	if err != nil {
		return nil, fmt.Errorf("native lineage verification: %w: %w", entity.ErrTechCardSavedAssemblyUnsupported, err)
	}
	if plan == nil {
		return nil, blockedJoin("native verification did not check the locked document")
	}
	return &lockedNativeJoin{card, want, plan, found[0], j}, nil
}

func (l *lockedNativeJoin) verify(after *entity.TechCard) error {
	before := l.before
	if err := verifyJoinLineDelta(before, after, l.want, l.plan, l.alias); err != nil {
		return err
	}
	if len(after.Patterns) != len(before.Patterns) {
		return blockedJoin("changed pattern row count")
	}
	others := make(map[string]entity.TechCardSizePattern, len(before.Patterns))
	for _, p := range before.Patterns {
		others[p.LineKey] = p
	}
	for _, p := range after.Patterns {
		old, ok := others[p.LineKey]
		if !ok {
			return blockedJoin("changed pattern row identity")
		}
		delete(others, p.LineKey)
		old.SavedRowRevision++
		if p.LineKey == l.want.row.PatternLineKey {
			if p.URL != l.command.Reviewed.URL || p.Filename.String != l.command.Filename || !p.Filename.Valid || !p.SizeBytes.Valid || p.SizeBytes.Int64 != l.command.Reviewed.Size ||
				!p.SHA256.Valid || p.SHA256.String != l.command.Reviewed.SHA256 || p.Version <= old.Version || !p.UploadedAt.Valid || len(p.History) != len(old.History)+1 {
				return blockedJoin("replacement pattern or history differs")
			}
			h := p.History[0]
			if h.URL != old.URL || h.Filename != old.Filename || h.SHA256 != old.SHA256 || h.Revision != old.Version || h.HistoryID <= 0 || !h.SnapshotID.Valid || !slices.EqualFunc(p.History[1:], old.History, func(a, b entity.TechCardPatternRevision) bool { return reflect.DeepEqual(a, b) }) {
				return blockedJoin("original pattern did not reach ordinary saved history and snapshot")
			}
			old.URL, old.Filename, old.SizeBytes, old.SHA256 = p.URL, p.Filename, p.SizeBytes, p.SHA256
			old.UploadedAt, old.Version, old.History = p.UploadedAt, p.Version, p.History
		}
		if !reflect.DeepEqual(old, p) {
			return blockedJoin("unreviewed pattern data changed")
		}
	}
	if len(others) != 0 {
		return blockedJoin("missing original pattern row")
	}
	// Everything outside the reviewed projection must transport through the ordinary writer.
	// Numeric IDs recreated by ordinary writers are considered separately where warranted.
	a, b := *before, *after
	a.LockVersion, a.UpdatedAt, a.UpdatedBy = b.LockVersion, b.UpdatedAt, b.UpdatedBy
	a.Pieces, a.Patterns, a.PieceDxfAliases = b.Pieces, b.Patterns, b.PieceDxfAliases
	if !reflect.DeepEqual(a, b) {
		return blockedJoin("unreviewed card data changed")
	}
	return nil
}
