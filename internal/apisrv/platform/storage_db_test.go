package platform

import (
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSetTeamStorageCap(t *testing.T) {
	st := freshStore(t)
	addTeams(t, st, 2, 1)
	s := newSrv(st)
	ctx := opCtx()

	// default cap (20 GB) on a team with no cap of its own; team #1 starts with no cap
	list, err := s.ListTeams(ctx, &pb_admin.ListTeamsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	caps := map[string]int64{}
	for _, tm := range list.GetTeams() {
		caps[tm.GetSlug()] = tm.GetStorageCapBytes()
	}
	if caps["team2"] != 20<<30 || caps["grbpwr"] != 0 {
		t.Fatalf("caps = %v", caps)
	}

	res, err := s.SetTeamStorageCap(ctx, &pb_admin.SetTeamStorageCapRequest{TeamSlug: "team2", CapBytes: 5 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetTeam().GetStorageCapBytes() != 5<<30 {
		t.Fatalf("cap = %d", res.GetTeam().GetStorageCapBytes())
	}
	exec(t, st, `UPDATE team_storage SET used_bytes = 123 WHERE team_id = 2`)
	got, err := s.GetTeam(ctx, &pb_admin.GetTeamRequest{TeamSlug: "team2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTeam().GetStorageUsedBytes() != 123 || got.GetTeam().GetStorageCapBytes() != 5<<30 {
		t.Fatalf("team = %+v", got.GetTeam())
	}

	res, err = s.SetTeamStorageCap(ctx, &pb_admin.SetTeamStorageCapRequest{TeamSlug: "team2", NoCap: true})
	if err != nil || res.GetTeam().GetStorageCapBytes() != 0 {
		t.Fatalf("no cap: %v %v", res, err)
	}
	if _, err := s.SetTeamStorageCap(ctx, &pb_admin.SetTeamStorageCapRequest{TeamSlug: "grbpwr", CapBytes: 1 << 40}); err != nil {
		t.Fatalf("team #1 cap: %v", err)
	}

	for _, bad := range []*pb_admin.SetTeamStorageCapRequest{
		{TeamSlug: "team2"},
		{TeamSlug: "team2", CapBytes: -1},
		{TeamSlug: "team2", CapBytes: 5, NoCap: true},
		{TeamSlug: "team2", CapBytes: 1 << 60},
	} {
		if _, err := s.SetTeamStorageCap(ctx, bad); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v: %v, want INVALID_ARGUMENT", bad, err)
		}
	}
	var n int
	if err := st.DB().Get(&n, `SELECT COUNT(*) FROM platform_audit WHERE action = 'team_storage_cap'`); err != nil || n != 3 {
		t.Fatalf("audit rows = %d, %v", n, err)
	}
}
