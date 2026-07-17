package techcard

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// roleAssignmentSelect is the shared projection: the assignment plus the resolved admin username.
const roleAssignmentSelect = `
	SELECT r.id, r.tech_card_id, r.role, r.admin_id, a.username AS admin_username, r.assigned_by, r.assigned_at
	FROM tech_card_role_assignment r
	JOIN admins a ON a.id = r.admin_id`

// AssignTechCardRole inserts a role assignment (Q5) and returns it with the resolved username. A
// duplicate (tech_card_id, role, admin_id) surfaces as a unique violation and a missing card/admin as
// a foreign-key violation — the handler maps both to field-tagged InvalidArgument.
func (s *Store) AssignTechCardRole(ctx context.Context, a entity.TechCardRoleAssignment) (entity.TechCardRoleAssignment, error) {
	id, err := storeutil.ExecNamedLastId(ctx, s.DB, `
		INSERT INTO tech_card_role_assignment (tech_card_id, role, admin_id, assigned_by)
		VALUES (:tech_card_id, :role, :admin_id, :assigned_by)`,
		map[string]any{
			"tech_card_id": a.TechCardId,
			"role":         string(a.Role),
			"admin_id":     a.AdminId,
			"assigned_by":  a.AssignedBy,
		})
	if err != nil {
		return entity.TechCardRoleAssignment{}, fmt.Errorf("assign tech card role: %w", err)
	}
	row, err := storeutil.QueryNamedOne[entity.TechCardRoleAssignment](ctx, s.DB,
		roleAssignmentSelect+` WHERE r.id = :id`, map[string]any{"id": id})
	if err != nil {
		return entity.TechCardRoleAssignment{}, fmt.Errorf("read back role assignment: %w", err)
	}
	return row, nil
}

// RemoveTechCardRoleAssignment deletes one assignment by id, returning sql.ErrNoRows when none
// existed (the handler maps that to NotFound rather than a silent success).
func (s *Store) RemoveTechCardRoleAssignment(ctx context.Context, id int) error {
	rows, err := storeutil.ExecNamedRows(ctx, s.DB,
		`DELETE FROM tech_card_role_assignment WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("remove tech card role assignment: %w", err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListTechCardRoleAssignments returns a card's role assignments with resolved usernames, ordered by
// role then username.
func (s *Store) ListTechCardRoleAssignments(ctx context.Context, techCardID int) ([]entity.TechCardRoleAssignment, error) {
	rows, err := storeutil.QueryListNamed[entity.TechCardRoleAssignment](ctx, s.DB,
		roleAssignmentSelect+` WHERE r.tech_card_id = :tech_card_id ORDER BY r.role, a.username`,
		map[string]any{"tech_card_id": techCardID})
	if err != nil {
		return nil, fmt.Errorf("list tech card role assignments: %w", err)
	}
	return rows, nil
}
