package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestDeleteAccountSpecialtyRefusalIsActionable — весь смысл появления удаления.
//
// Словарь пополняет любой аутентифицированный, поэтому опечатка в нём неизбежна, а видна она на
// каждом экране с пикером людей. Отказ «нельзя» превратил бы выход обратно в тупик: важно не то,
// ЧТО отказали, а то, что в отказе стоит ЧИСЛО аккаунтов, которые надо переназначить.
func TestDeleteAccountSpecialtyRefusalIsActionable(t *testing.T) {
	req := &pb_admin.DeleteAccountSpecialtyRequest{Name: "конструктр"}

	t.Run("позицию держат аккаунты: FailedPrecondition и их число в тексте", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().DeleteSpecialty(mock.Anything, "конструктр").
			Return(entity.NewErrAdminSpecialtyInUse(3))
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo}

		_, err := s.DeleteAccountSpecialty(ctxAs("kirill", false, accountsWrite), req)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		// Именно число, а не «позицию кто-то держит»: с ним человек знает объём работы,
		// без него отказ ничем не отличается от «сломалось».
		require.Contains(t, status.Convert(err).Message(), "3")
	})

	t.Run("такой позиции нет: NotFound, а не «готово»", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().DeleteSpecialty(mock.Anything, "конструктр").Return(sql.ErrNoRows)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo}

		_, err := s.DeleteAccountSpecialty(ctxAs("kirill", false, accountsWrite), req)
		require.Equal(t, codes.NotFound, status.Code(err))
	})

	t.Run("пустое имя не доходит до стора", func(t *testing.T) {
		// Ожиданий на repo нет вовсе: mockery роняет тест на любом неожидаемом вызове, и это и
		// есть доказательство, что отказ случился до похода в базу.
		s := &Server{repo: mocks.NewMockRepository(t)}

		_, err := s.DeleteAccountSpecialty(ctxAs("kirill", false, accountsWrite),
			&pb_admin.DeleteAccountSpecialtyRequest{Name: "   "})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}

// accountsWrite — право, которым закрыт DeleteAccountSpecialty. В хендлере оно не проверяется
// (это делает интерсептор по карте rbac), но контекст здесь честный: тест не должен выглядеть
// доказательством того, чего не проверяет.
var accountsWrite = map[string]entity.AccessLevel{rbac.SectionAccounts: entity.AccessWrite}

// TestGetCurrentAccountReadsSpecialtiesFromTheStore — поле, которое раньше ВСЕГДА было пустым.
//
// Ответ собирался из claims токена, а специальностей в токене нет вовсе. Клиент обошёл это чтением
// себя из ListAdmins, но следующий экран, поверивший пустоте, сохранил бы её обратно:
// SetAccountSpecialties — полная замена набора.
func TestGetCurrentAccountReadsSpecialtiesFromTheStore(t *testing.T) {
	t.Run("специальности приезжают из базы, права остаются из токена", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		stored := &entity.AdminAccount{
			Specialties: []string{"конструктор", "технолог"},
			// В базе у аккаунта прав БОЛЬШЕ, чем в токене. Ответ обязан показать токен: он и
			// только он применяется интерсептором, а нарисованная секция, в которую не пустят,
			// хуже устаревшей.
			Permissions: []entity.AdminPermission{{Section: rbac.SectionAccounts, Access: entity.AccessWrite}},
		}
		stored.IsSuper = true
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "kirill").Return(stored, nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo}

		resp, err := s.GetCurrentAccount(ctxAs("kirill", false, nil), &pb_admin.GetCurrentAccountRequest{})
		require.NoError(t, err)
		require.Equal(t, []string{"конструктор", "технолог"}, resp.Account.Specialties)
		require.False(t, resp.Account.IsSuper)
		require.Empty(t, resp.Account.Permissions)
	})

	t.Run("чтение упало: панель всё равно отвечает, но без подписи", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "kirill").
			Return(nil, errors.New("db is down"))
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo}

		resp, err := s.GetCurrentAccount(ctxAs("kirill", false, accountsWrite), &pb_admin.GetCurrentAccountRequest{})
		// Этот RPC решает, какие секции панели рисовать: уронить его из-за подписи значило бы
		// погасить панель целиком.
		require.NoError(t, err)
		require.Empty(t, resp.Account.Specialties)
		require.Len(t, resp.Account.Permissions, 1)
	})

	t.Run("контекст без имени в базу не ходит", func(t *testing.T) {
		s := &Server{repo: mocks.NewMockRepository(t)}

		resp, err := s.GetCurrentAccount(context.Background(), &pb_admin.GetCurrentAccountRequest{})
		require.NoError(t, err)
		require.Empty(t, resp.Account.Specialties)
	})
}
