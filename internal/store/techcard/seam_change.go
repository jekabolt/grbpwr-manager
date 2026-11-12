package techcard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ONE SEAM GESTURE, ONE WRITE (ChangeTechCardSeams). The old seam RPCs (seams.go) stay as they are
// and never touch lock_version; this path is the opposite on purpose: rows and step links move in ONE
// transaction under the card's optimistic lock, so a gesture either lands whole or not at all, and a
// form that read the card before it cannot save over it.
//
// Order inside the transaction: card row FOR UPDATE (first action, before any read — see
// UpsertTechCardSeams) → released refusal → lock_version equals expected → steps resolved → upsert
// (a new key of a pair already on the card or earlier in the request goes onto that row's key) →
// link → detach → delete → the caps checked on the state all of that left → one lock_version bump if
// anything changed → the response read in the same transaction.

// ChangeTechCardSeams applies one seam gesture atomically and returns what was committed.
func (s *Store) ChangeTechCardSeams(ctx context.Context, in entity.TechCardSeamsChange) (entity.TechCardSeamsChangeResult, error) {
	// Re-validated here and not only trusted from the handler: the store is the last gate.
	if err := entity.ValidateTechCardSeamsChange(&in); err != nil {
		return entity.TechCardSeamsChangeResult{}, err
	}
	var out entity.TechCardSeamsChangeResult
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Assigned, never appended — txFunc may retry this closure.
		res, err := changeTechCardSeamsTx(ctx, rep.DB(), in)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	if err != nil {
		return entity.TechCardSeamsChangeResult{}, err
	}
	return out, nil
}

func changeTechCardSeamsTx(ctx context.Context, db dependency.DB, in entity.TechCardSeamsChange) (entity.TechCardSeamsChangeResult, error) {
	var zero entity.TechCardSeamsChangeResult
	id := in.TechCardId

	version, err := lockTechCardRowVersion(ctx, db, id)
	if err != nil {
		return zero, err
	}
	if err := storeutil.RequireMutableTechCard(ctx, db, id); err != nil {
		return zero, err
	}
	if version != *in.ExpectedLockVersion {
		return zero, entity.ErrTechCardLockVersionMoved
	}

	// Steps by number. operation_number has no UNIQUE in the schema: a number on two steps is
	// refused, never guessed. A step without a number (a draft fusing step before the next save) is
	// not addressable.
	ops, err := storeutil.QueryListNamed[struct {
		Id     int `db:"id"`
		Number int `db:"operation_number"`
	}](ctx, db, `
		SELECT id, operation_number FROM tech_card_operation
		WHERE tech_card_id = :id AND operation_number IS NOT NULL`, map[string]any{"id": id})
	if err != nil {
		return zero, fmt.Errorf("load steps of tech card %d: %w", id, err)
	}
	opsByNumber := make(map[int][]int, len(ops))
	for _, o := range ops {
		opsByNumber[o.Number] = append(opsByNumber[o.Number], o.Id)
	}
	stepOf := func(field string, links []entity.TechCardSeamLink) ([]int, error) {
		out := make([]int, len(links))
		for i, l := range links {
			f := fmt.Sprintf("%s[%d].operation_number", field, i)
			switch ids := opsByNumber[l.OperationNumber]; len(ids) {
			case 0:
				return nil, entity.NewFieldViolation(f, "unknown_operation", "",
					"no step with this number on the card — reload the card")
			case 1:
				out[i] = ids[0]
			default:
				return nil, entity.NewFieldViolation(f, "operation_not_unique", "",
					"two steps carry this number — save the card to renumber them")
			}
		}
		return out, nil
	}
	linkOps, err := stepOf("link", in.Link)
	if err != nil {
		return zero, err
	}
	detachOps, err := stepOf("detach", in.Detach)
	if err != nil {
		return zero, err
	}

	// REKEY — one pair and ranges, one row, as the client's reuseKeys does. A sent key that is not
	// stored and names a non-pose row goes onto the row already holding its pair and ranges: first a
	// stored row (stored order, first wins), judged by what it holds AFTER this change — a stored row
	// the upsert rewrites is a target by its new content, not its old — then an earlier new row of
	// this request. Rows of the same request that land on one key are written once: the row sent
	// under that key's own name gives the content, else the first of them; key_map and the links of
	// every sent key follow the row they landed on. Rows this change deletes or detaches are no
	// target: landing a new row on one would delete or detach it in the same breath.
	stored, err := storeutil.QueryListNamed[struct {
		SeamKey string `db:"seam_key"`
		Kind    string `db:"kind"`
		SideA   []byte `db:"side_a"`
		SideB   []byte `db:"side_b"`
	}](ctx, db, `
		SELECT seam_key, kind, side_a, side_b FROM tech_card_seam
		WHERE tech_card_id = :id ORDER BY id`, map[string]any{"id": id})
	if err != nil {
		return zero, fmt.Errorf("load seams of tech card %d: %w", id, err)
	}
	excluded := make(map[string]bool, len(in.Delete)+len(in.Detach))
	for _, k := range in.Delete {
		excluded[k] = true
	}
	for _, d := range in.Detach {
		excluded[d.SeamKey] = true
	}
	sentAt := make(map[string]int, len(in.Upsert)) // sent keys are unique (ValidateTechCardSeamsWrite)
	for i, u := range in.Upsert {
		sentAt[u.SeamKey] = i
	}
	storedKeys := make(map[string]bool, len(stored))
	bySig := map[string]string{}
	for _, r := range stored {
		key := entity.NormalizeTechCardSeamKey(r.SeamKey)
		storedKeys[key] = true
		if excluded[key] {
			continue
		}
		var row entity.TechCardSeamInput
		if i, rewritten := sentAt[key]; rewritten {
			row = in.Upsert[i]
		} else {
			row = entity.TechCardSeamInput{Kind: entity.TechCardSeamKind(r.Kind)}
			if json.Unmarshal(r.SideA, &row.SideA) != nil || json.Unmarshal(r.SideB, &row.SideB) != nil {
				continue // a corrupted row is offered for re-confirm or delete, never a rekey target
			}
		}
		if row.Kind == entity.TechCardSeamKindPose {
			continue
		}
		if sig := entity.TechCardSeamPairRangeSig(row); bySig[sig] == "" {
			bySig[sig] = key
		}
	}
	upsert := make([]entity.TechCardSeamInput, 0, len(in.Upsert))
	keyMap := make([]entity.TechCardSeamKeyMap, 0, len(in.Upsert))
	alias := map[string]string{}
	rowAt := make(map[string]int, len(in.Upsert)) // stored key → its row in upsert
	for _, u := range in.Upsert {
		sent := u.SeamKey
		key := sent
		if !storedKeys[sent] && u.Kind != entity.TechCardSeamKindPose {
			sig := entity.TechCardSeamPairRangeSig(u)
			if k := bySig[sig]; k != "" {
				key = k
			} else {
				bySig[sig] = sent // a later new row of this pair lands here
			}
		}
		keyMap = append(keyMap, entity.TechCardSeamKeyMap{Sent: sent, Stored: key})
		if key != sent {
			alias[sent] = key
		}
		u.SeamKey = key
		if j, landed := rowAt[key]; landed {
			if sent == key {
				upsert[j] = u
			}
			continue
		}
		rowAt[key] = len(upsert)
		upsert = append(upsert, u)
	}
	resolve := func(links []entity.TechCardSeamLink) []entity.TechCardSeamLink {
		out := make([]entity.TechCardSeamLink, len(links))
		for i, l := range links {
			if k, ok := alias[l.SeamKey]; ok {
				l.SeamKey = k
			}
			out[i] = l
		}
		return out
	}
	link, detach := resolve(in.Link), resolve(in.Detach)
	// Checked again on the stored keys: two sent keys may name one row now.
	if err := entity.TechCardSeamLinksContradiction(link, detach, in.Delete); err != nil {
		return zero, err
	}

	changed := false
	var deleted []string
	noteDeleted := func(k string) {
		for _, d := range deleted {
			if d == k {
				return
			}
		}
		deleted = append(deleted, k)
	}

	// UPSERT — the reviewer's write with piece membership; the per-card cap waits for the final state
	// (below): a MIRROR at the cap adds its row before its detach takes the replaced one out.
	if len(upsert) > 0 {
		if err := writeTechCardSeamsTx(ctx, db, id, upsert, in.By, seamWriteOpts{capChecked: true}); err != nil {
			return zero, entity.RenameSeamFieldPrefix(err, "seams", "upsert")
		}
		changed = true
	}

	// LINK — appended to the step; a pair already linked is a no-op. The key must be a row of the card
	// after the upsert (checked here, after the write, so a refusal rolls the upsert back with it).
	// The per-step cap waits for the final state, as the per-card cap does.
	type gained struct{ op, before, at int } // a step that gained a link: its count before, the first link[i]
	var grown []gained
	grownAt := map[int]bool{}
	for i, l := range link {
		if _, upserted := rowAt[l.SeamKey]; !storedKeys[l.SeamKey] && !upserted {
			return zero, entity.NewFieldViolation(fmt.Sprintf("link[%d].seam_key", i), "unknown_seam", "",
				"seam "+l.SeamKey+" is not stored on this card — upsert it in the same change, or reload: it may have been deleted")
		}
		had, inserted, err := linkSeamToOperationTx(ctx, db, linkOps[i], l.SeamKey)
		if err != nil {
			return zero, err
		}
		if inserted {
			if !grownAt[linkOps[i]] {
				grownAt[linkOps[i]] = true
				grown = append(grown, gained{op: linkOps[i], before: had, at: i})
			}
			changed = true
		}
	}

	// DETACH — the link goes; the row goes too only when THIS change removed a link and no step links
	// it any more. An absent link is a no-op: a review row never linked to a step stays.
	for i, d := range detach {
		removed, err := storeutil.ExecNamedRows(ctx, db, `
			DELETE FROM tech_card_operation_seam WHERE operation_id = :op AND seam_key = :k`,
			map[string]any{"op": detachOps[i], "k": d.SeamKey})
		if err != nil {
			return zero, fmt.Errorf("detach seam %s from a step of tech card %d: %w", d.SeamKey, id, err)
		}
		if removed == 0 {
			continue
		}
		changed = true
		left, err := countSeamLinksTx(ctx, db, id, d.SeamKey)
		if err != nil {
			return zero, err
		}
		if left > 0 {
			continue
		}
		// A row this change wrote would go again right here: upsert + detach of its last link is the
		// upsert + delete the request validation refuses, only it shows on the stored links. Refused
		// whole (the transaction rolls the upsert and the detach back). A move keeps working: link the
		// new step in the same change and the detach leaves a link behind.
		if _, upserted := rowAt[d.SeamKey]; upserted {
			at := 0
			for j, m := range keyMap { // keyMap runs in request order: the first upsert landing on the row
				if m.Stored == d.SeamKey {
					at = j
					break
				}
			}
			return zero, entity.NewFieldViolation(fmt.Sprintf("detach[%d]", i), "contradicts_upsert",
				fmt.Sprintf("upsert[%d]", at),
				"this detach takes the last link of a row the same change writes, so the row would be deleted — link it to a step or drop the upsert")
		}
		n, err := deleteTechCardSeamsTx(ctx, db, id, []string{d.SeamKey})
		if err != nil {
			return zero, err
		}
		if n > 0 {
			noteDeleted(d.SeamKey)
		}
	}

	// DELETE — unconditional, and it unlinks the key from every step (THE SEAM DELETE).
	for _, k := range in.Delete {
		links, err := countSeamLinksTx(ctx, db, id, k)
		if err != nil {
			return zero, err
		}
		n, err := deleteTechCardSeamsTx(ctx, db, id, []string{k})
		if err != nil {
			return zero, err
		}
		if n > 0 || links > 0 {
			changed = true
		}
		if n > 0 {
			noteDeleted(k)
		}
	}

	// THE CAPS, ON THE FINAL STATE — 400 rows per card, 64 links per step — after detach and delete,
	// so a gesture that replaces at the cap lands and one that ends over it is refused whole. Only a
	// change that GROWS a count past its cap is refused: a delete or a replace is never blocked by it.
	if len(upsert) > 0 {
		after, err := storeutil.QueryCountNamed(ctx, db, `
			SELECT COUNT(*) FROM tech_card_seam WHERE tech_card_id = :id`, map[string]any{"id": id})
		if err != nil {
			return zero, fmt.Errorf("count seams of tech card %d: %w", id, err)
		}
		if after > entity.TechCardSeamMaxRowsPerCard && after > len(stored) {
			return zero, entity.NewFieldViolation("upsert", "too_many", "",
				fmt.Sprintf("a card holds at most %d seams; delete orphan or obsolete rows first", entity.TechCardSeamMaxRowsPerCard))
		}
	}
	for _, g := range grown { // request order: the first step over its cap is the one named
		after, err := storeutil.QueryCountNamed(ctx, db, `
			SELECT COUNT(*) FROM tech_card_operation_seam WHERE operation_id = :op`, map[string]any{"op": g.op})
		if err != nil {
			return zero, fmt.Errorf("count seam links of step %d: %w", g.op, err)
		}
		if after > entity.TechCardOperationMaxSeamKeys && after > g.before {
			return zero, entity.NewFieldViolation(fmt.Sprintf("link[%d]", g.at), "too_many", "",
				fmt.Sprintf("a step links at most %d seams", entity.TechCardOperationMaxSeamKeys))
		}
	}

	// ONE bump per change that changed anything; a no-op change leaves the version alone.
	newVersion := version
	if changed {
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE tech_card SET lock_version = lock_version + 1 WHERE id = :id`,
			map[string]any{"id": id}); err != nil {
			return zero, fmt.Errorf("bump lock_version of tech card %d: %w", id, err)
		}
		newVersion = version + 1
	}

	// The response, read in the same transaction: what was committed, not the request echoed.
	seams, err := listTechCardSeams(ctx, db, id)
	if err != nil {
		return zero, err
	}
	links, err := loadNumberedStepSeamKeys(ctx, db, id)
	if err != nil {
		return zero, err
	}
	if deleted == nil {
		deleted = []string{}
	}
	return entity.TechCardSeamsChangeResult{
		Seams:       seams,
		Links:       links,
		LockVersion: newVersion,
		KeyMap:      keyMap,
		DeletedKeys: deleted,
	}, nil
}

// lockTechCardRowVersion takes the card row FOR UPDATE (as lockTechCardRow) and returns its
// lock_version under that lock. A missing card is sql.ErrNoRows.
func lockTechCardRowVersion(ctx context.Context, db dependency.DB, techCardID int) (int, error) {
	row, err := storeutil.QueryNamedOne[struct {
		LockVersion int `db:"lock_version"`
	}](ctx, db, `SELECT lock_version FROM tech_card WHERE id = :id FOR UPDATE`,
		map[string]any{"id": techCardID})
	if err != nil {
		return 0, fmt.Errorf("lock tech card %d: %w", techCardID, err)
	}
	return row.LockVersion, nil
}

// linkSeamToOperationTx appends key to the step's seams and returns the step's link count before it;
// inserted is false when the step already links key. The per-step cap
// (entity.TechCardOperationMaxSeamKeys) is the caller's, on its final state.
func linkSeamToOperationTx(ctx context.Context, db dependency.DB, opID int, key string) (had int, inserted bool, err error) {
	st, err := storeutil.QueryNamedOne[struct {
		N        int `db:"n"`
		MaxOrder int `db:"max_order"`
		Has      int `db:"has"`
	}](ctx, db, `
		SELECT COUNT(*) AS n, COALESCE(MAX(display_order), -1) AS max_order,
		       COALESCE(SUM(seam_key = :k), 0) AS has
		FROM tech_card_operation_seam WHERE operation_id = :op`,
		map[string]any{"op": opID, "k": key})
	if err != nil {
		return 0, false, fmt.Errorf("load seam links of step %d: %w", opID, err)
	}
	if st.Has > 0 {
		return st.N, false, nil
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_operation_seam (operation_id, seam_key, display_order)
		VALUES (:op, :k, :pos)`,
		map[string]any{"op": opID, "k": key, "pos": st.MaxOrder + 1}); err != nil {
		return 0, false, fmt.Errorf("link seam %s to step %d: %w", key, opID, err)
	}
	return st.N, true, nil
}

// countSeamLinksTx counts the steps of the card that link key.
func countSeamLinksTx(ctx context.Context, db dependency.DB, techCardID int, key string) (int, error) {
	n, err := storeutil.QueryCountNamed(ctx, db, `
		SELECT COUNT(*) FROM tech_card_operation_seam l
		JOIN tech_card_operation o ON o.id = l.operation_id
		WHERE o.tech_card_id = :id AND l.seam_key = :k`,
		map[string]any{"id": techCardID, "k": key})
	if err != nil {
		return 0, fmt.Errorf("count step links of seam %s on tech card %d: %w", key, techCardID, err)
	}
	return n, nil
}

// loadNumberedStepSeamKeys returns every numbered step of the card with its linked keys in sewing
// order, steps in the card read's order (operation_number, then display_order).
func loadNumberedStepSeamKeys(ctx context.Context, db dependency.DB, techCardID int) ([]entity.TechCardOperationSeamKeys, error) {
	rows, err := storeutil.QueryListNamed[struct {
		OpID    int            `db:"op_id"`
		Number  int            `db:"operation_number"`
		SeamKey sql.NullString `db:"seam_key"`
	}](ctx, db, `
		SELECT o.id AS op_id, o.operation_number AS operation_number, l.seam_key AS seam_key
		FROM tech_card_operation o
		LEFT JOIN tech_card_operation_seam l ON l.operation_id = o.id
		WHERE o.tech_card_id = :id AND o.operation_number IS NOT NULL
		ORDER BY o.operation_number, o.display_order, o.id, l.display_order, l.id`,
		map[string]any{"id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("load step seam links of tech card %d: %w", techCardID, err)
	}
	out := []entity.TechCardOperationSeamKeys{}
	lastOp := 0
	for _, r := range rows {
		if len(out) == 0 || r.OpID != lastOp {
			out = append(out, entity.TechCardOperationSeamKeys{OperationNumber: r.Number, SeamKeys: []string{}})
			lastOp = r.OpID
		}
		if r.SeamKey.Valid {
			last := &out[len(out)-1]
			last.SeamKeys = append(last.SeamKeys, r.SeamKey.String)
		}
	}
	return out, nil
}
