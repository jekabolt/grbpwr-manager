// Package admin implements admin user management operations.
package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// TxFunc executes f within a serializable transaction with deadlock retry.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Admin.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new admin store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// AddAccount creates a new admin account with an initial permission set. isSuper
// grants full access (permissions are then irrelevant and should be empty).
func (s *Store) AddAccount(ctx context.Context, username, pwHash string, isSuper bool, perms []entity.AdminPermission) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res, err := rep.DB().ExecContext(ctx, `
			INSERT INTO admins (username, password_hash, is_super)
			VALUES (?, ?, ?)`, username, pwHash, isSuper)
		if err != nil {
			return fmt.Errorf("can't add admin account: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("can't read new admin id: %w", err)
		}
		if isSuper {
			return nil
		}
		return insertPermissions(ctx, rep, int(id), perms)
	})
}

// SetAccountPermissions replaces an account's super flag and permission set.
func (s *Store) SetAccountPermissions(ctx context.Context, username string, isSuper bool, perms []entity.AdminPermission) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		id, err := adminIDByUsername(ctx, rep, username)
		if err != nil {
			return err
		}
		if _, err := rep.DB().ExecContext(ctx,
			`UPDATE admins SET is_super = ? WHERE id = ?`, isSuper, id); err != nil {
			return fmt.Errorf("failed to update admin super flag: %w", err)
		}
		if _, err := rep.DB().ExecContext(ctx,
			`DELETE FROM admin_permission WHERE admin_id = ?`, id); err != nil {
			return fmt.Errorf("failed to clear admin permissions: %w", err)
		}
		if isSuper {
			return nil
		}
		return insertPermissions(ctx, rep, id, perms)
	})
}

// SetAccountDisabled toggles whether an account may obtain new tokens at login.
func (s *Store) SetAccountDisabled(ctx context.Context, username string, disabled bool) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res, err := rep.DB().ExecContext(ctx,
			`UPDATE admins SET disabled = ? WHERE username = ?`, disabled, username)
		if err != nil {
			return fmt.Errorf("failed to set admin disabled flag: %w", err)
		}
		ra, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get affected rows: %w", err)
		}
		if ra == 0 {
			return fmt.Errorf("admin not found")
		}
		return nil
	})
}

// insertPermissions bulk-inserts the (valid) permission rows for an admin id.
// Rows with an unrecognized access level are rejected to keep the table clean.
func insertPermissions(ctx context.Context, rep dependency.Repository, adminID int, perms []entity.AdminPermission) error {
	if len(perms) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(perms))
	args := make([]any, 0, len(perms)*3)
	for _, p := range perms {
		if p.Section == "" || !p.Access.Valid() {
			return fmt.Errorf("invalid permission %q:%q", p.Section, p.Access)
		}
		placeholders = append(placeholders, "(?, ?, ?)")
		args = append(args, adminID, p.Section, string(p.Access))
	}
	q := `INSERT INTO admin_permission (admin_id, section, access) VALUES ` + strings.Join(placeholders, ", ")
	if _, err := rep.DB().ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("failed to insert admin permissions: %w", err)
	}
	return nil
}

func adminIDByUsername(ctx context.Context, rep dependency.Repository, username string) (int, error) {
	var id int
	if err := rep.DB().QueryRowxContext(ctx,
		`SELECT id FROM admins WHERE username = ?`, username).Scan(&id); err != nil {
		return 0, fmt.Errorf("failed to get admin id: %w", err)
	}
	return id, nil
}

// DeleteAdmin deletes an admin account (its permissions cascade).
func (s *Store) DeleteAdmin(ctx context.Context, username string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res, err := rep.DB().ExecContext(ctx, `
			DELETE FROM admins WHERE username = ?`, username)
		if err != nil {
			return fmt.Errorf("failed to delete admin user")
		}
		ra, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get affected rows")
		}
		if ra == 0 {
			return fmt.Errorf("admin not found")
		}
		return nil
	})
}

// ChangePassword changes the password of an admin user.
func (s *Store) ChangePassword(ctx context.Context, un, newHash string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res, err := rep.DB().ExecContext(ctx, `
			UPDATE admins
			SET password_hash = ?
			WHERE username = ?`, newHash, un)
		if err != nil {
			return fmt.Errorf("failed change admin user password")
		}
		ra, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get affected rows")
		}
		if ra == 0 {
			return fmt.Errorf("admin not found")
		}
		return nil
	})
}

// PasswordHashByUsername returns password hash of an admin user.
func (s *Store) PasswordHashByUsername(ctx context.Context, un string) (string, error) {
	query := `SELECT password_hash FROM admins WHERE username = :username`
	adm, err := storeutil.QueryNamedOne[entity.Admin](ctx, s.DB, query, map[string]any{"username": un})
	if err != nil {
		return "", fmt.Errorf("failed to get password hash %w", err)
	}
	return adm.PasswordHash, nil
}

// GetAdminByUsername returns an admin user by username (without permissions).
func (s *Store) GetAdminByUsername(ctx context.Context, un string) (*entity.Admin, error) {
	query := `SELECT id, username, password_hash, is_super, disabled, created_at, updated_at
		FROM admins WHERE username = :username`
	admin, err := storeutil.QueryNamedOne[entity.Admin](ctx, s.DB, query, map[string]any{"username": un})
	if err != nil {
		return nil, fmt.Errorf("failed to get admin: %w", err)
	}
	return &admin, nil
}

// GetAccountWithPermissions returns an account with its resolved permission set.
// Used at login to embed authorization into the JWT.
func (s *Store) GetAccountWithPermissions(ctx context.Context, username string) (*entity.AdminAccount, error) {
	admin, err := s.GetAdminByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	perms, err := s.permissionsByAdminID(ctx, admin.Id)
	if err != nil {
		return nil, err
	}
	specialties, err := storeutil.LoadAdminSpecialties(ctx, s.DB, []int{admin.Id})
	if err != nil {
		return nil, err
	}
	return &entity.AdminAccount{
		Admin:       *admin,
		Permissions: perms,
		Specialties: specialties[admin.Id],
	}, nil
}

func (s *Store) permissionsByAdminID(ctx context.Context, adminID int) ([]entity.AdminPermission, error) {
	perms, err := storeutil.QueryListNamed[entity.AdminPermission](ctx, s.DB,
		`SELECT section, access FROM admin_permission WHERE admin_id = :admin_id ORDER BY section`,
		map[string]any{"admin_id": adminID})
	if err != nil {
		return nil, fmt.Errorf("failed to get admin permissions: %w", err)
	}
	return perms, nil
}

// ListAccounts returns every admin account with its permissions, ordered by
// username. Small table (a handful of admins), so two queries + in-memory group.
func (s *Store) ListAccounts(ctx context.Context) ([]entity.AdminAccount, error) {
	admins, err := storeutil.QueryListNamed[entity.Admin](ctx, s.DB,
		`SELECT id, username, password_hash, is_super, disabled, created_at, updated_at
		 FROM admins ORDER BY username`, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list admins: %w", err)
	}
	type permRow struct {
		AdminID int                `db:"admin_id"`
		Section string             `db:"section"`
		Access  entity.AccessLevel `db:"access"`
	}
	rows, err := storeutil.QueryListNamed[permRow](ctx, s.DB,
		`SELECT admin_id, section, access FROM admin_permission ORDER BY section`, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list admin permissions: %w", err)
	}
	byAdmin := make(map[int][]entity.AdminPermission, len(admins))
	for _, r := range rows {
		byAdmin[r.AdminID] = append(byAdmin[r.AdminID], entity.AdminPermission{Section: r.Section, Access: r.Access})
	}
	adminIDs := make([]int, 0, len(admins))
	for _, a := range admins {
		adminIDs = append(adminIDs, a.Id)
	}
	// Третий запрос той же формы, что и второй: специальности всей страницы разом.
	// Их читает не только экран аккаунтов — на этой же выборке стоит ListAdmins,
	// пикер людей всей панели, и он рисуется на каждом экране с назначением.
	specialties, err := storeutil.LoadAdminSpecialties(ctx, s.DB, adminIDs)
	if err != nil {
		return nil, err
	}
	accounts := make([]entity.AdminAccount, 0, len(admins))
	for _, a := range admins {
		accounts = append(accounts, entity.AdminAccount{
			Admin:       a,
			Permissions: byAdmin[a.Id],
			Specialties: specialties[a.Id],
		})
	}
	return accounts, nil
}

// ListAdminRefs is the people picker's own read, deliberately NOT a projection of
// ListAccounts.
//
// ДВЕ ПРИЧИНЫ, ОБЕ ПРО ТО, ЧТО ЭТОТ ЗАПРОС ВИДИТ ЛЮБОЙ АУТЕНТИФИЦИРОВАННЫЙ.
// Первая: ListAccounts тянет `password_hash` и полный набор прав и выбрасывает их
// при проекции — то есть на каждый рендер пикера поднимает в память учётные данные
// всех аккаунтов ради имени и id. Проекция была бы корректной и всё равно
// неправильной: не читать лишнего надёжнее, чем не отдать прочитанное.
// Вторая: отключённые аккаунты в пикере — неверный ответ на оба вопроса, которые
// им задают («кого назначить владельцем» и «у кого просить доступ»). Уволенного
// спрашивать бесполезно. Уже назначенные владельцы этим не задеты: они приезжают
// на самом файле (library_file.owners), а не из пикера.
func (s *Store) ListAdminRefs(ctx context.Context) ([]entity.AdminRef, error) {
	refs, err := storeutil.QueryListNamed[entity.AdminRef](ctx, s.DB,
		`SELECT id, username, is_super FROM admins WHERE disabled = FALSE ORDER BY username`, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list admin refs: %w", err)
	}
	if len(refs) == 0 {
		return nil, nil
	}
	adminIDs := make([]int, 0, len(refs))
	for _, r := range refs {
		adminIDs = append(adminIDs, r.Id)
	}
	specialties, err := storeutil.LoadAdminSpecialties(ctx, s.DB, adminIDs)
	if err != nil {
		return nil, err
	}
	for i := range refs {
		refs[i].Specialties = specialties[refs[i].Id]
	}
	return refs, nil
}

// ListSpecialties returns the WHOLE specialty vocabulary, most used first and
// alphabetically within a usage tier. Ordering by usage rather than by name is
// the same choice ListTopics makes: entries nobody carries sink on their own, and
// a list of thirty becomes the six that describe this team.
func (s *Store) ListSpecialties(ctx context.Context) ([]string, error) {
	names, err := storeutil.QueryScalarListNamed[string](ctx, s.DB, `
		SELECT sp.name
		FROM admin_specialty sp
		LEFT JOIN admin_specialty_link l ON l.specialty_id = sp.id
		GROUP BY sp.id
		ORDER BY COUNT(l.admin_id) DESC, sp.name ASC`, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list admin specialties: %w", err)
	}
	return names, nil
}

// SetSpecialties REPLACES what one account says it does. Names in newSpecialties
// are created on the fly and land in the SHARED vocabulary — the same grammar as
// a file's topics, so the next person picks the word instead of inventing a
// synonym for it.
//
// Транзакция, потому что промежуточное состояние здесь наблюдаемо: старые связи
// уже сняты, новые ещё не вставлены — человек на мгновение оказался бы без
// специальности, и параллельный пикер напечатал бы его без подписи.
func (s *Store) SetSpecialties(ctx context.Context, adminID int, specialtyIDs []int, newSpecialties []string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		ids := make(map[int]struct{}, len(specialtyIDs)+len(newSpecialties))
		for _, id := range specialtyIDs {
			if id > 0 {
				ids[id] = struct{}{}
			}
		}
		if len(newSpecialties) > 0 {
			// ПОТОЛОК СЛОВАРЯ. Завести новую специальность может любой аутентифицированный
			// (решение Р1), удалить — никто: RPC удаления нет, а FK связи стоит RESTRICT.
			// При этом ВЕСЬ словарь едет в каждом ответе пикера, то есть на каждый рендер
			// любого экрана с назначением. Без потолка один цикл навсегда раздувает
			// панельный ответ, и откатить это некому. Потолок щедрый: он ловит цикл, а не
			// человека — команде из шести человек до него не дойти.
			total, err := storeutil.QueryCountNamed(ctx, rep.DB(),
				`SELECT COUNT(*) FROM admin_specialty`, nil)
			if err != nil {
				return fmt.Errorf("failed to count admin specialties: %w", err)
			}
			if total+len(newSpecialties) > entity.MaxAdminSpecialtyVocabulary {
				return entity.ErrAdminSpecialtyVocabularyFull
			}
		}
		for _, name := range newSpecialties {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			id, err := storeutil.UpsertAdminSpecialty(ctx, rep.DB(), name)
			if err != nil {
				return fmt.Errorf("failed to resolve specialty %q: %w", name, err)
			}
			ids[id] = struct{}{}
		}
		if _, err := rep.DB().ExecContext(ctx,
			`DELETE FROM admin_specialty_link WHERE admin_id = ?`, adminID); err != nil {
			return fmt.Errorf("failed to clear admin specialties: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		// Обычный INSERT, а не INSERT IGNORE: IGNORE глушит и нарушение внешнего
		// ключа тоже, поэтому несуществующий id специальности превратился бы в
		// «ничего не проставилось» с ответом «готово». Пусть падает — хендлер
		// переводит это в внятный InvalidArgument.
		rows := make([]map[string]any, 0, len(ids))
		for id := range ids {
			rows = append(rows, map[string]any{"admin_id": adminID, "specialty_id": id})
		}
		if err := storeutil.BulkInsert(ctx, rep.DB(), "admin_specialty_link", rows); err != nil {
			return fmt.Errorf("failed to link admin specialties: %w", err)
		}
		return nil
	})
}

// CountSuperAdmins returns the number of non-disabled super-admin accounts. Used
// to guard against removing or disabling the last super-admin (which would leave
// nobody able to manage accounts).
func (s *Store) CountSuperAdmins(ctx context.Context) (int, error) {
	return storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM admins WHERE is_super = TRUE AND disabled = FALSE`, nil)
}
