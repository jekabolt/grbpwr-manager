package storeutil

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

// Специальности читают ДВА разных стора: `admin` (список аккаунтов, пикер людей) и `fileslibrary`
// (владельцы файла). Они лежат в соседних пакетах, ни один из которых не вправе импортировать
// другой, — и это ровно тот случай, ради которого существует storeutil. Две копии этого запроса
// разошлись бы молча: одна начала бы отдавать имена в другом порядке или без дедупликации, и
// подпись «kirill · конструктор» стала бы зависеть от того, с какого экрана на неё смотрят.

// LoadAdminSpecialties resolves the specialties of MANY accounts in ONE query,
// keyed by admin id. Never N+1: both callers resolve a whole page of people at
// once. Names come back ordered so a byline is stable between renders.
func LoadAdminSpecialties(ctx context.Context, db dependency.DB, adminIDs []int) (map[int][]string, error) {
	out := make(map[int][]string, len(adminIDs))
	if len(adminIDs) == 0 {
		return out, nil
	}
	type row struct {
		AdminID int    `db:"admin_id"`
		Name    string `db:"name"`
	}
	rows, err := QueryListNamed[row](ctx, db, `
		SELECT l.admin_id, s.name
		FROM admin_specialty_link l
		JOIN admin_specialty s ON s.id = l.specialty_id
		WHERE l.admin_id IN (:ids)
		ORDER BY s.name`, map[string]any{"ids": adminIDs})
	if err != nil {
		return nil, fmt.Errorf("failed to load admin specialties: %w", err)
	}
	for _, r := range rows {
		out[r.AdminID] = append(out[r.AdminID], r.Name)
	}
	return out, nil
}

// UpsertAdminSpecialty returns the id of the specialty with this name, creating
// it when it is new. Same shape (and same race-freedom) as the files library's
// upsertTopic: ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id) means two people
// typing the same new specialty in the same second both get the same id instead
// of one of them dying on 1062. Uniqueness is case-insensitive by collation, so
// «Конструктор» resolves onto the existing «конструктор» rather than forking it.
func UpsertAdminSpecialty(ctx context.Context, db dependency.DB, name string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("specialty name is empty")
	}
	return ExecNamedLastId(ctx, db, `
		INSERT INTO admin_specialty (name) VALUES (:name)
		ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
		map[string]any{"name": name})
}
