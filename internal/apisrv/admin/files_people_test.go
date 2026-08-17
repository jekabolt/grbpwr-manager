package admin

import (
	"context"
	"database/sql"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ctxAs builds a request context the way the RBAC interceptor does: a username
// plus the authorization resolved from the token.
func ctxAs(username string, super bool, perms map[string]entity.AccessLevel) context.Context {
	ctx := authsrv.PutAdminUsername(context.Background(), username)
	return authsrv.PutAdminAuthz(ctx, authsrv.AdminAuthz{Super: super, Perms: perms})
}

var filesWrite = map[string]entity.AccessLevel{rbac.SectionFiles: entity.AccessWrite}

// storedFile is the file every case below argues about: uploaded by pasha, kept
// by kirill.
func storedFile() *entity.LibraryFile {
	f := &entity.LibraryFile{Id: 7}
	f.FileName = "mockup.pdf"
	f.UploadedBy = "pasha"
	f.UploadedById = sql.NullInt64{Int64: 1, Valid: true}
	f.Owners = []entity.AdminRef{{Id: 2, Username: "kirill"}}
	return f
}

// TestSetLibraryFileOwnersCircle is the point of the whole handler: files:write
// is NOT enough to change who owns a file. Without the circle, anybody who may
// upload could appoint themselves owner of anybody's file — and, once the access
// levels land, widen their own access by doing it.
func TestSetLibraryFileOwnersCircle(t *testing.T) {
	req := &pb_admin.SetLibraryFileOwnersRequest{FileId: 7, AdminIds: []int32{9}}

	t.Run("an outsider with files:write is refused and nothing is written", func(t *testing.T) {
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetFileById(mock.Anything, 7).Return(storedFile(), nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.SetLibraryFileOwners(ctxAs("mallory", false, filesWrite), req)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		// SetFileOwners has no expectation: mockery fails the test if it is called,
		// which is the assertion that the refusal happened BEFORE the write.
		files.AssertNotCalled(t, "SetFileOwners", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	// Each member of the circle, by the route that admits them.
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		who   string
		super bool
	}{
		{name: "the uploader", who: "pasha"},
		{name: "a current owner", who: "kirill"},
		{name: "a super-admin", who: "someone-else", super: true},
	} {
		t.Run(tc.name+" may change owners", func(t *testing.T) {
			perms := filesWrite
			if tc.super {
				perms = nil
			}
			files := mocks.NewMockFiles(t)
			files.EXPECT().GetFileById(mock.Anything, 7).Return(storedFile(), nil)
			files.EXPECT().SetFileOwners(mock.Anything, 7, []int{9}, tc.who).Return(nil)
			repo := mocks.NewMockRepository(t)
			repo.EXPECT().Files().Return(files)
			s := &Server{repo: repo}

			_, err := s.SetLibraryFileOwners(ctxAs(tc.who, tc.super, perms), req)
			require.NoError(t, err)
		})
	}

	// РЕГРЕССИЯ НА ПОВТОРНОЕ ЗАНЯТИЕ ИМЕНИ. Строка uploaded_by переживает удаление
	// аккаунта, поэтому одного совпадения по имени мало: новый человек, заведённый
	// под освободившимся именем, иначе унаследовал бы круг у всех файлов прежнего.
	t.Run("a recreated username does not inherit the uploader's authority", func(t *testing.T) {
		orphaned := storedFile()
		orphaned.UploadedById = sql.NullInt64{} // аккаунт удалён, ссылка занулена SET NULL
		files := mocks.NewMockFiles(t)
		files.EXPECT().GetFileById(mock.Anything, 7).Return(orphaned, nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s := &Server{repo: repo}

		_, err := s.SetLibraryFileOwners(ctxAs("pasha", false, filesWrite), req)
		require.Equal(t, codes.PermissionDenied, status.Code(err))

		// Владелец и супер этим не задеты — круг сузился ровно на одну ветку.
		files2 := mocks.NewMockFiles(t)
		files2.EXPECT().GetFileById(mock.Anything, 7).Return(orphaned, nil)
		files2.EXPECT().SetFileOwners(mock.Anything, 7, []int{9}, "kirill").Return(nil)
		repo2 := mocks.NewMockRepository(t)
		repo2.EXPECT().Files().Return(files2)
		s2 := &Server{repo: repo2}
		_, err = s2.SetLibraryFileOwners(ctxAs("kirill", false, filesWrite), req)
		require.NoError(t, err)
	})

	t.Run("ids are deduped and a non-positive one is refused", func(t *testing.T) {
		s := &Server{}
		_, err := s.SetLibraryFileOwners(ctxAs("pasha", true, nil),
			&pb_admin.SetLibraryFileOwnersRequest{FileId: 7, AdminIds: []int32{0}})
		require.Equal(t, codes.InvalidArgument, status.Code(err))

		files := mocks.NewMockFiles(t)
		files.EXPECT().GetFileById(mock.Anything, 7).Return(storedFile(), nil)
		files.EXPECT().SetFileOwners(mock.Anything, 7, []int{9}, "pasha").Return(nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Files().Return(files)
		s2 := &Server{repo: repo}
		_, err = s2.SetLibraryFileOwners(ctxAs("pasha", false, filesWrite),
			&pb_admin.SetLibraryFileOwnersRequest{FileId: 7, AdminIds: []int32{9, 9}})
		require.NoError(t, err)
	})
}

// TestListAdminsProjectsDownToAPicker pins WHAT the allowlisted picker may return.
//
// ListAdmins переехал из rd(tech_cards) в allowlist, то есть его теперь может
// позвать ЛЮБОЙ аутентифицированный аккаунт. Довод переезда опирается ровно на
// содержимое ответа: id, имя, специальности, суперность. Права и флаг «отключён»
// ехать не должны — они accounts:read и живут на ListAccounts. Без этого теста
// довод держится на памяти автора, а поле в ответ дописывается одной строкой.
func TestListAdminsProjectsDownToAPicker(t *testing.T) {
	admin := mocks.NewMockAdmin(t)
	// ListAccounts НЕ ожидается: у пикера свой узкий запрос, и отсутствие ожидания
	// здесь — это и есть проверка, что хеши паролей и права он больше не поднимает.
	admin.EXPECT().ListAdminRefs(mock.Anything).Return([]entity.AdminRef{
		{Id: 1, Username: "pasha", IsSuper: true, Specialties: []string{"фотограф"}},
	}, nil)
	admin.EXPECT().ListSpecialties(mock.Anything).Return([]string{"фотограф", "технолог"}, nil)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Admin().Return(admin)
	s := &Server{repo: repo}

	resp, err := s.ListAdmins(context.Background(), &pb_admin.ListAdminsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Admins, 1)
	got := resp.Admins[0]
	require.Equal(t, int32(1), got.Id)
	require.Equal(t, "pasha", got.Username)
	require.True(t, got.IsSuper)
	require.Equal(t, []string{"фотограф"}, got.Specialties)
	// Весь словарь — для «+ добавить свою»: незанятая никем затравка иначе невидима.
	require.Equal(t, []string{"фотограф", "технолог"}, resp.Specialties)

	// Ни одного поля сверх четырёх: AdminRef физически не несёт ни прав, ни
	// disabled, ни хеша пароля, и этот require ловит момент, когда понесёт.
	require.Equal(t, 4, got.ProtoReflect().Descriptor().Fields().Len(),
		"AdminRef grew a field: an allowlisted response now says more about people than a picker needs")
}

// TestListAdminsSurvivesAMissingVocabulary: словарь — удобство, а список людей —
// суть. Отказ первого не имеет права уносить второй.
func TestListAdminsSurvivesAMissingVocabulary(t *testing.T) {
	admin := mocks.NewMockAdmin(t)
	admin.EXPECT().ListAdminRefs(mock.Anything).Return([]entity.AdminRef{
		{Id: 1, Username: "pasha"},
	}, nil)
	admin.EXPECT().ListSpecialties(mock.Anything).Return(nil, context.DeadlineExceeded)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Admin().Return(admin)
	s := &Server{repo: repo}

	resp, err := s.ListAdmins(context.Background(), &pb_admin.ListAdminsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Admins, 1)
	require.Empty(t, resp.Specialties)
}

// TestSetAccountSpecialtiesSelfEdit pins решение Р1: свою специальность человек
// правит сам, чужую — только с accounts:write. Именно поэтому метод стоит в
// allowlist, а проверка живёт в хендлере.
func TestSetAccountSpecialtiesSelfEdit(t *testing.T) {
	t.Run("own specialties need no grant at all", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAdminByUsername(mock.Anything, "kirill").Return(&entity.Admin{Id: 2, Username: "kirill"}, nil)
		admin.EXPECT().SetSpecialties(mock.Anything, 2, []int{3}, []string{"конструктор"}).Return(nil)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "kirill").Return(&entity.AdminAccount{
			Admin:       entity.Admin{Id: 2, Username: "kirill"},
			Specialties: []string{"конструктор"},
		}, nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo}

		resp, err := s.SetAccountSpecialties(ctxAs("kirill", false, nil), &pb_admin.SetAccountSpecialtiesRequest{
			Username:       "KIRILL", // регистр имени не должен превращать своё в чужое
			SpecialtyIds:   []int32{3},
			NewSpecialties: []string{"конструктор"},
		})
		require.NoError(t, err)
		require.Equal(t, []string{"конструктор"}, resp.Specialties)
	})

	t.Run("somebody else's specialties require accounts:write", func(t *testing.T) {
		s := &Server{}
		_, err := s.SetAccountSpecialties(ctxAs("kirill", false, filesWrite),
			&pb_admin.SetAccountSpecialtiesRequest{Username: "pasha"})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("an anonymous context is not treated as anybody's own account", func(t *testing.T) {
		s := &Server{}
		_, err := s.SetAccountSpecialties(context.Background(),
			&pb_admin.SetAccountSpecialtiesRequest{Username: "pasha"})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("accounts:write edits somebody else", func(t *testing.T) {
		admin := mocks.NewMockAdmin(t)
		admin.EXPECT().GetAdminByUsername(mock.Anything, "pasha").Return(&entity.Admin{Id: 1, Username: "pasha"}, nil)
		// Пустой набор — это «снять все специальности», законная правка.
		admin.EXPECT().SetSpecialties(mock.Anything, 1, []int{}, []string{}).Return(nil)
		admin.EXPECT().GetAccountWithPermissions(mock.Anything, "pasha").Return(&entity.AdminAccount{
			Admin: entity.Admin{Id: 1, Username: "pasha"},
		}, nil)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().Admin().Return(admin)
		s := &Server{repo: repo}

		_, err := s.SetAccountSpecialties(
			ctxAs("boss", false, map[string]entity.AccessLevel{rbac.SectionAccounts: entity.AccessWrite}),
			&pb_admin.SetAccountSpecialtiesRequest{Username: "pasha"})
		require.NoError(t, err)
	})
}
