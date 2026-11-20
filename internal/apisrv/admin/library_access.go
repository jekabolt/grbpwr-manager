package admin

import (
	"context"
	"slices"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
)

// libraryAccess is what the caller may do in each branch of the unified library (02-DESIGN §3.1,
// §4): the media branch needs the content section AND the team's Media feature; the files branch
// the files section AND the Files feature. The edge only lets the library RPCs in (allowlist);
// every real decision is made here, per branch.
type libraryAccess struct {
	MediaRead  bool
	MediaWrite bool
	FilesRead  bool
	FilesWrite bool
}

// libraryAccessOf resolves the caller's library rights from the admin authorization and the
// workspace's features. No authorization in ctx = no access (fail closed, as productsWriteAccess).
func libraryAccessOf(ctx context.Context) libraryAccess {
	az, ok := authsrv.GetAdminAuthz(ctx)
	if !ok {
		return libraryAccess{}
	}
	covers := func(section string, lvl entity.AccessLevel) bool {
		if az.FullAccess() {
			return true
		}
		have, ok := az.Perms[section]
		return ok && have.Covers(lvl)
	}
	media := libraryFeatureOn(ctx, feature.Media)
	files := libraryFeatureOn(ctx, feature.Files)
	return libraryAccess{
		MediaRead:  media && covers(rbac.SectionContent, entity.AccessRead),
		MediaWrite: media && covers(rbac.SectionContent, entity.AccessWrite),
		FilesRead:  files && covers(rbac.SectionFiles, entity.AccessRead),
		FilesWrite: files && covers(rbac.SectionFiles, entity.AccessWrite),
	}
}

// libraryFeatureOn reports whether the team has feature k on. A missing workspace (teams mode off,
// in-process callers) means every feature is on.
func libraryFeatureOn(ctx context.Context, k feature.Key) bool {
	w, ok := authsrv.GetWorkspace(ctx)
	if !ok {
		return true
	}
	return slices.Contains(w.Features, k)
}

// AnyRead: the caller may read at least one branch; otherwise library reads are
// PermissionDenied library_no_section.
func (a libraryAccess) AnyRead() bool { return a.MediaRead || a.FilesRead }

// FolderWrite: folder structure edits need write in either section (with its feature on).
func (a libraryAccess) FolderWrite() bool { return a.MediaWrite || a.FilesWrite }

// Branches is the read gating handed to the store before the filter narrows it.
func (a libraryAccess) Branches() entity.LibraryBranches {
	return entity.LibraryBranches{Media: a.MediaRead, Files: a.FilesRead}
}

// CanWrite reports whether the caller may move an item of this source.
func (a libraryAccess) CanWrite(src entity.LibrarySource) bool {
	switch src {
	case entity.LibrarySourceMedia:
		return a.MediaWrite
	case entity.LibrarySourceFile:
		return a.FilesWrite
	default:
		return false
	}
}
