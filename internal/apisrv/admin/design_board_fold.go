package admin

import (
	"context"
	"database/sql"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// designFoldReferenceRows — 101 Ф4, DEPLOY 1 OF THE REFERENCE RETIREMENT (§3.3). The flat's separate
// input («INPUT — REFERENCES», tech_card_media.kind = reference) is gone: the moodboard is the only
// source of a flat's pictures. Migration 0404 moved every stored reference row onto the board; this
// fold does the same to a SAVE that still carries one — an admin tab opened before the client
// update round-trips its old form, and a full-replace save would otherwise put the rows back.
//
// What it does, in the list's own order:
//   - a reference row whose picture is already on the board is dropped; if that board row has no
//     purpose, it takes the one the picture's person label implies (a view → target, detail →
//     detail);
//   - any other reference row becomes a board row (kind moodboard) with the purpose it carried or,
//     when it carried none, the one its person label implies; a second reference row of the same
//     picture is dropped.
//
// A board row the person left without a purpose is NOT given one here: «none» on the tile is an
// answer, and only the folded reference twin speaks for the picture. A dropped twin's caption moves to
// a board row that has none (the old client never wrote one, but nothing written is dropped silently).
// Model labels never decide a purpose (they propose it on the client). Non-moodboard rows pass
// untouched. The word itself leaves the dictionary in deploy 2 (README-pending-drops.md).
//
// labels is asked only when there is a reference row to fold; its error fails the fold (and the save,
// which the client retries) — a fold without the labels would commit a picture with no purpose next
// to a person's label the model may not touch, i.e. a photo that silently stops riding.
func designFoldReferenceRows(media []entity.TechCardMediaItem, labels func() ([]entity.DesignReference, error)) ([]entity.TechCardMediaItem, error) {
	isRef := func(m entity.TechCardMediaItem) bool {
		return m.Category == entity.TechCardMediaCategoryMoodboard && m.Kind == entity.TechCardMediaReference
	}
	hasRef := false
	onBoard := map[int]bool{}
	for _, m := range media {
		if isRef(m) {
			hasRef = true
		} else if m.Category == entity.TechCardMediaCategoryMoodboard {
			onBoard[m.MediaId] = true
		}
	}
	if !hasRef {
		return media, nil
	}
	var refs []entity.DesignReference
	if labels != nil {
		var err error
		if refs, err = labels(); err != nil {
			return nil, err
		}
	}
	label := map[int]string{}
	for _, r := range refs {
		if r.Role != "" && !entity.IsDesignLabelByModel(r.LabelSource) {
			label[r.MediaId] = r.Role
		}
	}
	purposeOf := func(mediaID int) entity.TechCardMediaRole {
		switch role := label[mediaID]; {
		case role == "":
			return entity.TechCardMediaRoleNone
		case role == entity.DesignViewDetail:
			return entity.TechCardMediaRoleDetail
		default:
			return entity.TechCardMediaRoleTarget
		}
	}
	type twin struct {
		caption sql.NullString
		seen    bool
	}
	folded := map[int]*twin{} // a board picture whose reference twin was dropped
	converted := map[int]bool{}
	out := make([]entity.TechCardMediaItem, 0, len(media))
	for _, m := range media {
		if !isRef(m) {
			out = append(out, m)
			continue
		}
		if onBoard[m.MediaId] {
			tw := folded[m.MediaId]
			if tw == nil {
				tw = &twin{}
				folded[m.MediaId] = tw
			}
			if !tw.seen && m.Caption.Valid && m.Caption.String != "" {
				tw.caption, tw.seen = m.Caption, true
			}
			continue
		}
		if converted[m.MediaId] {
			continue
		}
		converted[m.MediaId] = true
		m.Kind = entity.TechCardMediaMoodboard
		if m.Role == entity.TechCardMediaRoleNone {
			m.Role = purposeOf(m.MediaId)
		}
		out = append(out, m)
	}
	done := map[int]bool{}
	for i := range out {
		m := &out[i]
		tw := folded[m.MediaId]
		if tw == nil || done[m.MediaId] || m.Category != entity.TechCardMediaCategoryMoodboard {
			continue
		}
		done[m.MediaId] = true
		if m.Role == entity.TechCardMediaRoleNone {
			m.Role = purposeOf(m.MediaId)
		}
		if tw.seen && !(m.Caption.Valid && m.Caption.String != "") {
			m.Caption = tw.caption
		}
	}
	return out, nil
}

// designLabelsForFold — the card's labels for the fold.
func (s *Server) designLabelsForFold(ctx context.Context, cardID int) ([]entity.DesignReference, error) {
	refs, err := s.repo.Design().ListReferences(ctx, cardID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "board fold: can't read the picture labels",
			slog.Int("tech_card_id", cardID), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read the moodboard labels; try again")
	}
	return refs, nil
}
