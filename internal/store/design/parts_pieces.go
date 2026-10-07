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

// ─────────────── PARTS · THE PIECES LIST (M6, flat-consistency 107) ───────────────
//
// One row per card. Two writers, and the second one always wins over the first:
//   - the READ (SavePartsPiecesRead): the model's list of the FRONT/BACK plates. It replaces the list
//     only while no designer saved it; once edited_at is set it lands in `proposal` and the list is
//     left exactly as the designer wrote it. A row that moved since the reader looked is left alone.
//   - the DESIGNER (SetPartsPieces): the whole list under CAS on rev, edited_at stamped.

type piecesRow struct {
	TechCardId int            `db:"tech_card_id"`
	Rev        int            `db:"rev"`
	Pieces     entity.RawJSON `db:"pieces"`
	Front      int            `db:"source_front_media_id"`
	Back       int            `db:"source_back_media_id"`
	Proposal   entity.RawJSON `db:"proposal"`
	Model      string         `db:"model"`
	EditedBy   string         `db:"edited_by"`
	EditedAt   sql.NullTime   `db:"edited_at"`
	UpdatedAt  time.Time      `db:"updated_at"`
}

func (r piecesRow) entity() (entity.DesignPartsPieces, error) {
	out := entity.DesignPartsPieces{
		TechCardId: r.TechCardId, Rev: r.Rev, Front: r.Front, Back: r.Back, Model: r.Model,
		EditedBy: r.EditedBy, EditedAt: r.EditedAt, UpdatedAt: r.UpdatedAt,
	}
	if len(r.Pieces) > 0 {
		if err := json.Unmarshal(r.Pieces, &out.Doc); err != nil {
			return entity.DesignPartsPieces{}, fmt.Errorf("the pieces list of tech card %d does not parse: %w", r.TechCardId, err)
		}
	}
	if out.Doc.Pieces == nil {
		out.Doc.Pieces = []entity.DesignPartsPiece{}
	}
	if len(r.Proposal) > 0 && string(r.Proposal) != "null" {
		var p entity.DesignPartsPiecesProposal
		if err := json.Unmarshal(r.Proposal, &p); err != nil {
			return entity.DesignPartsPieces{}, fmt.Errorf("the pieces proposal of tech card %d does not parse: %w", r.TechCardId, err)
		}
		out.Proposal = &p
	}
	return out, nil
}

// piecesByCard — the card's row, nil when there is none.
func piecesByCard(ctx context.Context, db dependency.DB, cardID int) (*entity.DesignPartsPieces, error) {
	row, err := storeutil.QueryNamedOne[piecesRow](ctx, db, `
		SELECT tech_card_id, rev, pieces, source_front_media_id, source_back_media_id, proposal, model,
		       edited_by, edited_at, updated_at
		FROM design_parts_pieces WHERE tech_card_id = :card`, map[string]any{"card": cardID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the pieces list of tech card %d: %w", cardID, err)
	}
	out, err := row.entity()
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPartsPieces — the card's pieces list; nil when none. ErrDesignNotFound for an unknown card.
func (s *Store) GetPartsPieces(ctx context.Context, cardID int) (*entity.DesignPartsPieces, error) {
	if err := requireCard(cardID); err != nil {
		return nil, err
	}
	if err := refuseUnknownCard(ctx, s.DB, cardID); err != nil {
		return nil, err
	}
	return piecesByCard(ctx, s.DB, cardID)
}

func piecesDocJSON(doc entity.DesignPartsPiecesDoc) (string, error) {
	if doc.Pieces == nil {
		doc.Pieces = []entity.DesignPartsPiece{}
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("failed to encode the pieces list: %w", err)
	}
	return string(b), nil
}

// SavePartsPiecesRead writes the model's read of the FRONT/BACK plates and returns the row as it
// stands after the write:
//   - no row (ExpectedRev 0) → the read is the list (rev 1); a row born meanwhile is returned untouched;
//   - the row moved since the reader saw ExpectedRev → returned untouched (the newer state wins);
//   - a designer edited the list → the read becomes the proposal, the list and rev stay as they are
//     (a proposal changes no name the labeller uses);
//   - else → the read replaces the list, rev + 1, any proposal dropped.
func (s *Store) SavePartsPiecesRead(ctx context.Context, req entity.DesignPartsPiecesRead) (*entity.DesignPartsPieces, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	docJSON, err := piecesDocJSON(req.Doc)
	if err != nil {
		return nil, err
	}
	var out *entity.DesignPartsPieces
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if err := refuseUnknownCard(ctx, db, req.TechCardId); err != nil {
			return err
		}
		before, err := piecesByCard(ctx, db, req.TechCardId)
		if err != nil {
			return err
		}
		args := map[string]any{
			"card": req.TechCardId, "pieces": docJSON, "front": req.Front, "back": req.Back,
			"model": req.Model, "expected": req.ExpectedRev,
		}
		switch {
		case before == nil && req.ExpectedRev == 0:
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO design_parts_pieces (tech_card_id, rev, pieces, source_front_media_id, source_back_media_id, model, updated_at)
				VALUES (:card, 1, :pieces, :front, :back, :model, UTC_TIMESTAMP(6))`, args); err != nil {
				if isDupKey(err) {
					break // another read was first: its row stands
				}
				return fmt.Errorf("failed to create the pieces list: %w", err)
			}
		case before == nil || before.Rev != req.ExpectedRev:
			// The row moved (or vanished) since the reader looked: the newer state wins.
		case before.Edited():
			prop, err := json.Marshal(entity.DesignPartsPiecesProposal{
				Doc: req.Doc, Model: req.Model, Front: req.Front, Back: req.Back, ReadAt: time.Now().UTC(),
			})
			if err != nil {
				return fmt.Errorf("failed to encode the pieces proposal: %w", err)
			}
			args["proposal"] = string(prop)
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE design_parts_pieces SET proposal = :proposal, updated_at = UTC_TIMESTAMP(6)
				WHERE tech_card_id = :card AND rev = :expected`, args); err != nil {
				return fmt.Errorf("failed to save the pieces proposal: %w", err)
			}
		default:
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE design_parts_pieces
				SET rev = rev + 1, pieces = :pieces, source_front_media_id = :front, source_back_media_id = :back,
				    model = :model, proposal = NULL, updated_at = UTC_TIMESTAMP(6)
				WHERE tech_card_id = :card AND rev = :expected AND edited_at IS NULL`, args); err != nil {
				return fmt.Errorf("failed to save the pieces list: %w", err)
			}
		}
		out, err = piecesByCard(ctx, db, req.TechCardId)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetPartsPieces saves the designer's list (CAS on rev; 0 = no row yet) and returns the row. A name
// the list or the proposal already had keeps the views the read saw it on. SettleProposal drops the
// pending proposal and takes its plates and openings as the list's: the designer has answered the
// read of the current flats (take = its names, keep = their own).
func (s *Store) SetPartsPieces(ctx context.Context, req entity.DesignPartsPiecesSave) (*entity.DesignPartsPieces, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	var out *entity.DesignPartsPieces
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if err := refuseUnknownCard(ctx, db, req.TechCardId); err != nil {
			return err
		}
		before, err := piecesByCard(ctx, db, req.TechCardId)
		if err != nil {
			return err
		}
		if before == nil && req.ExpectedRev != 0 || before != nil && before.Rev != req.ExpectedRev {
			at := 0
			if before != nil {
				at = before.Rev
			}
			return fmt.Errorf("%w: the pieces list is at rev %d, %d was echoed",
				entity.ErrDesignPartsPiecesRevMismatch, at, req.ExpectedRev)
		}
		views := map[string][]string{}
		doc := entity.DesignPartsPiecesDoc{}
		front, back, model := 0, 0, ""
		var proposal *entity.DesignPartsPiecesProposal
		if before != nil {
			doc.Openings = before.Doc.Openings
			front, back, model, proposal = before.Front, before.Back, before.Model, before.Proposal
			if proposal != nil {
				for _, p := range proposal.Doc.Pieces {
					views[p.Name] = p.Views
				}
			}
			for _, p := range before.Doc.Pieces {
				views[p.Name] = p.Views
			}
		}
		if req.SettleProposal && proposal != nil {
			doc.Openings, front, back, model = proposal.Doc.Openings, proposal.Front, proposal.Back, proposal.Model
			proposal = nil
		}
		for _, n := range req.Names {
			doc.Pieces = append(doc.Pieces, entity.DesignPartsPiece{Name: n, Views: views[n]})
		}
		docJSON, err := piecesDocJSON(doc)
		if err != nil {
			return err
		}
		args := map[string]any{
			"card": req.TechCardId, "pieces": docJSON, "front": front, "back": back, "model": model,
			"who": req.Actor, "expected": req.ExpectedRev, "settle": req.SettleProposal,
		}
		if before == nil {
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO design_parts_pieces (tech_card_id, rev, pieces, source_front_media_id, source_back_media_id, model, edited_by, edited_at, updated_at)
				VALUES (:card, 1, :pieces, :front, :back, :model, :who, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`, args); err != nil {
				if isDupKey(err) {
					return fmt.Errorf("%w: a pieces list for tech card %d already exists",
						entity.ErrDesignPartsPiecesRevMismatch, req.TechCardId)
				}
				return fmt.Errorf("failed to create the pieces list: %w", err)
			}
		} else {
			n, err := storeutil.ExecNamedRows(ctx, db, `
				UPDATE design_parts_pieces
				SET rev = rev + 1, pieces = :pieces, source_front_media_id = :front, source_back_media_id = :back,
				    model = :model, proposal = IF(:settle, NULL, proposal), edited_by = :who,
				    edited_at = UTC_TIMESTAMP(6), updated_at = UTC_TIMESTAMP(6)
				WHERE tech_card_id = :card AND rev = :expected`, args)
			if err != nil {
				return fmt.Errorf("failed to save the pieces list of tech card %d: %w", req.TechCardId, err)
			}
			if n == 0 {
				return fmt.Errorf("%w: the pieces list moved past rev %d", entity.ErrDesignPartsPiecesRevMismatch, req.ExpectedRev)
			}
		}
		out, err = piecesByCard(ctx, db, req.TechCardId)
		if err == nil && out == nil {
			err = fmt.Errorf("failed to read back the pieces list of tech card %d", req.TechCardId)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
