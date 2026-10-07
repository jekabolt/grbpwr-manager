package admin

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

func TestFoldReferenceRowsMovesTheOldInputOntoTheBoard(t *testing.T) {
	board := func(id int, role entity.TechCardMediaRole) entity.TechCardMediaItem {
		return entity.TechCardMediaItem{MediaId: id, Category: entity.TechCardMediaCategoryMoodboard,
			Kind: entity.TechCardMediaMoodboard, Role: role}
	}
	ref := func(id int) entity.TechCardMediaItem {
		return entity.TechCardMediaItem{MediaId: id, Category: entity.TechCardMediaCategoryMoodboard,
			Kind: entity.TechCardMediaReference}
	}
	tech := entity.TechCardMediaItem{MediaId: 90, Category: entity.TechCardMediaCategoryTechnical,
		Kind: entity.TechCardMediaReference}
	labels := []entity.DesignReference{
		{MediaId: 1, Role: entity.DesignViewFront, LabelSource: entity.DesignLabelSourceHuman},
		{MediaId: 2, Role: entity.DesignViewDetail},
		{MediaId: 3, Role: entity.DesignViewBack, LabelSource: entity.DesignLabelSourceModelStrong},
		{MediaId: 5, Role: entity.DesignViewBack},
		{MediaId: 6, Role: entity.DesignViewFront},
	}
	in := []entity.TechCardMediaItem{
		board(5, entity.TechCardMediaRoleNone), // its reference twin speaks for it → target
		board(6, entity.TechCardMediaRoleNone), // no twin: the person's «none» stays
		ref(1), ref(2), ref(3), ref(4), ref(5), ref(1), tech,
	}
	got, err := designFoldReferenceRows(in, func() ([]entity.DesignReference, error) { return labels, nil })
	require.NoError(t, err)
	type row struct {
		id   int
		kind entity.TechCardMediaKind
		role entity.TechCardMediaRole
	}
	var rows []row
	for _, m := range got {
		rows = append(rows, row{m.MediaId, m.Kind, m.Role})
	}
	require.Equal(t, []row{
		{5, entity.TechCardMediaMoodboard, entity.TechCardMediaRoleTarget},
		{6, entity.TechCardMediaMoodboard, entity.TechCardMediaRoleNone},
		{1, entity.TechCardMediaMoodboard, entity.TechCardMediaRoleTarget},
		{2, entity.TechCardMediaMoodboard, entity.TechCardMediaRoleDetail},
		{3, entity.TechCardMediaMoodboard, entity.TechCardMediaRoleNone}, // a model's label decides nothing
		{4, entity.TechCardMediaMoodboard, entity.TechCardMediaRoleNone},
		{90, entity.TechCardMediaReference, entity.TechCardMediaRoleNone}, // not the board: untouched
	}, rows)

	plain := []entity.TechCardMediaItem{board(7, entity.TechCardMediaRoleMood)}
	same, err := designFoldReferenceRows(plain, func() ([]entity.DesignReference, error) {
		t.Fatal("labels are read only when a reference row is folded")
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, plain, same, "no reference row: the list is returned as is")

	_, err = designFoldReferenceRows(in, func() ([]entity.DesignReference, error) { return nil, errors.New("db down") })
	require.Error(t, err, "no labels, no fold: the save is refused and retried, not committed without purposes")

	// A dropped twin's caption moves to a board row that has none.
	capt := ref(8)
	capt.Caption = sql.NullString{String: "sleeve seam only", Valid: true}
	kept, err := designFoldReferenceRows([]entity.TechCardMediaItem{board(8, entity.TechCardMediaRoleMood), capt},
		func() ([]entity.DesignReference, error) { return nil, nil })
	require.NoError(t, err)
	require.Len(t, kept, 1)
	require.Equal(t, "sleeve seam only", kept[0].Caption.String)
	require.Equal(t, entity.TechCardMediaRoleMood, kept[0].Role, "a purpose the person gave stays")
}
