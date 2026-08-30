package design

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// SetReferenceRole states WHICH SIDE of the garment a reference image is about, and in what order
// it is fed to the model.
//
// AN EMPTY ROLE CLEARS IT — the row is deleted and the response carries no reference. «No side
// stated» is a real answer and must not need a second verb, and a row that exists only to say
// «nothing» would then have to be told apart from a row that was never written.
//
// THE ROLE LIVES IN THE BAND, NOT ON THE CARD'S MEDIA ROW, and that is forced: tech_card_media has
// no row key at all — it is rewritten whole by every card save — so there would be nothing to
// carry the attribute onto the resent row.
func (s *Store) SetReferenceRole(ctx context.Context, req entity.DesignReferenceRole) (*entity.DesignReference, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.MediaId <= 0 {
		return nil, fmt.Errorf("%w: a reference role needs a media id", entity.ErrDesignInvalidArgument)
	}
	if req.Role != "" && !entity.IsDesignGhostView(req.Role) {
		return nil, fmt.Errorf("%w: unknown reference role %q", entity.ErrDesignInvalidArgument, req.Role)
	}
	var out *entity.DesignReference
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		out = nil
		db := rep.DB()

		// The image must be one the CARD holds. Without this a role could be pinned onto any file
		// in the installation, and the prompt would then feed the model a picture that belongs to
		// somebody else's card.
		held, err := storeutil.QueryCountNamed(ctx, db,
			`SELECT COUNT(*) FROM tech_card_media WHERE tech_card_id = :card AND media_id = :media`,
			map[string]any{"card": req.TechCardId, "media": req.MediaId})
		if err != nil {
			return fmt.Errorf("failed to check tech card media %d: %w", req.MediaId, err)
		}
		if held == 0 {
			return fmt.Errorf("%w: media %d is not held by tech card %d",
				entity.ErrDesignForeignMedia, req.MediaId, req.TechCardId)
		}

		if req.Role == "" {
			if err := storeutil.ExecNamed(ctx, db,
				`DELETE FROM design_reference WHERE tech_card_id = :card AND media_id = :media`,
				map[string]any{"card": req.TechCardId, "media": req.MediaId}); err != nil {
				return fmt.Errorf("failed to clear design reference role: %w", err)
			}
			return nil
		}

		// THE NOTE IS WRITTEN BY THIS UPSERT AND BY NO OTHER (0348, W-3). It lives on this row, so
		// a verb of its own would be a second write over the same key that could half-succeed —
		// leaving a role stated with somebody else's words next to it.
		//
		// AN EMPTY NOTE CLEARS IT — the column goes to NULL. That is not the rule `role` follows,
		// and the asymmetry is deliberate: a note is text, and empty text is a real answer for it,
		// while an empty role deletes the row above (see the branch before this one).
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO design_reference (tech_card_id, media_id, role, note, ordinal, set_by, set_at)
			VALUES (:card, :media, :role, :note, :ord, :who, UTC_TIMESTAMP(6))
			ON DUPLICATE KEY UPDATE
				role = VALUES(role),
				-- IF, А НЕ VALUES(note) — когда вызывающий про записку ничего не сказал, колонка
				-- обязана остаться КАК БЫЛА. Ветвиться в Go двумя разными запросами здесь нельзя,
				-- это два писателя одной строки, и они разойдутся на первой же правке одного.
				--
				-- ⚠ В КОММЕНТАРИИ ВНУТРИ ИМЕНОВАННОГО ЗАПРОСА НЕ СТАВИТЬ ДВОЕТОЧИЕ. sqlx разбирает
				-- текст ДО того, как MySQL увидит комментарий, и «двоеточие плюс пробел» читает как
				-- параметр с ПУСТЫМ именем. Запрос падает на связывании с «could not find name»,
				-- то есть не на синтаксисе SQL, а там, где искать не станешь.
				note = IF(:note_omitted, note, VALUES(note)),
				ordinal = VALUES(ordinal),
				set_by = VALUES(set_by), set_at = VALUES(set_at)`,
			map[string]any{
				"card": req.TechCardId, "media": req.MediaId, "role": req.Role,
				// На ВСТАВКЕ omitted даёт NULL, и это верно: у новорождённой строки записки нет.
				"note":         nullStr(strings.TrimSpace(req.Note)),
				"note_omitted": req.NoteOmitted,
				"ord":          req.Ordinal, "who": req.Actor,
			}); err != nil {
			return fmt.Errorf("failed to set design reference role: %w", err)
		}
		rows, err := storeutil.QueryListNamed[entity.DesignReference](ctx, db,
			`SELECT * FROM design_reference WHERE tech_card_id = :card AND media_id = :media`,
			map[string]any{"card": req.TechCardId, "media": req.MediaId})
		if err != nil {
			return fmt.Errorf("failed to read design reference: %w", err)
		}
		if len(rows) > 0 {
			r := rows[0]
			out = &r
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RecordSheetIssue writes a printed/shared line into a version's append-only journal. It mints
// nothing: reprinting a sheet is a line here, and that distinction is the whole reason the
// journal exists.
//
// client_request_id is UNIQUE on the table, so a retried print does not double the line. `minted`
// is written by the mint itself and is refused here — otherwise a version could acquire a second
// birth certificate.
func (s *Store) RecordSheetIssue(ctx context.Context, req entity.DesignSheetIssueRecord) (*entity.DesignSheetIssue, error) {
	if err := requireCard(req.TechCardId); err != nil {
		return nil, err
	}
	if req.Action != entity.DesignIssuePrinted && req.Action != entity.DesignIssueShared {
		return nil, fmt.Errorf("%w: journal action %q is not printed or shared",
			entity.ErrDesignInvalidArgument, req.Action)
	}
	var out entity.DesignSheetIssue
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		versionID, err := storeutil.QueryCountNamed(ctx, db, `
			SELECT COALESCE(MAX(id), 0) FROM design_sheet_version
			WHERE tech_card_id = :card AND version_number = :n`,
			map[string]any{"card": req.TechCardId, "n": req.VersionNumber})
		if err != nil {
			return fmt.Errorf("failed to resolve design sheet version: %w", err)
		}
		if versionID == 0 {
			return fmt.Errorf("%w: tech card %d has no sheet version %d",
				entity.ErrDesignNotFound, req.TechCardId, req.VersionNumber)
		}
		// LAST_INSERT_ID(id) in the duplicate branch is what makes the read-back address the row
		// that ALREADY EXISTS rather than the newest line of the version. Reading «the last issue
		// of this version» instead would hand a retried print somebody else's share line.
		issueID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO design_sheet_issue (version_id, action, actor, client_request_id)
			VALUES (:v, :action, :who, :req)
			ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
			map[string]any{
				"v": versionID, "action": req.Action, "who": req.Actor,
				"req": nullStr(req.ClientRequestId),
			})
		if err != nil {
			return fmt.Errorf("failed to record design sheet issue: %w", err)
		}
		rows, err := storeutil.QueryListNamed[entity.DesignSheetIssue](ctx, db, `
			SELECT i.*, v.version_number
			FROM design_sheet_issue i
			JOIN design_sheet_version v ON v.id = i.version_id
			WHERE i.id = :id`,
			map[string]any{"id": issueID})
		if err != nil {
			return fmt.Errorf("failed to read the recorded design sheet issue: %w", err)
		}
		if len(rows) == 0 {
			return fmt.Errorf("failed to read the recorded design sheet issue")
		}
		out = rows[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
