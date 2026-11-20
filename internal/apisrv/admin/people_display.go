package admin

import (
	"context"
	"log/slog"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/avatar"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// personDisplay is how one person row shows (P0 §3.1): name, @username and photo, read from the
// control-plane directory by handle. The handle stays the row's key (AdminRef.username and friends).
type personDisplay struct {
	Name           string
	PublicUsername string
	AvatarURL      string
}

func (s *Server) displayOf(p platformstore.Person) personDisplay {
	return personDisplay{
		Name:           platformstore.DisplayName(p.FirstName, p.LastName),
		PublicUsername: p.PublicUsername,
		AvatarURL:      s.avatarURL(p.AvatarSHA),
	}
}

// avatarURL is the public URL of an avatar; "" without one or with teams mode off.
func (s *Server) avatarURL(sha string) string {
	if s.platform.store == nil {
		return ""
	}
	return avatar.URL(s.platform.cfg.APIBase(), sha)
}

// directory resolves handles to their display. nil with teams mode off or when the control plane
// cannot be read (logged): the rows then show their handles only, as before, and the call still works.
func (s *Server) directory(ctx context.Context, handles []string) map[string]personDisplay {
	if s.platform.store == nil || len(handles) == 0 {
		return nil
	}
	people, err := s.platform.store.PeopleByHandles(ctx, handles)
	if err != nil {
		slog.Default().WarnContext(ctx, "people directory", slog.String("err", err.Error()))
		return nil
	}
	out := make(map[string]personDisplay, len(people))
	for h, p := range people {
		out[h] = s.displayOf(p)
	}
	return out
}

// withDisplay fills an AdminAccount's name, @username and photo from the directory.
func withDisplay(acc *pb_admin.AdminAccount, d personDisplay) {
	acc.Name, acc.PublicUsername, acc.AvatarUrl = d.Name, d.PublicUsername, d.AvatarURL
}

// entryNeeded is Workspace.entry_needed (P0 §4): the caller's membership still owes its first entry.
// false with teams mode off, for a legacy token (no user id), for an entered operator, and when the
// flag cannot be read (logged): the welcome screen must never become a wall.
func (s *Server) entryNeeded(ctx context.Context, w authsrv.Workspace) bool {
	if s.platform.store == nil || w.Operator || w.UserID <= 0 || w.Team.ID <= 0 {
		return false
	}
	pending, err := s.platform.store.EntryPending(ctx, w.Team.ID, w.UserID)
	if err != nil {
		slog.Default().WarnContext(ctx, "entry pending", slog.String("err", err.Error()))
		return false
	}
	return pending
}
