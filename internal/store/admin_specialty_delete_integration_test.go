package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestAdminSpecialtyDeletion — выход из словаря специальностей (0315, долг ревью Ф3).
//
// Тест обязан быть интеграционным: каждое утверждение здесь — утверждение о SQL и о схеме.
// Регистро-независимое сопоставление имени делает КОЛЛАЦИЯ, а не Go; отказ на используемой позиции
// держит внешний ключ RESTRICT; потолок словаря считает строки таблицы. Ни одно из трёх не
// наблюдается в Go-коде.
func TestAdminSpecialtyDeletion(t *testing.T) {
	filesGuard(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	t.Run("свободную позицию удаляет, имя сопоставляя без учёта регистра", func(t *testing.T) {
		kirillID, _ := insertAdminFixture(ctx, t, "test-spec-kirill")
		require.NoError(t, s.Admin().SetSpecialties(ctx, kirillID, nil, []string{"тестспец"}))
		// Снимаем связь: позиция остаётся в словаре, но её больше никто не несёт.
		require.NoError(t, s.Admin().SetSpecialties(ctx, kirillID, nil, nil))

		// Удаляем ДРУГИМ регистром, чем заводили: снаружи имя приходит так, как его набрал
		// человек, и попасть в удаление обязано то же, что попало бы в пикер.
		require.NoError(t, s.Admin().DeleteSpecialty(ctx, "ТЕСТСПЕЦ"))

		names, err := s.Admin().ListSpecialties(ctx)
		require.NoError(t, err)
		require.NotContains(t, names, "тестспец")
	})

	t.Run("занятую позицию не удаляет и называет число аккаунтов", func(t *testing.T) {
		oneID, _ := insertAdminFixture(ctx, t, "test-spec-one")
		twoID, _ := insertAdminFixture(ctx, t, "test-spec-two")
		require.NoError(t, s.Admin().SetSpecialties(ctx, oneID, nil, []string{"тестзанятая"}))
		require.NoError(t, s.Admin().SetSpecialties(ctx, twoID, nil, []string{"Тестзанятая"}))
		t.Cleanup(func() {
			_, _ = testDB.Exec(`DELETE l FROM admin_specialty_link l
				JOIN admin_specialty sp ON sp.id = l.specialty_id WHERE sp.name = 'тестзанятая'`)
			_, _ = testDB.Exec(`DELETE FROM admin_specialty WHERE name = 'тестзанятая'`)
		})

		err := s.Admin().DeleteSpecialty(ctx, "тестзанятая")
		require.ErrorIs(t, err, entity.ErrAdminSpecialtyInUse)
		// Ровно ДВА, а не две строки связи и не «кто-то»: «Тестзанятая» вторым регистром — та же
		// позиция, и отказ обязан назвать число людей, которых придётся переназначить.
		require.Contains(t, err.Error(), "2")

		names, err := s.Admin().ListSpecialties(ctx)
		require.NoError(t, err)
		require.Contains(t, names, "тестзанятая")
	})

	t.Run("несуществующая позиция — ErrNoRows, а не молчаливое «удалил»", func(t *testing.T) {
		require.ErrorIs(t, s.Admin().DeleteSpecialty(ctx, "такой-специальности-нет"), sql.ErrNoRows)
	})

	// М-2 из ревью Ф3: потолок считал ПРИСЛАННЫЕ имена, а не те, что реально появятся. У самой
	// границы законная правка — «оставь мне ту же специальность, что и была» — получала «словарь
	// переполнен», хотя не добавляла ни одного слова.
	t.Run("потолок считает только новые имена", func(t *testing.T) {
		adminID, _ := insertAdminFixture(ctx, t, "test-spec-cap")
		var total int
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_specialty`).Scan(&total))
		require.LessOrEqual(t, total, entity.MaxAdminSpecialtyVocabulary)
		// Добиваем словарь ровно до потолка.
		for i := total; i < entity.MaxAdminSpecialtyVocabulary; i++ {
			_, err := testDB.ExecContext(ctx,
				`INSERT INTO admin_specialty (name) VALUES (CONCAT('тестпотолок-', ?))`, i)
			require.NoError(t, err)
		}
		t.Cleanup(func() {
			_, _ = testDB.Exec(`DELETE l FROM admin_specialty_link l
				JOIN admin_specialty sp ON sp.id = l.specialty_id WHERE sp.name LIKE 'тестпотолок-%'`)
			_, _ = testDB.Exec(`DELETE FROM admin_specialty WHERE name LIKE 'тестпотолок-%'`)
		})

		// Имя, которое в словаре УЖЕ есть (пусть и набранное иначе), словарь не растит.
		require.NoError(t, s.Admin().SetSpecialties(ctx, adminID, nil, []string{"Конструктор"}))
		account, err := s.Admin().GetAccountWithPermissions(ctx, adminUsernameByID(ctx, t, adminID))
		require.NoError(t, err)
		require.Equal(t, []string{"конструктор"}, account.Specialties)

		// А настоящее новое имя за потолком — отказ, и он остался ровно тем же.
		err = s.Admin().SetSpecialties(ctx, adminID, nil, []string{"тестпереполнение"})
		require.ErrorIs(t, err, entity.ErrAdminSpecialtyVocabularyFull)
		names, err := s.Admin().ListSpecialties(ctx)
		require.NoError(t, err)
		require.NotContains(t, names, "тестпереполнение")
		// Отказ не тронул то, что человек нёс до него: транзакция откатилась целиком.
		account, err = s.Admin().GetAccountWithPermissions(ctx, adminUsernameByID(ctx, t, adminID))
		require.NoError(t, err)
		require.Equal(t, []string{"конструктор"}, account.Specialties)
	})
}

func adminUsernameByID(ctx context.Context, t *testing.T, id int) string {
	t.Helper()
	var username string
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT username FROM admins WHERE id = ?`, id).Scan(&username))
	return strings.ToLower(username)
}
