package admin

import (
	"context"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func libraryCtx(az *authsrv.AdminAuthz, features ...feature.Key) context.Context {
	ctx := context.Background()
	if az != nil {
		ctx = authsrv.PutAdminAuthz(ctx, *az)
	}
	if features != nil {
		ctx = authsrv.PutWorkspace(ctx, authsrv.Workspace{Features: features})
	}
	return ctx
}

func perms(kv ...string) map[string]entity.AccessLevel {
	m := map[string]entity.AccessLevel{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = entity.AccessLevel(kv[i+1])
	}
	return m
}

func TestLibraryAccessOf(t *testing.T) {
	both := []feature.Key{feature.Core, feature.Media, feature.Files}
	tests := []struct {
		name string
		ctx  context.Context
		want libraryAccess
	}{
		{"no authz fails closed", libraryCtx(nil), libraryAccess{}},
		{"super, no workspace = all features", libraryCtx(&authsrv.AdminAuthz{Super: true}), libraryAccess{true, true, true, true}},
		{"legacy token is full access", libraryCtx(&authsrv.AdminAuthz{Legacy: true}), libraryAccess{true, true, true, true}},
		{"content read only", libraryCtx(&authsrv.AdminAuthz{Perms: perms(rbac.SectionContent, string(entity.AccessRead))}, both...),
			libraryAccess{MediaRead: true}},
		{"content write covers read", libraryCtx(&authsrv.AdminAuthz{Perms: perms(rbac.SectionContent, string(entity.AccessWrite))}, both...),
			libraryAccess{MediaRead: true, MediaWrite: true}},
		{"files write only", libraryCtx(&authsrv.AdminAuthz{Perms: perms(rbac.SectionFiles, string(entity.AccessWrite))}, both...),
			libraryAccess{FilesRead: true, FilesWrite: true}},
		{"neither section", libraryCtx(&authsrv.AdminAuthz{Perms: perms(rbac.SectionOrders, string(entity.AccessWrite))}, both...),
			libraryAccess{}},
		{"media feature off drops media, even for super", libraryCtx(&authsrv.AdminAuthz{Super: true}, feature.Core, feature.Files),
			libraryAccess{FilesRead: true, FilesWrite: true}},
		{"files feature off drops files", libraryCtx(&authsrv.AdminAuthz{Perms: perms(rbac.SectionContent, string(entity.AccessWrite), rbac.SectionFiles, string(entity.AccessWrite))}, feature.Core, feature.Media),
			libraryAccess{MediaRead: true, MediaWrite: true}},
		{"workspace with no features = nothing", libraryCtx(&authsrv.AdminAuthz{Super: true}, []feature.Key{}...),
			libraryAccess{}},
	}
	for _, tc := range tests {
		if got := libraryAccessOf(tc.ctx); got != tc.want {
			t.Errorf("%s: libraryAccessOf = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestLibraryAccessHelpers(t *testing.T) {
	a := libraryAccess{MediaRead: true, MediaWrite: true}
	if !a.AnyRead() || !a.FolderWrite() {
		t.Error("content writer must read and edit folders")
	}
	if b := a.Branches(); !b.Media || b.Files {
		t.Errorf("branches = %+v", b)
	}
	if !a.CanWrite(entity.LibrarySourceMedia) || a.CanWrite(entity.LibrarySourceFile) || a.CanWrite(entity.LibrarySourceUnspecified) {
		t.Error("CanWrite must follow the item's own source")
	}
	if (libraryAccess{}).AnyRead() || (libraryAccess{FilesRead: true}).FolderWrite() {
		t.Error("no read / read-only must not pass")
	}
}

// P0 ships the contract only: the new RPCs answer Unimplemented until B1/B2 land, so beta behaves
// exactly as before.
func TestLibraryRPCsAreUnimplementedInP0(t *testing.T) {
	s := &Server{}
	ctx := libraryCtx(&authsrv.AdminAuthz{Super: true})
	calls := map[string]func() error{
		"ListLibraryItems": func() error { _, err := s.ListLibraryItems(ctx, &pb_admin.ListLibraryItemsRequest{}); return err },
		"GetLibraryFacets": func() error { _, err := s.GetLibraryFacets(ctx, &pb_admin.GetLibraryFacetsRequest{}); return err },
		"ListLibraryFolders": func() error {
			_, err := s.ListLibraryFolders(ctx, &pb_admin.ListLibraryFoldersRequest{})
			return err
		},
		"CreateLibraryFolder": func() error {
			_, err := s.CreateLibraryFolder(ctx, &pb_admin.CreateLibraryFolderRequest{})
			return err
		},
		"RenameLibraryFolder": func() error {
			_, err := s.RenameLibraryFolder(ctx, &pb_admin.RenameLibraryFolderRequest{})
			return err
		},
		"MoveLibraryFolder": func() error {
			_, err := s.MoveLibraryFolder(ctx, &pb_admin.MoveLibraryFolderRequest{})
			return err
		},
		"DeleteLibraryFolder": func() error {
			_, err := s.DeleteLibraryFolder(ctx, &pb_admin.DeleteLibraryFolderRequest{})
			return err
		},
		"MoveLibraryItems": func() error { _, err := s.MoveLibraryItems(ctx, &pb_admin.MoveLibraryItemsRequest{}); return err },
		"CopyLibraryFileToMedia": func() error {
			_, err := s.CopyLibraryFileToMedia(ctx, &pb_admin.CopyLibraryFileToMediaRequest{})
			return err
		},
	}
	for name, call := range calls {
		if code := status.Code(call()); code != codes.Unimplemented {
			t.Errorf("%s: code %v, want Unimplemented", name, code)
		}
	}
}
