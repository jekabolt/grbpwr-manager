package design

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ─────────────── FLAT ROUTE · THE JOIN LIST (0397) ───────────────
//
// One current row per card. Two writers: the generator (GenerateDesignJoins — no CAS, the model's
// answer replaces the row, edited_at cleared) and the designer (SetDesignJoins — CAS on rev, edited_at
// stamped). Both bump rev, so a designer's tab that read the list before a regeneration is refused.

type joinsRow struct {
	Id                int            `db:"id"`
	TechCardId        int            `db:"tech_card_id"`
	Rev               int            `db:"rev"`
	Joins             entity.RawJSON `db:"joins"`
	Consistency       entity.RawJSON `db:"consistency"`
	Model             string         `db:"model"`
	SourceFingerprint string         `db:"source_fingerprint"`
	EditedBy          string         `db:"edited_by"`
	EditedAt          sql.NullTime   `db:"edited_at"`
	CreatedAt         time.Time      `db:"created_at"`
}

func (r joinsRow) entity() (entity.DesignJoins, error) {
	out := entity.DesignJoins{
		Id: r.Id, TechCardId: r.TechCardId, Rev: r.Rev, Model: r.Model,
		SourceFingerprint: r.SourceFingerprint, EditedBy: r.EditedBy, EditedAt: r.EditedAt, CreatedAt: r.CreatedAt,
	}
	if len(r.Joins) > 0 {
		if err := json.Unmarshal(r.Joins, &out.Doc); err != nil {
			return entity.DesignJoins{}, fmt.Errorf("the join list of tech card %d does not parse: %w", r.TechCardId, err)
		}
	}
	if out.Doc.Items == nil {
		out.Doc.Items = []entity.DesignJoinItem{}
	}
	if len(r.Consistency) > 0 && string(r.Consistency) != "null" {
		if err := json.Unmarshal(r.Consistency, &out.Consistency); err != nil {
			return entity.DesignJoins{}, fmt.Errorf("the photos verdict of tech card %d does not parse: %w", r.TechCardId, err)
		}
	}
	return out, nil
}

// joinsByCard — the card's row, nil when there is none.
func joinsByCard(ctx context.Context, db dependency.DB, cardID int) (*entity.DesignJoins, error) {
	row, err := storeutil.QueryNamedOne[joinsRow](ctx, db, `
		SELECT id, tech_card_id, rev, joins, consistency, model, source_fingerprint, edited_by, edited_at, created_at
		FROM design_joins WHERE tech_card_id = :card`, map[string]any{"card": cardID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the join list of tech card %d: %w", cardID, err)
	}
	out, err := row.entity()
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetJoins — the card's current join list; nil when none. ErrDesignNotFound for an unknown card.
func (s *Store) GetJoins(ctx context.Context, cardID int) (*entity.DesignJoins, error) {
	if err := requireCard(cardID); err != nil {
		return nil, err
	}
	if err := refuseUnknownCard(ctx, s.DB, cardID); err != nil {
		return nil, err
	}
	return joinsByCard(ctx, s.DB, cardID)
}

// SaveJoins writes the card's list. req.ExpectedRev < 0 — the generator's write (replace, rev + 1);
// ≥ 0 — the designer's write under CAS (0 = no row yet), ErrDesignJoinsRevMismatch on a stale rev.
func (s *Store) SaveJoins(ctx context.Context, req entity.DesignJoinsSave) (*entity.DesignJoins, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	doc := req.Doc
	if doc.Items == nil {
		doc.Items = []entity.DesignJoinItem{}
	}
	docJSON, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("failed to encode the join list: %w", err)
	}
	consJSON, err := json.Marshal(req.Consistency)
	if err != nil {
		return nil, fmt.Errorf("failed to encode the photos verdict: %w", err)
	}
	fp := req.SourceFingerprint
	if len(fp) > entity.DesignJoinsMaxFingerprint {
		fp = fp[:entity.DesignJoinsMaxFingerprint]
	}
	args := map[string]any{
		"card": req.TechCardId, "joins": string(docJSON), "cons": string(consJSON), "model": req.Model,
		"fp": fp, "who": req.Actor, "edited": req.Edited, "expected": req.ExpectedRev,
	}

	var out entity.DesignJoins
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if err := refuseUnknownCard(ctx, db, req.TechCardId); err != nil {
			return err
		}
		before, err := joinsByCard(ctx, db, req.TechCardId)
		if err != nil {
			return err
		}
		if before == nil {
			if req.ExpectedRev > 0 {
				return fmt.Errorf("%w: tech card %d has no join list yet, that is rev 0, %d was echoed",
					entity.ErrDesignJoinsRevMismatch, req.TechCardId, req.ExpectedRev)
			}
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO design_joins (tech_card_id, rev, joins, consistency, model, source_fingerprint, edited_by, edited_at)
				VALUES (:card, 1, :joins, :cons, :model, :fp, IF(:edited, :who, ''), IF(:edited, UTC_TIMESTAMP(6), NULL))`,
				args); err != nil {
				if isDupKey(err) {
					return fmt.Errorf("%w: a join list for tech card %d already exists",
						entity.ErrDesignJoinsRevMismatch, req.TechCardId)
				}
				return fmt.Errorf("failed to create the join list: %w", err)
			}
		} else {
			q := `
				UPDATE design_joins
				SET rev = rev + 1, joins = :joins, consistency = :cons, model = :model, source_fingerprint = :fp,
				    edited_by = IF(:edited, :who, ''), edited_at = IF(:edited, UTC_TIMESTAMP(6), NULL),
				    created_at = IF(:edited, created_at, UTC_TIMESTAMP(6))
				WHERE tech_card_id = :card`
			if req.ExpectedRev >= 0 {
				q += ` AND rev = :expected`
			}
			n, err := storeutil.ExecNamedRows(ctx, db, q, args)
			if err != nil {
				return fmt.Errorf("failed to save the join list of tech card %d: %w", req.TechCardId, err)
			}
			if n == 0 {
				return fmt.Errorf("%w: the join list is at rev %d, %d was echoed",
					entity.ErrDesignJoinsRevMismatch, before.Rev, req.ExpectedRev)
			}
		}
		j, err := joinsByCard(ctx, db, req.TechCardId)
		if err != nil {
			return err
		}
		if j == nil {
			return fmt.Errorf("failed to read back the join list of tech card %d", req.TechCardId)
		}
		out = *j
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
